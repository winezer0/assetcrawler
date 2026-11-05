// Command assetcrawl 是 assetcrawl 包的参考命令行工具。
//
// 用法示例：
//
//	assetcrawl -u https://example.com -d 3 -o urls.txt
//	assetcrawl -u https://example.com -jsonl -o result.jsonl
//	assetcrawl -u https://example.com -scope same-host -H "Cookie: a=b"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"assetcrawler"
)

type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			*m = append(*m, p)
		}
	}
	return nil
}

func main() {
	var (
		urls      multiFlag
		headers   multiFlag
		known     multiFlag
		depth     = flag.Int("d", 3, "max crawl depth (0 = unlimited)")
		conc      = flag.Int("c", 10, "global concurrency")
		perHost   = flag.Int("phc", 4, "per-host concurrency")
		rateLimit = flag.Int("rl", 0, "global requests per second (0 = unlimited)")
		timeout   = flag.Int("timeout", 10, "per-request timeout in seconds")
		maxURLs   = flag.Int("max-urls", 2000, "max pages to fetch")
		maxBody   = flag.Int64("max-body", 2<<20, "max response body bytes")
		scope     = flag.String("scope", "subdomain", "scope: same-host | subdomain | any")
		proxy     = flag.String("proxy", "", "proxy URL, e.g. http://127.0.0.1:8080")
		output    = flag.String("o", "", "write results to file (default stdout)")
		asJSONL   = flag.Bool("jsonl", false, "output JSONL instead of plain URLs")
		insecure  = flag.Bool("k", false, "skip TLS verification")
		noRobots  = flag.Bool("no-robots", false, "do not read robots.txt")
		noSitemap = flag.Bool("no-sitemap", false, "do not read sitemap.xml")
		noJS      = flag.Bool("no-js", false, "disable JS/CSS endpoint extraction")
		silent    = flag.Bool("silent", false, "only print URLs, no summary")
		verbose   = flag.Bool("v", false, "print errors to stderr")
		ua        = flag.String("ua", "", "custom User-Agent")
		exclude   = flag.String("exclude", "", "exclude URLs matching this regex")
		include   = flag.String("include", "", "only keep URLs matching this regex")
	)

	flag.Var(&urls, "u", "target URL (repeatable or comma-separated)")
	flag.Var(&headers, "H", "extra header, e.g. -H 'Cookie: a=b' (repeatable)")
	flag.Var(&known, "known-file", "extra fixed path to probe, e.g. -known-file /.git/config")
	flag.Parse()

	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "usage: assetcrawl -u <url> [-d depth] [-o out.txt] [-jsonl]")
		flag.PrintDefaults()
		os.Exit(2)
	}

	opts := assetcrawler.DefaultOptions()
	opts.MaxDepth = *depth
	opts.Concurrency = *conc
	opts.PerHostConcurrency = *perHost
	opts.RateLimit = *rateLimit
	opts.MaxURLs = *maxURLs
	opts.MaxBodySize = *maxBody
	opts.Timeout = seconds(*timeout)
	opts.Proxy = *proxy
	opts.Insecure = *insecure
	opts.FollowRobots = !*noRobots
	opts.FollowSitemap = !*noSitemap
	opts.ExtractJS = !*noJS
	opts.KnownFiles = known
	if *ua != "" {
		opts.UserAgent = *ua
	}
	switch *scope {
	case "same-host":
		opts.Scope = assetcrawler.ScopeSameHost
	case "any":
		opts.Scope = assetcrawler.ScopeAny
	default:
		opts.Scope = assetcrawler.ScopeSubdomain
	}
	if *exclude != "" {
		re, err := regexp.Compile(*exclude)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid -exclude regex: %v\n", err)
			os.Exit(2)
		}
		opts.ExcludeRegex = append(opts.ExcludeRegex, re)
	}
	if *include != "" {
		re, err := regexp.Compile(*include)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid -include regex: %v\n", err)
			os.Exit(2)
		}
		opts.IncludeRegex = append(opts.IncludeRegex, re)
	}
	for _, h := range headers {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			continue
		}
		if opts.Headers == nil {
			opts.Headers = make(map[string][]string)
		}
		k = strings.TrimSpace(k)
		opts.Headers[k] = append(opts.Headers[k], strings.TrimSpace(v))
	}

	// 输出
	var (
		outWriter = os.Stdout
		file      *os.File
	)
	if *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot create output file: %v\n", err)
			os.Exit(1)
		}
		file = f
		outWriter = f
		defer file.Close()
	}

	var (
		wmu   sync.Mutex
		count int
		uniq  = make(map[string]struct{}, 1024)
	)
	opts.OnResult = func(r assetcrawler.Result) {
		// StatusCode 为 0 表示这类结果是推测出来的线索（如 robots 里的路径），不可访问
		wmu.Lock()
		defer wmu.Unlock()
		if _, dup := uniq[r.URL]; dup && !*asJSONL {
			return
		}
		uniq[r.URL] = struct{}{}
		count++
		if *asJSONL {
			b, err := json.Marshal(r)
			if err == nil {
				fmt.Fprintln(outWriter, string(b))
			}
			return
		}
		fmt.Fprintln(outWriter, r.URL)
	}
	if *verbose {
		opts.OnError = func(u string, err error) {
			fmt.Fprintf(os.Stderr, "[err] %s: %v\n", u, err)
		}
	}

	crawler, err := assetcrawler.New(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
		os.Exit(1)
	}
	defer crawler.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	results, err := crawler.Crawl(ctx, urls...)
	if err != nil && err != context.Canceled {
		fmt.Fprintf(os.Stderr, "crawl error: %v\n", err)
	}

	if file != nil {
		file.Sync()
	}
	if !*silent {
		fmt.Fprintf(os.Stderr, "\n[done] %d URLs collected\n", len(results))
	}
}

// seconds 把秒数转成 time.Duration，供 flag 使用。
func seconds(n int) time.Duration {
	if n <= 0 {
		return 10 * time.Second
	}
	return time.Duration(n) * time.Second
}
