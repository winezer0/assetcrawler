package extractor

import (
	"net/http"
	"regexp"
	"strings"
)

// JS endpoint 提取：这是「不依赖浏览器」方案里最关键也最容易做脏的一环。
//
// 做法是从脚本源码里用保守正则捞三类东西：
//  1. 完整绝对 URL（https://...）
//  2. 带引号的绝对路径（"/api/user/list"），前后端分离项目的主要来源
//  3. 带引号的相对路径（"./api/x"、"../v2/x"）
//
// 刻意不做的事：不解析 AST、不拼接字符串、不处理 \x2f 这类转义。
// 原因是收益递减：真正值钱的接口路径基本都以完整字面量出现，
// 而做 AST 解析就要引入 treesitter 这类重依赖，反而背离了轻量目标。
var (
	// 绝对 URL。排除尾部的引号与空白，避免把 JSON 标点吃进来。
	jsAbsURLRe = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#@!$&*+,;=%]+`)

	// 引号包裹的绝对路径，可带 query。\x60 是反引号，用于兼容模板字符串。
	jsQuotedPathRe = regexp.MustCompile("[\"'\x60](/[A-Za-z0-9_\\-./~%]*(?:\\?[A-Za-z0-9_\\-=&%.]*)?)[\"'\x60]")

	// 引号包裹的相对路径（以 ./ 或 ../ 开头）。
	jsRelPathRe = regexp.MustCompile("[\"'\x60](\\.{1,2}/[A-Za-z0-9_\\-./~%]+)[\"'\x60]")

	// 不带前导斜杠的多段路径，如 "api/v1/users"。限制至少两段，
	// 否则 "text/html"、"utf-8" 这类 MIME 与编码串会造成大量误报。
	jsMultiSegPathRe = regexp.MustCompile("[\"'\x60]([A-Za-z0-9_\\-]+(?:/[A-Za-z0-9_\\-]+){1,8}(?:\\?[A-Za-z0-9_\\-=&%.]*)?)[\"'\x60]")

	// sourceMappingURL 注释，能直接暴露前端源码与构建结构，价值很高。
	jsSourceMapRe = regexp.MustCompile(`(?m)[#@]\s*sourceMappingURL=([^\s*]+)`)
)

// jsNoiseRe 过滤明显不是接口路径的字符串。
var jsNoiseRe = regexp.MustCompile(`(?i)^(/|//|/\*|\*/)$|[%$]\{|\$\{|%s|%d|\{\{`)

// FromJS 从脚本或样式内容中提取 URL 候选。
//
// 若注入了 JSExtract（AST 引擎），先取其结果，再叠加正则结果：
// AST 擅长还原拼接与调用点语义，正则擅长兜住 AST 解析失败或漏掉的字面量，
// 两者合并后由下游 frontier 统一去重。
func FromJS(content string, o Options) []Request {
	if o.JSExtract != nil {
		if reqs, ok := o.JSExtract(content); ok {
			return reqs
		}
	}
	return fromJSRegex(content)
}

// FromCSS 从样式表内容中提取 url(...)。
//
// CSS 必须单独处理：把 `.css` 送进 JS 解析器必然失败，回退正则后又只能捞到
// 带引号的字符串，真正的 url() 因为落在静态资源过滤里全部丢弃。
func FromCSS(content string) []Request {
	var out []Request
	seen := make(map[string]struct{}, 16)
	for _, m := range cssURLRe.FindAllStringSubmatch(content, -1) {
		raw := strings.TrimSpace(m[1])
		if !cssRefUsable(raw) {
			continue
		}
		if _, dup := seen[raw]; dup {
			continue
		}
		seen[raw] = struct{}{}
		out = append(out, Request{Raw: raw, Method: http.MethodGet, Source: "css"})
	}
	return out
}

// fromJSRegex 是零依赖的正则实现，作为 AST 引擎未启用或解析失败时的兜底。
func fromJSRegex(content string) []Request {
	if content == "" {
		return nil
	}

	var out []Request
	seen := make(map[string]struct{}, 64)

	push := func(raw, source string) {
		raw = strings.TrimSpace(raw)
		if raw == "" || len(raw) > 2048 {
			return
		}
		if jsNoiseRe.MatchString(raw) {
			return
		}
		// 模板占位符处理不了，跳过以免产生无法抓取的假路径
		if strings.ContainsAny(raw, "${}") {
			return
		}
		// JS 里的静态图片/字体路径数量巨大且价值低，直接丢弃；
		// 它们若真被页面引用，会从 HTML 那边正常发现。
		if IsStaticAsset(raw) {
			return
		}
		// 只剩根路径的无意义结果
		if raw == "/" {
			return
		}
		if _, dup := seen[raw]; dup {
			return
		}
		seen[raw] = struct{}{}
		out = append(out, Request{Raw: raw, Method: http.MethodGet, Source: source})
	}

	for _, m := range jsAbsURLRe.FindAllString(content, -1) {
		push(m, "js")
	}
	for _, m := range jsQuotedPathRe.FindAllStringSubmatch(content, -1) {
		push(m[1], "js")
	}
	for _, m := range jsRelPathRe.FindAllStringSubmatch(content, -1) {
		push(m[1], "js")
	}
	for _, m := range jsMultiSegPathRe.FindAllStringSubmatch(content, -1) {
		push(m[1], "js")
	}
	for _, m := range jsSourceMapRe.FindAllStringSubmatch(content, -1) {
		push(m[1], "sourcemap")
	}

	return out
}
