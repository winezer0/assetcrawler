package assetcrawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCrawlEndToEnd 用一个本地站点验证完整链路：
// 种子 → 页面链接 → JS 里的接口 → robots 线索 → sitemap 入口，以及去重与回调一致性。
func TestCrawlEndToEnd(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<a href="/a.html">a</a>
			<a href="/b.html">b</a>
			<script src="/app.js"></script>
		</body></html>`)
	})
	mux.HandleFunc("/a.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/c.html">c</a></body></html>`)
	})
	mux.HandleFunc("/b.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>no links</body></html>`)
	})
	mux.HandleFunc("/c.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><a href="/a.html">already seen</a></body></html>`)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `var api="/api/hidden/endpoint";`)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "User-agent: *\nDisallow: /secret\nSitemap: /sitemap.xml\n")
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><urlset><url><loc>/from-sitemap.html</loc></url></urlset>`)
	})
	mux.HandleFunc("/from-sitemap.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>sitemap page</body></html>")
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := DefaultOptions()
	opts.MaxDepth = 2
	opts.Concurrency = 4
	opts.Timeout = 5 * time.Second

	var collected []Result
	opts.OnResult = func(r Result) { collected = append(collected, r) }

	c, err := New(opts)
	if err != nil {
		t.Fatalf("new crawler: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	results, err := c.Crawl(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}

	got := make(map[string]Result)
	for _, r := range results {
		got[r.URL] = r
	}

	expect := []string{
		srv.URL + "/",
		srv.URL + "/a.html",
		srv.URL + "/b.html",
		srv.URL + "/c.html",
		srv.URL + "/app.js",
		srv.URL + "/api/hidden/endpoint",
		srv.URL + "/from-sitemap.html",
		srv.URL + "/secret", // robots 线索，仅记录不抓取
	}
	for _, e := range expect {
		if _, ok := got[e]; !ok {
			t.Errorf("missing %s", e)
		}
	}

	// robots 线索必须标记为 StatusCode 0（表示未实际访问）
	if r, ok := got[srv.URL+"/secret"]; ok {
		if r.Source != "robots" || r.StatusCode != 0 {
			t.Errorf("robots clue = %+v, want Source=robots Status=0", r)
		}
	}
	// sitemap 入口的来源应被保留
	if r, ok := got[srv.URL+"/from-sitemap.html"]; ok && r.Source != "sitemap" {
		t.Errorf("sitemap entry source = %q, want sitemap", r.Source)
	}
	// 抓取过的页面必须有状态码
	if r, ok := got[srv.URL+"/a.html"]; ok && r.StatusCode != 200 {
		t.Errorf("/a.html status = %d, want 200", r.StatusCode)
	}
	// 去重：a.html 被 c.html 再次引用，不应重复出现
	n := 0
	for _, r := range results {
		if r.URL == srv.URL+"/a.html" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("/a.html appeared %d times, want 1", n)
	}
	// 回调与返回值条数应一致
	if len(collected) != len(results) {
		t.Errorf("OnResult called %d times, results %d", len(collected), len(results))
	}
}

// TestMaxURLs 验证配额生效：抓取数被限制，且不因取消而报错。
func TestMaxURLs(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><body>
			<a href="/1">1</a><a href="/2">2</a><a href="/3">3</a>
			<a href="/4">4</a><a href="/5">5</a><a href="/6">6</a>
		</body></html>`)
			return
		}
		fmt.Fprint(w, "<html><body>page</body></html>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := DefaultOptions()
	opts.MaxURLs = 3
	opts.MaxDepth = 1
	opts.FollowRobots = false
	opts.FollowSitemap = false
	opts.Concurrency = 2

	var errCount int
	opts.OnError = func(url string, err error) { errCount++ }

	c, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	results, err := c.Crawl(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("crawl returned error: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("fetched %d pages, want <= 3", len(results))
	}
	if errCount != 0 {
		t.Errorf("quota exhaustion should not report errors, got %d", errCount)
	}
}

// TestScopeAlias 确保对外暴露的 Scope 常量可直接用于 Options。
func TestScopeAlias(t *testing.T) {
	for _, s := range []Scope{ScopeSameHost, ScopeSubdomain, ScopeAny} {
		opts := DefaultOptions()
		opts.Scope = s
		c, err := New(opts)
		if err != nil {
			t.Fatalf("New with scope %v: %v", s, err)
		}
		c.Close()
	}
	if ScopeSameHost.String() != "same-host" {
		t.Errorf("Scope String() = %q", ScopeSameHost.String())
	}
}
