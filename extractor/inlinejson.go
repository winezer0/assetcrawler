package extractor

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// 内联 hydration 载体：Nuxt 3 / Next.js Pages Router / Vue SSR 会把整棵
// 页面数据序列化进 <script type="application/json">，里面的导航路径、
// 接口地址在 HTML 标签与外链 JS 里都找不到。
//
// 实测 nuxt.com 首页：__NUXT_DATA__ 约 200 KB，含 460 条站内绝对路径，
// 现有提取只覆盖 50 条。而且 Nuxt 把斜杠写成 \u002F（`\u002Fdocs\u002F4.x`），
// 按普通 JS 或正则扫字面量都拿不到——必须按 JSON 解码后再取字符串值。
//
// 这里刻意不复用 FromJSON：响应体 JSON 与 hydration payload 的噪音形态不同
// （后者混着大量文案、ID、枚举值），需要更严的形状判定和独立的条数上限。

// inlineJSONScripts 是表驱动的识别规则：script[id] → source 后缀。
// 后缀进 Source 字段，排查时能一眼看出是哪个框架的数据。
var inlineJSONScripts = map[string]string{
	"__NUXT_DATA__":     "nuxt-data",
	"__NEXT_DATA__":     "next-data",
	"__INITIAL_STATE__": "initial-state",
}

// maxInlineJSONURLs 限制单段 payload 的产出，避免大型导航树灌满队列。
const maxInlineJSONURLs = 200

// inlineJSONStringRe 是「payload 不是合法 JSON」时的保守兜底，
// 只取带引号的字面量，不扫裸字符串。
var inlineJSONStringRe = regexp.MustCompile(`"((?:[^"\\]|\\.){2,300})"`)

// inlineJSONKind 判断 <script> 是否是 hydration JSON 载体。
//
// 只认 id 白名单，或 type=application/json 且带 data-nuxt-data 标记；
// 刻意不认 application/ld+json（schema.org 结构化数据），那是另一类内容。
func inlineJSONKind(n *html.Node) (string, bool) {
	if n == nil || !strings.EqualFold(n.Data, "script") {
		return "", false
	}
	var id, typ string
	var nuxtData bool
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "id":
			id = a.Val
		case "type":
			typ = strings.ToLower(strings.TrimSpace(a.Val))
		case "data-nuxt-data":
			nuxtData = true
		}
	}
	if kind, ok := inlineJSONScripts[id]; ok {
		return kind, true
	}
	if typ == "application/json" && nuxtData {
		return "nuxt-data", true
	}
	return "", false
}

// FromInlineJSON 从一段 hydration payload 里提取站内地址。
//
// 只收解码后以 / 或 http(s) 开头的字面量，不合成 URL、不猜测相对名。
func FromInlineJSON(payload, kind string) []Request {
	source := "inline-json:" + kind
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err == nil {
		return walkInlineJSON(v, source)
	}
	return inlineJSONFallback(payload, source)
}

func walkInlineJSON(v any, source string) []Request {
	out := make([]Request, 0, 16)
	seen := make(map[string]struct{}, 32)
	var walk func(any, int)
	walk = func(node any, depth int) {
		if depth > 24 || len(out) >= maxInlineJSONURLs {
			return
		}
		switch t := node.(type) {
		case map[string]any:
			for _, val := range t {
				walk(val, depth+1)
			}
		case []any:
			for _, val := range t {
				walk(val, depth+1)
			}
		case string:
			if !inlinePathUsable(t) {
				return
			}
			if _, dup := seen[t]; dup {
				return
			}
			seen[t] = struct{}{}
			out = append(out, Request{Raw: t, Method: http.MethodGet, Source: source})
		}
	}
	walk(v, 0)
	return out
}

// inlineJSONFallback 处理带赋值前缀（不是纯 JSON）的初始化脚本。
func inlineJSONFallback(payload, source string) []Request {
	var out []Request
	seen := make(map[string]struct{}, 32)
	for _, m := range inlineJSONStringRe.FindAllStringSubmatch(payload, -1) {
		s, ok := unescapeJSString(m[1])
		if !ok || !inlinePathUsable(s) {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, Request{Raw: s, Method: http.MethodGet, Source: source})
		if len(out) >= maxInlineJSONURLs {
			break
		}
	}
	return out
}

// inlinePathUsable 是比 jsonURL 更严的一层：payload 里混着大量文案与 ID。
func inlinePathUsable(s string) bool {
	if !jsonURL(s) || len(s) > 300 {
		return false
	}
	// 模板占位、JS 插值、转义残留都不该当地址
	if strings.Contains(s, "EXPR") || strings.ContainsAny(s, "${}\\") {
		return false
	}
	// 图片/字体等静态资源由标签属性提取，不在导航数据里重复收
	if IsStaticAsset(s) {
		return false
	}
	return true
}
