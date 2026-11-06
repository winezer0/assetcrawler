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

func TestExpandRoutePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		changed bool
	}{
		{"/user/:id", "/user/1", true},
		{"/user/:id/posts/:postId", "/user/1/posts/1", true},
		{"/files/*rest", "/files/index", true},
		{"/:lang(.*)", "", false}, // 正则约束参数无法安全实例化
		{"/plain/path", "", false},
		{"/", "", false},
	}
	for _, c := range cases {
		got, changed := expandRoutePath(c.in)
		if changed != c.changed || got != c.want {
			t.Errorf("expandRoutePath(%q) = (%q, %v)，期望 (%q, %v)", c.in, got, changed, c.want, c.changed)
		}
	}
}

// TestRouteExpandIntegration 验证开关的两种状态：
// 关着时只报告路由形状，打开后用占位值真抓一次。
func TestRouteExpandIntegration(t *testing.T) {
	var hits []string
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			// 记录被真正访问的路径
			hits = append(hits, r.URL.Path)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<html><body><script>var api="/api/p/%s";</script></body></html>`, strings.Trim(r.URL.Path, "/"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><script src="/app.js"></script></body></html>`)
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `const routes=[{path:"/user/:id",component:X}];
const c="/static/js/chunk-"+e+"."+m[e]+".js";`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	run := func(expand bool) (routeURLs, fetched []string) {
		hits = nil
		opts := DefaultOptions()
		opts.MaxDepth = 3
		opts.MaxURLs = 100
		opts.RouteExpand = expand
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
		for _, r := range results {
			if r.Kind == "route" {
				routeURLs = append(routeURLs, r.URL)
			}
		}
		return routeURLs, hits
	}

	routes, fetched := run(false)
	if len(routes) == 0 {
		t.Fatal("未恢复出任何路由")
	}
	for _, h := range fetched {
		if strings.Contains(h, "/user/") {
			t.Errorf("开关关闭时不该抓取路由地址，实际访问了 %s", h)
		}
	}

	routes, fetched = run(true)
	if len(routes) == 0 {
		t.Fatal("未恢复出任何路由")
	}
	expanded := false
	for _, h := range fetched {
		if h == "/user/1" {
			expanded = true
		}
	}
	if !expanded {
		t.Errorf("开关打开后未展开抓取 /user/1，实际访问 %v", fetched)
	}
}
