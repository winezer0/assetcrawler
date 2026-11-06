package assetcrawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// TestCSPHints 锁定 CSP 线索的取舍口径。
//
// 关键在于「哪些该收、哪些必须排除」，每一条都是实测踩出来的：
// frame-ancestors 列的是外部父页面，通配主机无法定位，
// 同 host 的 source 只是重复当前页面本身。
func TestCSPHints(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Security-Policy",
		"default-src 'self'; "+
			"connect-src https://api.example.com https://*.wild.com 'self'; "+
			"script-src https://cdn.example.com; "+
			"img-src https: data:; "+
			"frame-ancestors https://parent.example.org; "+
			"report-uri /csp-report")

	base, _ := url.Parse("https://example.com/page")
	got := cspHints(h, base)

	gotURL := map[string]string{}
	for _, x := range got {
		gotURL[x.URL.String()] = x.Kind
	}

	want := map[string]string{
		"https://api.example.com":        KindCSPSource,
		"https://cdn.example.com":        KindCSPSource,
		"https://example.com/csp-report": KindCSPReport,
	}
	for u, kind := range want {
		if gotURL[u] != kind {
			t.Errorf("缺少 %s（期望 kind=%s），实际 %v", u, kind, gotURL)
		}
	}

	// 必须排除的三类
	for _, bad := range []string{
		"https://parent.example.org", // frame-ancestors 是入站语义
		"https://wild.com",           // 通配无法定位
		"https://*.wild.com",
		"https://example.com", // 同 host，等于当前页面
	} {
		if _, ok := gotURL[bad]; ok {
			t.Errorf("不该收 %s", bad)
		}
	}
}

// TestCSPHintsNotFetched 验证线索只报告、不进抓取队列。
//
// 这条是设计契约：CSP 的 source 多是域名根，抓了 80% 是 404；
// report 端点带一次性 token，抓取等于向目标注入数据。
func TestCSPHintsNotFetched(t *testing.T) {
	// 先起一个「CSP 里列出的外部域名」，它一旦被访问就说明线索进了抓取队列
	var apiHits int32
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&apiHits, 1)
	}))
	defer ext.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Security-Policy", "connect-src "+ext.URL+"; report-uri /csp-report")
		fmt.Fprint(w, `<html><body>ok</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := DefaultOptions()
	opts.MaxDepth = 1
	opts.Concurrency = 2
	opts.PerHostConcurrency = 2
	opts.Timeout = 3 * time.Second

	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results, err := c.Crawl(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	var hints []Result
	for _, r := range results {
		if r.Kind == KindCSPSource || r.Kind == KindCSPReport {
			hints = append(hints, r)
		}
	}
	if len(hints) == 0 {
		t.Fatalf("没有产出 CSP 线索，结果 %d 条", len(results))
	}
	for _, h := range hints {
		if h.StatusCode != 0 {
			t.Errorf("%s 被访问了（status=%d），线索不应入队", h.URL, h.StatusCode)
		}
	}
	if n := atomic.LoadInt32(&apiHits); n != 0 {
		t.Errorf("CSP 列出的外部域名被访问了 %d 次", n)
	}
}
