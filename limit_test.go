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

// TestMaxSeenReportsTruncation 验证上限截断会被上报，而不是静默少收。
//
// 静默截断的危害在于结果「看起来完整」：使用者只会觉得这个站东西少，
// 不会想到是自己设的上限。
func TestMaxSeenReportsTruncation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<a href="/a.html">a</a><a href="/b.html">b</a><a href="/c.html">c</a>
		</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var errs []string
	opts := DefaultOptions()
	opts.MaxDepth = 2
	opts.MaxSeen = 2
	opts.Concurrency = 2
	opts.PerHostConcurrency = 2
	opts.Timeout = 3 * time.Second
	opts.OnError = func(u string, err error) { errs = append(errs, err.Error()) }

	c, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := c.Crawl(ctx, srv.URL+"/"); err != nil {
		t.Fatalf("Crawl: %v", err)
	}

	found := false
	for _, e := range errs {
		if strings.Contains(e, "上限") {
			found = true
		}
	}
	if !found {
		t.Errorf("上限截断未上报，错误列表: %v", errs)
	}
}
