package assetcrawler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// TestChunkGraphEndToEnd 验证打包产物的 chunk 与路由恢复能贯通到结果里。
//
// 场景还原真实情况：HTML 只有一个入口 bundle，懒加载 chunk 和前端路由
// 都不在任何静态链接上，只能从 bundle 的运行时清单里读出来。
func TestChunkGraphEndToEnd(t *testing.T) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><div id="app"></div>
			<script src="/static/js/app.js"></script></body></html>`)
	})

	bundle := `(function(){
	  var o={}; o.p="/static/js/";
	  o.u=function(e){return "chunk-"+e+"."+{101:"a1b2c3",102:"d4e5f6"}[e]+".js"};
	  var routes=[
	    {path:"/admin/reports",name:"reports",component:function(){return o.e(101)}},
	    {path:"/settings/profile",name:"profile",component:function(){return o.e(102)}},
	    {path:"/user/:id",name:"user",component:function(){return o.e(102)}}
	  ];
	  window.__routes=routes;
	})();`

	mux.HandleFunc("/static/js/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, bundle)
	})
	mux.HandleFunc("/static/js/chunk-101.a1b2c3.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `console.log("chunk 101");`)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := DefaultOptions()
	opts.MaxDepth = 2
	opts.RateLimit = 0
	opts.JSEngine = JSEngineGoja

	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results, err := c.Crawl(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	byPath := map[string]Result{}
	for _, r := range results {
		byPath[r.URL] = r
	}
	base := srv.URL

	// 懒加载 chunk 必须被发现并抓取
	chunkURL := base + "/static/js/chunk-101.a1b2c3.js"
	r, ok := byPath[chunkURL]
	if !ok {
		t.Fatalf("未发现 chunk，实际: %v", keysOfResult(byPath))
	}
	if r.Kind != "chunk" {
		t.Errorf("chunk 的 kind = %q，期望 chunk", r.Kind)
	}
	if r.StatusCode != 200 {
		t.Errorf("chunk 状态码 = %d，期望 200", r.StatusCode)
	}

	// 带参数占位的路由只报告形状，不发请求
	paramURL := base + "/user/:id"
	pr, ok := byPath[paramURL]
	if !ok {
		t.Errorf("未发现参数路由 %s，实际: %v", paramURL, keysOfResult(byPath))
	} else {
		if pr.Kind != "route" {
			t.Errorf("参数路由 kind = %q，期望 route", pr.Kind)
		}
		if pr.StatusCode != 0 {
			t.Errorf("参数路由不应被抓取，状态码 = %d，期望 0", pr.StatusCode)
		}
	}

	// 前端路由必须被识别并打标记
	for _, want := range []string{"/admin/reports", "/settings/profile"} {
		u := base + want
		rr, ok := byPath[u]
		if !ok {
			t.Errorf("未发现路由 %s，实际: %v", want, keysOfResult(byPath))
			continue
		}
		if rr.Kind != "route" {
			t.Errorf("路由 %s 的 kind = %q，期望 route", want, rr.Kind)
		}
	}
}

func keysOfResult(m map[string]Result) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestSitemapDoesNotStarveAssets 锁定「sitemap 不能吃光配额」。
//
// 实测问题：vuejs.org 的 sitemap 有 59 条，以 depth 0 与种子页同层入队，
// -max-urls 60 时配额被 sitemap 全部占满，种子页里的 <script src> 一条都没抓到，
// 结果里 JS 数为 0，看起来像「这个站没有 JS」。
//
// 现在 sitemap 页面放 depth 1，且第 0 层结束后才并入，排在种子页提取出的资源之后。
func TestSitemapDoesNotStarveAssets(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><script src="/app.js"></script></body></html>`)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `fetch("/api/from-js");`)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Sitemap: /sitemap.xml\n")
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0"?><urlset>`)
		for i := 0; i < 40; i++ {
			fmt.Fprintf(&sb, `<url><loc>/page-%d</loc></url>`, i)
		}
		sb.WriteString(`</urlset>`)
		fmt.Fprint(w, sb.String())
	})
	for i := 0; i < 40; i++ {
		p := fmt.Sprintf("/page-%d", i)
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>page</body></html>")
		})
	}

	srv := httptest.NewServer(mux)
	defer srv.Close()

	opts := DefaultOptions()
	opts.MaxDepth = 2
	opts.MaxURLs = 16 // 远小于 sitemap 条目数（40）
	opts.Concurrency = 2
	opts.PerHostConcurrency = 2
	opts.RateLimit = 0

	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	results, err := c.Crawl(ctx, srv.URL+"/")
	if err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	byURL := map[string]Result{}
	for _, r := range results {
		byURL[r.URL] = r
	}

	// 核心断言：脚本必须被抓到，且它是 JS 里发现的接口的跳板
	if r, ok := byURL[srv.URL+"/app.js"]; !ok {
		t.Fatalf("配额被 sitemap 吃光，/app.js 未被抓到；实际 %v", keysOfResult(byURL))
	} else if r.StatusCode != 200 {
		t.Errorf("/app.js 状态码 = %d，期望 200", r.StatusCode)
	}
	// 说明：这里不断言 js 内发现的 /api/from-js——
	// 它在 depth 2，而配额 16 在 depth 1（40 条 sitemap）就已用尽。
	// 本用例只验证「资源优先于 sitemap」，js 内提取由 TestCrawlEndToEnd 覆盖。

	// sitemap 页面应当落在 depth 1
	for u, r := range byURL {
		if strings.Contains(u, "/page-") && r.Depth != 1 {
			t.Errorf("sitemap 页面 %s 的 depth = %d，期望 1", u, r.Depth)
		}
	}
}
