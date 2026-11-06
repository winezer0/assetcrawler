// Package extractor 从 HTML、响应头、脚本内容、robots.txt、sitemap.xml 中
// 穷尽式提取 URL 候选。
//
// 设计约束（有意为之）：
//   - 纯函数，无状态、无 IO、无并发
//   - 只输出「原始字符串」，不做相对路径解析、不做归一化、不做去重
//     （归一化属 frontier 的职责，解析基准属调用方上下文）
//   - 不依赖本工程其他包
//
// 这样它可以直接被别的工具 import：给它一段 HTML，还你一堆 URL 字符串，
// 至于这些字符串怎么解析、要不要访问，调用方自己决定。
package extractor

import (
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// Request 是一个提取到的 URL 候选。Raw 可能是相对路径，需调用方结合上下文解析。
type Request struct {
	Raw       string
	Method    string
	Source    string // 来源分类：标签名 / header:xxx / js / style 等
	Attribute string
	Tag       string
	// Confidence 为空表示地址可确定；"template" 表示含 EXPR 占位符，不可直接访问。
	Confidence string
	// Kind 标记来源类别，目前用于区分 "chunk"（打包产物分片）与 "route"（前端路由）。
	// 为空表示普通提取结果。
	Kind string
	// Params 是从上下文推断出的参数名（表单控件、JS 调用参数对象）。
	// 只有名字没有取值——取不到真实值，编造一个反而会污染结果。
	Params []string
}

// Options 控制提取的深度。默认只做安全的部分。
type Options struct {
	// ExtractJS 是否从 <script> 内联代码与脚本文件中提取接口路径。
	ExtractJS bool
	// ExtractStyle 是否从 style 属性与 <style> 块中提取 url()。
	ExtractStyle bool
	// JSExtract 可选：注入的 AST 级 JS 提取器（由上层按引擎选择装配）。
	// 第二个返回值为 true 表示解析成功——此时不再叠加正则，
	// 否则会把 AST 已经合并好的结果重新拆成碎片。
	// 为 nil 时只用正则，extractor 因此保持零第三方依赖。
	JSExtract func(src string) ([]Request, bool)
}

// DefaultOptions 返回推荐配置。
func DefaultOptions() Options {
	return Options{ExtractJS: true, ExtractStyle: true}
}

// tagAttrMap 是 HTML 提取的核心清单。
//
// 这份清单比「只取 a[href] + script[src]」重要得多：老系统里的
// applet/object/embed、后台系统里的 form[action]、单页应用里的
// video[poster]、CDN 配置里的 link[href]、以及 base[href] 都会
// 直接暴露后端路径。漏掉任何一个都会导致资产覆盖不全。
var tagAttrMap = map[string][]string{
	"a":          {"href"},
	"area":       {"href"},
	"base":       {"href"},
	"link":       {"href"},
	"script":     {"src"},
	"img":        {"src", "srcset", "longdesc", "data-src", "data-original"},
	"source":     {"src", "srcset"},
	"track":      {"src"},
	"video":      {"src", "poster"},
	"audio":      {"src"},
	"iframe":     {"src"},
	"frame":      {"src"},
	"embed":      {"src"},
	"object":     {"data", "codebase"},
	"applet":     {"code", "codebase", "archive"},
	"form":       {"action"},
	"input":      {"src", "formaction"},
	"button":     {"formaction"},
	"blockquote": {"cite"},
	"q":          {"cite"},
	"del":        {"cite"},
	"ins":        {"cite"},
	"body":       {"background"},
	"table":      {"background"},
	"tr":         {"background"},
	"td":         {"background"},
	"th":         {"background"},
	"html":       {"manifest"},
	"use":        {"href"},
	"image":      {"href"},
	"isindex":    {"action"},
	"menuitem":   {"icon"},
	"command":    {"icon"},
	"meta":       {"content"}, // 由 metaRefreshURL 单独判断是否为 refresh
}

var (
	// cssURLRe 提取 url(...) 中的资源地址，兼容单双引号与无引号写法。
	cssURLRe = regexp.MustCompile(`(?i)url\(\s*['"]?([^'")\s]+)['"]?\s*\)`)
	// metaRefreshRe 解析 <meta http-equiv="refresh" content="0; url=/x"> 与 Refresh 响应头。
	metaRefreshRe = regexp.MustCompile(`(?i)^\s*\d*\s*[;,]\s*url\s*=\s*(.+)$`)
	// linkHeaderRe 解析响应头 Link: <https://x/y>; rel=preload。
	linkHeaderRe = regexp.MustCompile(`<([^>]+)>`)

	// 以下三个用于 HTML 注释里的路径。注释常被当作「不渲染所以无价值」而忽略，
	// 但在老 ASP/JSP 站点里恰恰相反：SSI 的 #include、Dreamweaver 的模板声明
	// 都写在注释里，而这些路径往往指向前端根本看不见的模板/配置文件。
	//
	// <!--#include virtual="/inc/header.asp" --> 或 file="x.asp"
	ssiIncludeRe = regexp.MustCompile(`(?i)#\s*include\s+(?:virtual|file)\s*=\s*["']([^"']+)["']`)
	// <!-- InstanceBegin template="/Templates/MainTemplate.dwt.asp" -->
	dwtTemplateRe = regexp.MustCompile(`(?i)InstanceBegin\s+template\s*=\s*["']([^"']+)["']`)
	// 注释里其它带扩展名的绝对路径，限制扩展名以压住噪音
	commentPathRe = regexp.MustCompile(`["'](/[A-Za-z0-9_\-./%]+\.(?:asp|aspx|php|jsp|do|action|html?|js|css|xml|json|dwt|inc))["']`)
)

// walkElements 深度优先遍历所有元素节点。
func walkElements(n *html.Node, fn func(*html.Node)) {
	if n == nil {
		return
	}
	if n.Type == html.ElementNode {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkElements(c, fn)
	}
}

// walkAll 深度优先遍历全部节点（含注释与文本）。
func walkAll(n *html.Node, fn func(*html.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkAll(c, fn)
	}
}

// AttrVal 取属性值，兼容大小写与 xlink 命名空间。
func AttrVal(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val, true
		}
		// xlink:href 在 x/net/html 中表现为 Namespace=xlink, Key=href
		if a.Namespace == "xlink" && strings.EqualFold(key, "href") && strings.EqualFold(a.Key, "href") {
			return a.Val, true
		}
	}
	return "", false
}

// formParamNames 收集表单里可提交的控件名。
//
// 这是性价比最高的参数来源：一个后台登录页就能给出 username / password / captcha，
// 而在此之前这些名字全部丢失，只剩下一个光秃秃的 action。
// 跳过纯按钮类控件（不会产生参数）与 disabled 控件。
func formParamNames(form *html.Node) []string {
	var out []string
	seen := map[string]struct{}{}
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if n == nil || depth > 12 || len(out) >= 32 {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode {
				tag := strings.ToLower(c.Data)
				if tag == "form" && c != form {
					continue // 嵌套表单不属于当前表单
				}
				if tag == "input" || tag == "select" || tag == "textarea" || tag == "button" {
					if name, ok := AttrVal(c, "name"); ok {
						if _, disabled := AttrVal(c, "disabled"); !disabled {
							name = strings.TrimSpace(name)
							typ, _ := AttrVal(c, "type")
							switch strings.ToLower(typ) {
							case "submit", "button", "reset", "image", "file":
							default:
								if name != "" && !strings.ContainsAny(name, " \t\n\r&=") {
									if _, dup := seen[name]; !dup {
										seen[name] = struct{}{}
										out = append(out, name)
									}
								}
							}
						}
					}
				}
			}
			walk(c, depth+1)
		}
	}
	walk(form, 0)
	return out
}

// cssRefUsable 判断 url(...) 里的引用是否值得当资产。
//
// `url(#filter)` 是同一文档内的 SVG / 渐变引用，按 URL 解析会得到
// `/assets/%23filter` 这种必然 404 的地址——实测 vuejs.org 上因此产生 32 条 404。
// `data:`、`about:` 同理。
func cssRefUsable(raw string) bool {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return false
	case strings.HasPrefix(raw, "#"):
		return false
	case strings.HasPrefix(raw, "%23"):
		// 百分号编码的 #：常见于 data URI 内联 SVG 里的
		// `url(%23filter)`，那是同文档引用，不是资源路径。
		return false
	case strings.HasPrefix(raw, "data:"):
		return false
	case strings.HasPrefix(raw, "about:"):
		return false
	}
	return true
}

// ParseSrcset 解析 srcset，返回其中每个候选资源的地址（丢弃宽度/密度描述符）。
func ParseSrcset(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	return out
}

// BaseHref 找出 <base href>，调用方可用它作为相对路径的解析基准。
func BaseHref(doc *html.Node) string {
	var href string
	walkElements(doc, func(n *html.Node) {
		if href != "" || !strings.EqualFold(n.Data, "base") {
			return
		}
		if v, ok := AttrVal(n, "href"); ok {
			href = v
		}
	})
	return href
}

// FromHTML 从已解析的 DOM 中穷尽式提取 URL 原始字符串。
func FromHTML(doc *html.Node, o Options) []Request {
	if doc == nil {
		return nil
	}

	var out []Request

	walkElements(doc, func(n *html.Node) {
		tag := strings.ToLower(n.Data)

		// 1) 标准标签-属性组合
		if attrs, ok := tagAttrMap[tag]; ok {
			for _, a := range attrs {
				v, ok := AttrVal(n, a)
				if !ok || strings.TrimSpace(v) == "" {
					continue
				}
				switch strings.ToLower(a) {
				case "srcset":
					for _, s := range ParseSrcset(v) {
						out = append(out, Request{Raw: s, Method: http.MethodGet, Source: tag, Attribute: a, Tag: tag})
					}
				case "content":
					// meta 的 content 有两类承载：refresh 跳转，以及 og:*/twitter:* 分享元数据
					if loc, ok := metaRefreshURL(n, v); ok {
						out = append(out, Request{Raw: loc, Method: http.MethodGet, Source: "meta-refresh", Attribute: a, Tag: tag})
						break
					}
					if loc, key, ok := metaPropertyURL(n, v); ok {
						out = append(out, Request{Raw: loc, Method: http.MethodGet, Source: "meta-" + key, Attribute: a, Tag: tag})
					}
				case "action":
					m := http.MethodGet
					if mv, ok := AttrVal(n, "method"); ok && strings.EqualFold(strings.TrimSpace(mv), "post") {
						m = http.MethodPost
					}
					// 表单控件名作为「参数形状」单独带出，不拼进 URL：
					// POST 表单的参数在 body 里，拼到 query 上会改变语义。
					out = append(out, Request{
						Raw: v, Method: m, Source: tag, Attribute: a, Tag: tag,
						Params: formParamNames(n),
					})
				case "archive":
					// applet[archive] 可能是逗号分隔的多文件
					for _, p := range strings.Split(v, ",") {
						out = append(out, Request{Raw: strings.TrimSpace(p), Method: http.MethodGet, Source: tag, Attribute: a, Tag: tag})
					}
				default:
					out = append(out, Request{Raw: v, Method: http.MethodGet, Source: tag, Attribute: a, Tag: tag})
				}
			}
		}

		// 2) htmx 属性：hx-get / hx-post / hx-put / hx-delete / hx-patch / hx-src
		for _, at := range n.Attr {
			k := strings.ToLower(at.Key)
			if !strings.HasPrefix(k, "hx-") || strings.TrimSpace(at.Val) == "" {
				continue
			}
			m := http.MethodGet
			switch k {
			case "hx-post":
				m = http.MethodPost
			case "hx-put":
				m = http.MethodPut
			case "hx-delete":
				m = http.MethodDelete
			case "hx-patch":
				m = http.MethodPatch
			case "hx-get", "hx-src", "hx-params", "hx-include":
			default:
				continue // 其他 hx-* 不是 URL
			}
			out = append(out, Request{Raw: at.Val, Method: m, Source: "htmx", Attribute: at.Key, Tag: tag})
		}

		// 3) 内联 style 属性里的 url()
		if o.ExtractStyle {
			if st, ok := AttrVal(n, "style"); ok {
				for _, m := range cssURLRe.FindAllStringSubmatch(st, -1) {
					if !cssRefUsable(m[1]) {
						continue
					}
					out = append(out, Request{Raw: m[1], Method: http.MethodGet, Source: "style-attr", Attribute: "style", Tag: tag})
				}
			}
		}

		// 4) <style> 块内容
		if o.ExtractStyle && strings.EqualFold(tag, "style") {
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type != html.TextNode {
					continue
				}
				for _, m := range cssURLRe.FindAllStringSubmatch(c.Data, -1) {
					if !cssRefUsable(m[1]) {
						continue
					}
					out = append(out, Request{Raw: m[1], Method: http.MethodGet, Source: "style-block", Attribute: "style", Tag: tag})
				}
			}
		}

		// 5) 内联 <script> 内容：前后端分离项目常把接口路径直接写死在页面里
		if o.ExtractJS && strings.EqualFold(tag, "script") {
			var sb strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.TextNode {
					sb.WriteString(c.Data)
				}
			}
			if sb.Len() > 0 {
				out = append(out, FromJS(sb.String(), o)...)
			}
		}

		// 6) data-* 自定义属性：现代前端常把接口端点挂在 data-* 上
		out = append(out, dataAttrRequests(n, tag)...)
	})

	// 7) 注释里的路径
	out = append(out, fromComments(doc)...)

	return out
}

// fromComments 从 HTML 注释里提取路径。
//
// 这一条是被实测逼出来的：testasp.vulnweb.com 的首页把真实模板路径
// 写在 Dreamweaver 的 InstanceBegin 注释里，只解析元素节点的爬虫会整体漏掉。
// 同理，SSI 的 #include 也藏在注释里，而它指向的往往是 include 目录下的配置文件。
func fromComments(doc *html.Node) []Request {
	var out []Request
	seen := make(map[string]struct{}, 8)

	add := func(raw, source string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if _, dup := seen[raw]; dup {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, Request{Raw: raw, Method: http.MethodGet, Source: source, Attribute: "comment", Tag: "comment"})
	}

	walkAll(doc, func(n *html.Node) {
		if n.Type != html.CommentNode {
			return
		}
		for _, m := range ssiIncludeRe.FindAllStringSubmatch(n.Data, -1) {
			add(m[1], "comment:ssi")
		}
		for _, m := range dwtTemplateRe.FindAllStringSubmatch(n.Data, -1) {
			add(m[1], "comment:dwt")
		}
		for _, m := range commentPathRe.FindAllStringSubmatch(n.Data, -1) {
			add(m[1], "comment")
		}
	})
	return out
}
func metaRefreshURL(n *html.Node, content string) (string, bool) {
	he, ok := AttrVal(n, "http-equiv")
	if !ok || !strings.EqualFold(strings.TrimSpace(he), "refresh") {
		return "", false
	}
	m := metaRefreshRe.FindStringSubmatch(content)
	if len(m) < 2 {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(m[1]), `"'`), true
}

// FromHeaders 从响应头提取 URL。
//
// Location / Content-Location / Link / Refresh 这四个头命中率很高：
// 网关、CDN、WAF 经常通过 Link 头暴露内部路径，而 Refresh 则是老系统做跳转的常见手段。
func FromHeaders(h http.Header) []Request {
	if h == nil {
		return nil
	}
	var out []Request

	add := func(source, raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		out = append(out, Request{Raw: raw, Method: http.MethodGet, Source: "header:" + source, Attribute: source})
	}

	add("location", h.Get("Location"))
	add("content-location", h.Get("Content-Location"))

	for _, hv := range h.Values("Link") {
		for _, m := range linkHeaderRe.FindAllStringSubmatch(hv, -1) {
			add("link", m[1])
		}
	}

	if rv := h.Get("Refresh"); rv != "" {
		if m := metaRefreshRe.FindStringSubmatch(rv); len(m) >= 2 {
			add("refresh", strings.Trim(strings.TrimSpace(m[1]), `"'`))
		}
	}

	return out
}
