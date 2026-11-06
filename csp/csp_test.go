package csp_test

import (
	"net/http"
	"os"
	"strings"
	"testing"

	"assetcrawler/csp"
)

// loadHeader 把 testdata 里抓下来的响应头转成 http.Header。
// 文件格式就是 `curl -sIL` 的输出行：`Name: value`。
func loadHeader(t *testing.T, path string) http.Header {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	h := http.Header{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		name, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		h.Add(strings.TrimSpace(name), strings.TrimSpace(val))
	}
	return h
}

func hostsOf(items []csp.Item) map[string]bool {
	m := map[string]bool{}
	for _, it := range items {
		if it.Host != "" {
			m[it.Host] = true
		}
	}
	return m
}

// TestMDN 用真实头验证：CSP 声明的域名能被提取，且各类非地址 token 被过滤。
func TestMDN(t *testing.T) {
	res := csp.ParseHeader(loadHeader(t, "testdata/mdn.txt"))
	if len(res.Policies) == 0 {
		t.Fatal("未解析到任何策略")
	}

	hosts := hostsOf(res.Sources())
	// 这些域名在 MDN 首页 HTML 里都搜不到，只能从 CSP 拿到
	for _, want := range []string{
		"observatory-api.mdn.mozilla.net",
		"bcd.developer.mozilla.org",
		"incoming.telemetry.mozilla.org",
	} {
		if !hosts[want] {
			t.Errorf("缺少 %s，实际: %v", want, res.Hosts())
		}
	}

	// 非地址 token 不应出现
	for _, it := range res.Items {
		for _, bad := range []string{"'self'", "'none'", "sha256-", "sha384-", "nonce-", "data:", "'report-sample'"} {
			if strings.Contains(it.Value, bad) {
				t.Errorf("非地址 token 被当作线索: %q (directive=%s)", it.Value, it.Directive)
			}
		}
		if it.Host == "" && !it.SchemeOnly {
			t.Errorf("既无主机也不是 scheme-only: %+v", it)
		}
	}
}

// TestCloudflareReportTo 验证 Report-To 的 JSON 解析（NEL 上报端点）。
func TestCloudflareReportTo(t *testing.T) {
	res := csp.ParseHeader(loadHeader(t, "testdata/cloudflare.txt"))

	var found bool
	for _, it := range res.Reports() {
		if it.Host == "a.nel.cloudflare.com" {
			found = true
			if it.Directive != "report-to" {
				t.Errorf("directive = %q，期望 report-to", it.Directive)
			}
			if it.Group != "cf-nel" {
				t.Errorf("group = %q，期望 cf-nel", it.Group)
			}
			if !strings.HasPrefix(it.Path, "/report/v4") {
				t.Errorf("path = %q，期望以 /report/v4 开头", it.Path)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 NEL 上报端点，实际: %+v", res.Reports())
	}

	// cloudflare 的 CSP 里有大量第三方域名，必须能拿到
	if len(res.Hosts()) < 10 {
		t.Errorf("主机数 = %d，偏少，可能漏了解析", len(res.Hosts()))
	}
}

// TestVuejsFrameAncestors 验证只有 frame-ancestors 的站点也能提取。
func TestVuejsFrameAncestors(t *testing.T) {
	res := csp.ParseHeader(loadHeader(t, "testdata/vuejs.txt"))
	hosts := hostsOf(res.Sources())
	for _, want := range []string{"certification.vuejs.org", "certificates.dev"} {
		if !hosts[want] {
			t.Errorf("缺少 %s，实际: %v", want, res.Hosts())
		}
	}
	// SameSite 应保留子域（certification.vuejs.org）、排除外站
	got := hostsOf(res.SameSite("vuejs.org"))
	if !got["certification.vuejs.org"] {
		t.Errorf("子域应保留: %v", got)
	}
	for _, bad := range []string{"certificates.dev", "alemira.com"} {
		if got[bad] {
			t.Errorf("外站不应保留: %s (%v)", bad, got)
		}
	}
}

// TestEdgeCases 覆盖人工构造的边界写法。
func TestEdgeCases(t *testing.T) {
	const policy = "default-src 'self' 'nonce-abc123' 'sha256-XNBp89FG76amD8BqrJzyflxOF9PaWPqPqvJfKZPCv7M=';" +
		"connect-src 'self' https://api.example.com https://api.example.com:8443/path/ *.wild.example.com https: data:;" +
		"img-src cdn.example.com;" +
		"report-uri /csp-report https://report.example.com/r;" +
		"sandbox allow-scripts;" +
		"upgrade-insecure-requests"

	res := csp.Parse([]string{policy}, nil, nil, nil)

	got := map[string]csp.Item{}
	for _, it := range res.Items {
		got[it.Value] = it
	}

	if it, ok := got["https://api.example.com"]; !ok || it.Host != "api.example.com" {
		t.Errorf("api.example.com 解析错误: %+v", it)
	}
	if it, ok := got["https://api.example.com:8443/path/"]; !ok || it.Port != "8443" || it.Path != "/path/" {
		t.Errorf("带端口的来源解析错误: %+v", it)
	}
	if it, ok := got["*.wild.example.com"]; !ok || !it.Wildcard || it.Host != "wild.example.com" {
		t.Errorf("通配来源解析错误: %+v", it)
	}
	if it, ok := got["https:"]; !ok || !it.SchemeOnly {
		t.Errorf("scheme-only 来源解析错误: %+v", it)
	}
	if it, ok := got["cdn.example.com"]; !ok || it.Host != "cdn.example.com" {
		t.Errorf("裸主机解析错误: %+v", it)
	}

	// 相对路径的上报端点要保留
	var hasRelative bool
	for _, it := range res.Reports() {
		if it.Value == "/csp-report" {
			hasRelative = true
			if it.Kind != csp.KindReport {
				t.Errorf("上报端点 Kind = %q", it.Kind)
			}
		}
	}
	if !hasRelative {
		t.Errorf("相对路径的 report-uri 丢失: %+v", res.Reports())
	}

	// data: / 'self' / nonce / hash / sandbox / upgrade-insecure-requests 都不该产出
	for _, bad := range []string{"data:", "'self'", "allow-scripts", "'nonce-abc123'"} {
		if _, ok := got[bad]; ok {
			t.Errorf("不应产出 %q", bad)
		}
	}
	for _, it := range res.Items {
		if strings.Contains(it.Value, "sha256-") {
			t.Errorf("hash 源被当成地址: %q", it.Value)
		}
	}
}

// TestReportingEndpoints 验证 Reporting-Endpoints 的键值对形式。
func TestReportingEndpoints(t *testing.T) {
	res := csp.Parse(nil, nil, nil, []string{`default="https://a.example.com/rep", other="https://b.example.com/x"`})
	hosts := hostsOf(res.Reports())
	for _, want := range []string{"a.example.com", "b.example.com"} {
		if !hosts[want] {
			t.Errorf("缺少 %s，实际: %v", want, hosts)
		}
	}
}

// TestReportOnly 验证 Report-Only 头被标记。
func TestReportOnly(t *testing.T) {
	res := csp.Parse(nil, []string{"default-src https://ro.example.com"}, nil, nil)
	if len(res.Policies) != 1 || !res.Policies[0].ReportOnly {
		t.Fatalf("Report-Only 未标记: %+v", res.Policies)
	}
	if !hostsOf(res.Sources())["ro.example.com"] {
		t.Error("Report-Only 里的来源丢失")
	}
}

// TestSameSite 验证同站/子域判定。
func TestSameSite(t *testing.T) {
	res := csp.Parse([]string{
		"default-src https://api.example.com https://example.com https://evil-example.com https://other.com",
	}, nil, nil, nil)

	got := hostsOf(res.SameSite("example.com"))
	if !got["api.example.com"] || !got["example.com"] {
		t.Errorf("同站/子域应保留: %v", got)
	}
	if got["evil-example.com"] || got["other.com"] {
		t.Errorf("外站不应保留: %v", got)
	}
}

// TestURLs 验证可抓取 URL 的构造：跳过通配与 scheme-only，默认补 https。
func TestURLs(t *testing.T) {
	res := csp.Parse([]string{
		"connect-src https://a.example.com http://b.example.com:8080/p *.c.example.com https:",
	}, nil, nil, nil)

	urls := res.URLs()
	set := map[string]bool{}
	for _, u := range urls {
		set[u] = true
	}
	if !set["https://a.example.com"] {
		t.Errorf("缺少 https://a.example.com，实际 %v", urls)
	}
	if !set["http://b.example.com:8080/p"] {
		t.Errorf("缺少带端口与路径的 URL，实际 %v", urls)
	}
	for _, u := range urls {
		if strings.Contains(u, "*") || strings.HasSuffix(u, "//") {
			t.Errorf("不该产出通配或空主机 URL: %q", u)
		}
	}
}

// TestNoHeader 验证没有相关头时安全返回空结果。
func TestNoHeader(t *testing.T) {
	if res := csp.ParseHeader(nil); len(res.Items) != 0 {
		t.Errorf("nil 头应返回空结果")
	}
	if res := csp.ParseHeader(http.Header{"Server": {"nginx"}}); len(res.Items) != 0 {
		t.Errorf("无关头应返回空结果")
	}
}
