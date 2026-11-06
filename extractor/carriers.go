package extractor

import (
	"net/http"
	"strings"

	"golang.org/x/net/html"
)

// 本文件收集「边缘载体」：不是 a[href] / script[src] 这种主流位置，
// 但在真实站点上确实会暴露路径的属性。
//
// 两类：
//  1. meta 的 og:* / twitter:* 分享元数据，常指向分享页变体与卡片图；
//  2. data-* 自定义属性，现代前端喜欢用它挂 API 端点（data-api-endpoint）。
//
// 共同前提是先判断「值是否真的像一个地址」，否则 data-id="123"、
// og:site_name="Vue.js" 这类会被当成路径，制造必然 404 的噪音。

// pathShaped 判断字符串是否具备地址形态。
//
// 只接受有明确起点的写法（`/`、`./`、`../`、`//`、`http(s)://`）。
// 裸相对路径（`img/a.png`）故意不收：data-* 里这类值多半是配置键或文案，
// 收益远小于误报成本。
func pathShaped(s string) bool {
	if s == "" || len(s) > 2048 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n<>") {
		return false
	}
	switch {
	case strings.HasPrefix(s, "//"),
		strings.HasPrefix(s, "/"),
		strings.HasPrefix(s, "./"),
		strings.HasPrefix(s, "../"),
		strings.HasPrefix(s, "http://"),
		strings.HasPrefix(s, "https://"):
		return true
	}
	return false
}

// metaURLKeys 是 content 里放地址的 meta 键。
//
// 精确匹配而不是按 `og:` 前缀全收：`og:site_name`、`og:type`、
// `og:locale` 同样是 og:*，但值是文案，全收会带进一堆假路径。
var metaURLKeys = map[string]bool{
	"og:url":         true,
	"og:image":       true,
	"og:image:url":   true,
	"og:video":       true,
	"og:audio":       true,
	"twitter:url":    true,
	"twitter:image":  true,
	"twitter:player": true,
}

// metaPropertyURL 从 meta 的 property/name/itemprop 判定它是否承载地址。
//
// 只返回「键命中且值像地址」的结果：键命中但值不像地址时返回 false，
// 由调用方继续走 refresh 之类的其它判定。
func metaPropertyURL(n *html.Node, content string) (string, string, bool) {
	for _, key := range []string{"property", "name", "itemprop"} {
		v, ok := AttrVal(n, key)
		if !ok {
			continue
		}
		v = strings.ToLower(strings.TrimSpace(v))
		if !metaURLKeys[v] {
			continue
		}
		s := strings.TrimSpace(content)
		if pathShaped(s) {
			return s, v, true
		}
		return "", "", false
	}
	return "", "", false
}

// dataAttrSkipped 是明确不收的 data-* 键。
//
// data-id / data-index / data-key 这类是标识符或序号，即使值以 `/` 开头
// （例如 `data-key="/"` 这种键位绑定）也不是资源地址。
var dataAttrSkipped = map[string]bool{
	"data-id":        true,
	"data-index":     true,
	"data-key":       true,
	"data-keycode":   true,
	"data-testid":    true,
	"data-test":      true,
	"data-cy":        true,
	"data-reactid":   true,
	"data-reactroot": true,
	"data-v-app":     true,
}

// dataAttrRequests 收集 data-* 属性里形似地址的值。
//
// 与 tagAttrMap 里显式列出的 data-src / data-original 并存：
// 那两个是 src 的替代写法，语义明确；这里覆盖的是任意 data-* 键，
// 靠形状判定兜住，重复命中由下游去重。
func dataAttrRequests(n *html.Node, tag string) []Request {
	var out []Request
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		if !strings.HasPrefix(key, "data-") || dataAttrSkipped[key] {
			continue
		}
		v := strings.TrimSpace(a.Val)
		if !pathShaped(v) {
			continue
		}
		out = append(out, Request{
			Raw: v, Method: http.MethodGet, Source: tag, Attribute: key, Tag: tag,
		})
	}
	return out
}
