package assetcrawler

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"assetcrawler/csp"
)

// CSP 线索的 Kind 取值。
const (
	// KindCSPSource 是策略允许的来源，多数是域名根（`https://bcd.example.org/`）。
	KindCSPSource = "csp-source"
	// KindCSPReport 是违规 / 网络错误的上报端点，通常带完整路径。
	KindCSPReport = "csp-report"
)

// cspHint 是一条 CSP 线索。
type cspHint struct {
	URL    *url.URL
	Source string
	Kind   string
}

// cspHints 把 CSP 响应头拆成线索。
//
// 这类结果**只报告不抓取**，原因是实测出来的：
//   - 来源条目绝大多数是域名根路径。抽 5 个子域实测，只有 1 个返回 200，
//     2 个 404、2 个连不上——抓它只是把配额换成 404；
//   - 但它的价值不在 URL 而在**域名**：`bcd.developer.mozilla.org`、
//     `js.stripe.com`、`challenges.cloudflare.com` 这类是目标主动信任的服务端，
//     属于资产面的直接增量；
//   - 上报端点带一次性 token，抓取等于向目标注入伪造上报数据。
//
// 所以默认「结构化线索」而不是「待抓 URL」：结果里有、队列里没有。
func cspHints(h http.Header, base *url.URL) []cspHint {
	res := csp.ParseHeader(h)
	if len(res.Items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(res.Items))
	out := make([]cspHint, 0, len(res.Items))
	for _, it := range res.Items {
		if inboundDirectives[it.Directive] {
			continue
		}
		// 同 host 的来源不带来新信息：`connect-src 'self' https://stripe.com`
		// 里的 stripe.com 就是当前页面本身，收进来只会制造
		// `https://stripe.com` 与 `https://stripe.com/` 并存的重复。
		if base != nil && it.Host != "" && strings.EqualFold(it.Host, base.Hostname()) {
			continue
		}
		u := cspItemURL(it, base)
		if u == nil {
			continue
		}
		key := u.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, cspHint{URL: u, Source: "csp:" + it.Directive, Kind: cspKind(it)})
	}
	return out
}

// inboundDirectives 的语义是「谁可以对我做什么」，列出的是外部页面，
// 不是目标自己的资产。实测 vuejs.org 的 frame-ancestors 一条就能带进
// 6 个第三方站点（certificates.dev、alemira.com…），全部是噪音。
var inboundDirectives = map[string]bool{
	"frame-ancestors": true,
}

func cspKind(it csp.Item) string {
	if it.Kind == csp.KindReport {
		return KindCSPReport
	}
	return KindCSPSource
}

// cspItemURL 把一条 CSP 条目还原成绝对地址。
//
// 三类跳过：只有 scheme（`https:`）、无法定位主机的通配（`*.example.com` 去掉
// 前缀后剩下的主域通常已在队列里，猜它没有意义）、以及既无主机又无路径的条目。
func cspItemURL(it csp.Item, base *url.URL) *url.URL {
	if it.SchemeOnly {
		return nil
	}
	// 站内相对路径（`report-uri /csp-report`）：按来源页面解析
	if it.Host == "" {
		if it.Path == "" || base == nil {
			return nil
		}
		u, err := base.Parse(it.Path)
		if err != nil {
			return nil
		}
		return u
	}
	// 通配主机无法定位：`*.example.com` 抓哪个子域都是猜，
	// 而主域本身通常已经在队列里。顺带防住 `https://*.x.com` 这种
	// 带 scheme 的通配——csp 包对它的 Wildcard 标志是 false。
	if it.Wildcard || strings.Contains(it.Host, "*") {
		return nil
	}

	// 优先按原始 token 解析：CSP 里 scheme 是显式写出来的
	// （`https://api.example.com`），不能拿来源页面的 scheme 顶替。
	if u, err := url.Parse(it.Value); err == nil && u.Hostname() != "" {
		if u.Port() == "" && it.Port != "" {
			u.Host = net.JoinHostPort(u.Hostname(), it.Port)
		}
		u.Fragment = ""
		return u
	}

	scheme := "https"
	if base != nil && base.Scheme == "http" {
		scheme = "http"
	}
	host := it.Host
	if it.Port != "" {
		host = net.JoinHostPort(host, it.Port)
	}
	path, query := splitPathQuery(it.Path)
	return &url.URL{Scheme: scheme, Host: host, Path: path, RawQuery: query}
}

// splitPathQuery 拆开 CSP 包里合并存储的 path 与 query。
func splitPathQuery(p string) (string, string) {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i], p[i+1:]
	}
	return p, ""
}

// emitCSPHints 输出 CSP 线索。
//
// 去重走 frontier.MarkSeen 而不是 Push：这些地址不参与抓取，
// 不该受 scope 约束——`connect-src https://api.other.com` 的重点恰恰是
// 它不在本站范围内。MarkSeen 只登记不判定，正好。
func (c *Crawler) emitCSPHints(h http.Header, base *url.URL, depth int, referer string) {
	for _, hint := range cspHints(h, base) {
		if !c.frontier.MarkSeen(hint.URL) {
			continue
		}
		c.emit(Result{
			URL:     hint.URL.String(),
			Source:  hint.Source,
			Depth:   depth,
			Referer: referer,
			Kind:    hint.Kind,
			// StatusCode 为 0 表示「推测出来的线索，未访问」
		})
	}
}
