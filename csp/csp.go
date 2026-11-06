// Package csp 从 HTTP 响应头里提取可用于资产发现的线索。
//
// 处理四类头：
//   - Content-Security-Policy
//   - Content-Security-Policy-Report-Only
//   - Report-To（NEL / 违规上报的分组与端点）
//   - Reporting-Endpoints
//
// 它只做解析与归一，不发请求、不爬取、不依赖本工程的其它包。
// 需要的地方直接调用 ParseHeader 即可，不会影响现有结构。
//
// 典型用途：CSP 的 connect-src / img-src 等指令会声明一批
// 「页面 HTML 与 JS 里都未必出现」的基础设施域名；report-uri 则直接给出上报接口。
//
//	r := csp.ParseHeader(resp.Header)
//	for _, it := range r.Sources() { ... }   // 允许的来源
//	for _, it := range r.Reports() { ... }   // 上报端点
package csp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Kind 区分线索类型。
type Kind string

const (
	// KindSource 是策略里允许的来源（connect-src 等）。
	KindSource Kind = "source"
	// KindReport 是违规 / 网络错误的上报端点。
	KindReport Kind = "report"
)

// Item 是一条线索。
type Item struct {
	Kind Kind
	// Directive 是它来自哪条指令：connect-src / report-uri / report-to …
	Directive string
	// Value 是原始 token，保留通配符与端口，便于人工核对。
	Value string
	// Host 是归一后的主机名（通配符去掉 "*."）。
	Host string
	// Port 是显式端口，没有则为空。
	Port string
	// Path 是来源里的路径前缀（少见，CSP 允许 "https://a.com/api/"）。
	Path string
	// Wildcard 表示原值是 *.example.com 这种通配。
	Wildcard bool
	// SchemeOnly 表示原值只有 scheme（https:）或 "*"，无法定位主机。
	SchemeOnly bool
	// Group 用于 Report-To 的分组名。
	Group string
}

// Directive 是一条策略指令。
type Directive struct {
	Name   string
	Values []string
}

// Policy 是一条解析后的策略。
type Policy struct {
	// Header 是来源头名，CSP 或 CSP-Report-Only。
	Header     string
	ReportOnly bool
	Directives []Directive
}

// Result 是一次解析的汇总。
type Result struct {
	Policies []Policy
	Items    []Item
}

// ParseHeader 从响应头解析全部相关字段。
func ParseHeader(h http.Header) Result {
	if h == nil {
		return Result{}
	}
	return Parse(
		h.Values("Content-Security-Policy"),
		h.Values("Content-Security-Policy-Report-Only"),
		h.Values("Report-To"),
		h.Values("Reporting-Endpoints"),
	)
}

// Parse 从原始头值解析，便于在测试或非 net/http 场景复用。
func Parse(policies, reportOnly, reportTo, reporting []string) Result {
	var res Result
	for _, v := range policies {
		res.Policies = append(res.Policies, parsePolicy(v, "Content-Security-Policy", false))
	}
	for _, v := range reportOnly {
		res.Policies = append(res.Policies, parsePolicy(v, "Content-Security-Policy-Report-Only", true))
	}

	seen := map[string]struct{}{}
	add := func(it Item) {
		// 站内相对路径的上报端点没有主机，但有 Path，必须保留：
		// report-uri 写成 "/csp-report" 的情况很常见。
		if it.Host == "" && !it.SchemeOnly && !(it.Kind == KindReport && it.Path != "") {
			return
		}
		key := string(it.Kind) + "|" + it.Directive + "|" + it.Value + "|" + it.Group
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		res.Items = append(res.Items, it)
	}

	for _, p := range res.Policies {
		for _, d := range p.Directives {
			switch {
			case d.Name == "report-uri":
				// 已废弃但仍常见，值就是上报地址（可空格分隔多个）
				for _, tok := range d.Values {
					if it, ok := parseReportURL("report-uri", tok, ""); ok {
						add(it)
					}
				}
			case sourceListDirectives[d.Name]:
				// sandbox / report-to / upgrade-insecure-requests 等不是源列表，直接跳过。
				// report-to 的指令值只是分组名，真实地址在 Report-To 头里。
				for _, tok := range d.Values {
					if it, ok := parseSource(d.Name, tok); ok {
						add(it)
					}
				}
			}
		}
	}

	for _, v := range reportTo {
		for _, it := range parseReportTo(v) {
			add(it)
		}
	}
	for _, v := range reporting {
		for _, it := range parseReportingEndpoints(v) {
			add(it)
		}
	}
	return res
}

// Sources 返回策略里允许的来源。
func (r Result) Sources() []Item { return r.filter(KindSource) }

// Reports 返回上报端点。
func (r Result) Reports() []Item { return r.filter(KindReport) }

func (r Result) filter(k Kind) []Item {
	var out []Item
	for _, it := range r.Items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

// Hosts 返回全部线索（来源 + 上报）涉及的主机名，去重升序。
func (r Result) Hosts() []string { return hostsOf(r.Items) }

// SourceHosts 只返回「策略允许的来源」里的主机名，不含上报端点。
func (r Result) SourceHosts() []string { return hostsOf(r.Sources()) }

func hostsOf(items []Item) []string {
	set := map[string]struct{}{}
	for _, it := range items {
		if it.Host != "" {
			set[it.Host] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// URLs 把「可抓取」的来源转成 URL 列表。
//
// 只包含有主机、非通配、非 scheme-only 的项；相对路径的上报端点保持原样。
func (r Result) URLs() []string {
	var out []string
	seen := map[string]struct{}{}
	for _, it := range r.Items {
		if it.Host == "" || it.Wildcard {
			continue
		}
		scheme := "https"
		if strings.HasPrefix(it.Value, "http://") {
			scheme = "http"
		}
		u := scheme + "://" + it.Host
		if it.Port != "" {
			u += ":" + it.Port
		}
		if it.Path != "" {
			u += it.Path
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

// SameSite 过滤出与 host 同站或同子域的线索。
//
// 不引入 publicsuffix：只做后缀匹配，够用于「这个域名是不是本站的」这一判断。
func (r Result) SameSite(host string) []Item {
	base := stripPort(strings.ToLower(host))
	var out []Item
	for _, it := range r.Items {
		if sameOrSub(base, strings.ToLower(it.Host)) {
			out = append(out, it)
		}
	}
	return out
}

// ---------- 解析实现 ----------

// sourceListDirectives 是取值语义为「来源列表」的指令。
// 其余指令（sandbox、report-uri、upgrade-insecure-requests、trusted-types…）
// 的值不是地址，按来源解析会产生垃圾。
var sourceListDirectives = map[string]bool{
	"default-src": true, "script-src": true, "script-src-elem": true, "script-src-attr": true,
	"style-src": true, "style-src-elem": true, "style-src-attr": true,
	"img-src": true, "font-src": true, "connect-src": true, "media-src": true,
	"object-src": true, "frame-src": true, "child-src": true, "worker-src": true,
	"manifest-src": true, "prefetch-src": true, "form-action": true,
	"frame-ancestors": true, "base-uri": true, "navigate-to": true,
}

// 源列表里这些 token 不是地址，直接跳过。
var skipTokens = map[string]bool{
	"'self'": true, "'none'": true, "'unsafe-inline'": true, "'unsafe-eval'": true,
	"'strict-dynamic'": true, "'report-sample'": true, "'wasm-unsafe-eval'": true,
	"'unsafe-hashes'": true, "'inline-speculation-rules'": true,
}

func parsePolicy(raw, header string, reportOnly bool) Policy {
	p := Policy{Header: header, ReportOnly: reportOnly}
	for _, seg := range strings.Split(raw, ";") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		fields := strings.Fields(seg)
		if len(fields) == 0 {
			continue
		}
		name := strings.ToLower(fields[0])
		p.Directives = append(p.Directives, Directive{Name: name, Values: fields[1:]})
	}
	return p
}

// parseSource 解析一条来源 token。
func parseSource(directive, tok string) (Item, bool) {
	t := strings.TrimSpace(tok)
	if t == "" {
		return Item{}, false
	}
	low := strings.ToLower(t)
	if skipTokens[low] {
		return Item{}, false
	}
	if strings.HasPrefix(low, "'nonce-") || strings.HasPrefix(low, "'sha256-") ||
		strings.HasPrefix(low, "'sha384-") || strings.HasPrefix(low, "'sha512-") {
		return Item{}, false
	}

	it := Item{Kind: KindSource, Directive: directive, Value: t}

	switch low {
	case "*":
		it.SchemeOnly = true
		return it, true
	case "data:", "blob:", "filesystem:", "mediastream:", "about:":
		return Item{}, false // 无法定位主机的伪协议
	case "https:", "http:", "ws:", "wss:":
		it.SchemeOnly = true
		return it, true
	}

	// 通配主机：*.example.com
	if strings.HasPrefix(t, "*.") {
		it.Wildcard = true
		it.Host = strings.ToLower(strings.TrimPrefix(t, "*."))
		it.Host, it.Port = splitHostPort(it.Host)
		return it, it.Host != ""
	}

	// 带 scheme 的来源
	if strings.Contains(t, "://") {
		if u, err := url.Parse(t); err == nil && u.Hostname() != "" {
			it.Host = strings.ToLower(u.Hostname())
			it.Port = u.Port()
			it.Path = u.Path
			return it, true
		}
		return Item{}, false
	}

	// 裸主机（可带端口与路径），CSP 允许
	hostPart := t
	if i := strings.IndexAny(hostPart, "/"); i >= 0 {
		it.Path = hostPart[i:]
		hostPart = hostPart[:i]
	}
	host, port := splitHostPort(hostPart)
	if host == "" || strings.ContainsAny(host, " *") {
		return Item{}, false
	}
	it.Host = strings.ToLower(host)
	it.Port = port
	return it, true
}

// parseReportURL 解析单个上报地址。
func parseReportURL(directive, raw, group string) (Item, bool) {
	v := strings.Trim(strings.TrimSpace(raw), `"'`)
	if v == "" {
		return Item{}, false
	}
	it := Item{Kind: KindReport, Directive: directive, Value: v, Group: group}
	if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
		it.Host = strings.ToLower(u.Hostname())
		it.Port = u.Port()
		it.Path = u.Path
		if u.RawQuery != "" {
			it.Path += "?" + u.RawQuery
		}
		return it, true
	}
	// 站内相对路径（如 /csp-report）：保留，但没有主机
	if strings.HasPrefix(v, "/") {
		it.Path = v
		return it, true
	}
	return Item{}, false
}

// parseReportTo 解析 Report-To 头（可能是单个对象或数组）。
func parseReportTo(raw string) []Item {
	type endpoint struct {
		URL string `json:"url"`
	}
	type group struct {
		Group     string     `json:"group"`
		Endpoints []endpoint `json:"endpoints"`
	}

	var out []Item
	var one group
	if err := json.Unmarshal([]byte(raw), &one); err == nil && len(one.Endpoints) > 0 {
		for _, e := range one.Endpoints {
			if it, ok := parseReportURL("report-to", e.URL, one.Group); ok {
				out = append(out, it)
			}
		}
		return out
	}
	var many []group
	if err := json.Unmarshal([]byte(raw), &many); err == nil {
		for _, g := range many {
			for _, e := range g.Endpoints {
				if it, ok := parseReportURL("report-to", e.URL, g.Group); ok {
					out = append(out, it)
				}
			}
		}
	}
	return out
}

// parseReportingEndpoints 解析 `default="https://a", other="https://b"` 形式。
func parseReportingEndpoints(raw string) []Item {
	var out []Item
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if it, ok := parseReportURL("reporting-endpoints", val, strings.TrimSpace(name)); ok {
			out = append(out, it)
		}
	}
	return out
}

func splitHostPort(s string) (string, string) {
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i:], "]") {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func stripPort(s string) string {
	h, _ := splitHostPort(s)
	return h
}

// sameOrSub 判断 domain 是否等于 base 或为其子域。
func sameOrSub(base, domain string) bool {
	if base == "" || domain == "" {
		return false
	}
	if base == domain {
		return true
	}
	return strings.HasSuffix(domain, "."+base)
}
