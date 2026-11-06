package fetcher_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"assetcrawler/fetcher"
)

// newFetcher 用给定配置建一个抓取器，测试结束自动关闭。
func newFetcher(t *testing.T, o fetcher.Options) *fetcher.Fetcher {
	t.Helper()
	f, err := fetcher.New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.Close)
	return f
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("parse %s: %v", s, err)
	}
	return u
}

// TestRedirectChain 验证多跳重定向：最终 URL 正确，且每一跳都被记录下来。
//
// 跳转目标本身是资产（例如 http→https、www 跳转、登录跳转），
// 漏记就会少一批 URL。
func TestRedirectChain(t *testing.T) {
	var mux http.ServeMux
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b", http.StatusFound)
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/c", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/c", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "final")
	})
	srv := httptest.NewServer(&mux)
	defer srv.Close()

	f := newFetcher(t, fetcher.Options{MaxRedirects: 5})
	res, err := f.Get(context.Background(), mustURL(t, srv.URL+"/a"), "")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, want := res.URL.Path, "/c"; got != want {
		t.Errorf("最终路径 = %q，期望 %q", got, want)
	}
	if got := string(res.Body); got != "final" {
		t.Errorf("body = %q，期望 final", got)
	}
	if len(res.Redirects) != 2 {
		t.Fatalf("跳转链 = %v，期望 2 跳", res.Redirects)
	}
	if !strings.HasSuffix(res.Redirects[0], "/b") || !strings.HasSuffix(res.Redirects[1], "/c") {
		t.Errorf("跳转链内容不对: %v", res.Redirects)
	}
}

// TestRedirectLimit 验证超过上限时不再跟随，也不会死循环。
//
// 这里只断言「停下来 + 记录已发生的跳转」，不要求返回错误：
// 调用方仍然能从 Redirects 里拿到资产线索。
func TestRedirectLimit(t *testing.T) {
	var mux http.ServeMux
	for i := 0; i < 10; i++ {
		next := fmt.Sprintf("/r%d", i+1)
		mux.HandleFunc(fmt.Sprintf("/r%d", i), func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, next, http.StatusFound)
		})
	}
	srv := httptest.NewServer(&mux)
	defer srv.Close()

	f := newFetcher(t, fetcher.Options{MaxRedirects: 1})
	done := make(chan struct{})
	var res *fetcher.Response
	var err error
	go func() {
		res, err = f.Get(context.Background(), mustURL(t, srv.URL+"/r0"), "")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("重定向上限未生效，疑似死循环")
	}
	if err != nil {
		t.Fatalf("Get 不应返回错误: %v", err)
	}
	if len(res.Redirects) > 2 {
		t.Errorf("MaxRedirects=1 却跟随了 %d 跳: %v", len(res.Redirects), res.Redirects)
	}
}

// TestRetryOn5xx 验证 5xx 会重试、4xx 不会。
func TestRetryOn5xx(t *testing.T) {
	var hits int32
	var mux http.ServeMux
	mux.HandleFunc("/flaky", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "ok")
	})
	srv := httptest.NewServer(&mux)
	defer srv.Close()

	f := newFetcher(t, fetcher.Options{Retries: 2})
	res, err := f.Get(context.Background(), mustURL(t, srv.URL+"/flaky"), "")
	if err != nil {
		t.Fatalf("重试后应当成功: %v", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("状态码 = %d，期望 200", res.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("实际请求 %d 次，期望 3 次（1 次原始 + 2 次重试）", got)
	}

	// 4xx 不该重试。
	// 注意 404 是「成功的抓取」——状态码本身就是要记录的证据，
	// 所以 Get 不返回错误，由调用方按 StatusCode 判断。
	var notFoundHits int32
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&notFoundHits, 1)
		w.WriteHeader(http.StatusNotFound)
	})
	res, err = f.Get(context.Background(), mustURL(t, srv.URL+"/missing"), "")
	if err != nil {
		t.Fatalf("404 不应返回错误（状态码是有效证据），实际: %v", err)
	}
	if res.StatusCode != 404 {
		t.Errorf("状态码 = %d，期望 404", res.StatusCode)
	}
	if got := atomic.LoadInt32(&notFoundHits); got != 1 {
		t.Errorf("404 被请求了 %d 次，期望 1 次（不应重试）", got)
	}
}

// TestRateLimit 验证 QPS 限速：桶容量为 QPS，超出部分按 1/QPS 秒排队。
func TestRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	const qps = 5
	const total = 10
	f := newFetcher(t, fetcher.Options{QPS: qps})

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Get(context.Background(), mustURL(t, srv.URL+"/"), ""); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	// 前 5 个立即拿到令牌，其余 5 个每 1/5 秒放行一个 → 至少约 1s。
	// 留 20% 余量，避免计时抖动导致偶发失败。
	lower := time.Duration(float64(time.Second) * 0.8)
	if elapsed < lower {
		t.Errorf("%d 个请求在 QPS=%d 下只用了 %v，期望不少于 %v", total, qps, elapsed, lower)
	}
	// 同时不能慢得离谱（对端是本地服务）
	if elapsed > 3*time.Second {
		t.Errorf("耗时 %v 过长，限速实现可能有问题", elapsed)
	}
}

// TestPerHostConcurrency 验证同一 host 的并发上限。
func TestPerHostConcurrency(t *testing.T) {
	var cur, peak int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			p := atomic.LoadInt32(&peak)
			if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	const limit = 2
	f := newFetcher(t, fetcher.Options{PerHostConcurrency: limit})

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Get(context.Background(), mustURL(t, srv.URL+"/"), ""); err != nil {
				t.Errorf("Get: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&peak); got > limit {
		t.Errorf("同 host 峰值并发 = %d，超过上限 %d", got, limit)
	}
}

// TestBodyTruncation 验证 body 超过上限时被截断且不报错。
//
// 默认上限 2MB，大 bundle 一定会在函数中途断开；
// 截断本身不是错误，但必须真的截断，否则内存会被大响应撑爆。
func TestBodyTruncation(t *testing.T) {
	const limit = 100
	big := strings.Repeat("x", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, big)
	}))
	defer srv.Close()

	f := newFetcher(t, fetcher.Options{MaxBodySize: limit})
	res, err := f.Get(context.Background(), mustURL(t, srv.URL+"/"), "")
	if err != nil {
		t.Fatalf("截断不应报错: %v", err)
	}
	if len(res.Body) != limit {
		t.Errorf("body 长度 = %d，期望 %d", len(res.Body), limit)
	}
}

// TestContextCancel 验证取消能中断在途请求（用于 MaxURLs 到顶时的收敛）。
func TestContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
		fmt.Fprint(w, "late")
	}))
	defer srv.Close()

	f := newFetcher(t, fetcher.Options{Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := f.Get(ctx, mustURL(t, srv.URL+"/"), "")
	if err == nil {
		t.Fatal("应当因 context 取消而返回错误")
	}
	if time.Since(start) > time.Second {
		t.Errorf("取消未及时生效，耗时 %v", time.Since(start))
	}
}
