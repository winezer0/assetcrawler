package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"assetcrawler"
)

// cliConfig 是命令行解析的完整结果：采集选项 + 输出控制。
//
// 之所以把解析从 main 里拆出来，是因为「flag 名字与 Options 字段的映射」
// 是最容易写错、又最没人测的一层：改一个默认值、把 -no-js 写成取反、
// 新增一个开关忘了接到 Options 上，所有这些只有跑起来才看得见。
// 拆成纯函数后可以用表驱动用例逐个锁定。
type cliConfig struct {
	// URLs 目标地址，来自可重复的 -u，逗号分隔也会被拆开。
	URLs []string
	// Opts 与命令行参数一一对应。
	Opts *assetcrawler.Options
	// OutputPath 输出文件路径，空表示 stdout。
	OutputPath string
	// JSONL 是否按 JSONL 输出。
	JSONL bool
	// Silent 是否只输出 URL、不打汇总。
	Silent bool
	// Verbose 是否把错误打到 stderr。
	Verbose bool
}

// errNoTarget 表示没有给出任何 -u。
var errNoTarget = errors.New("至少需要一个 -u 目标")

// multiFlag 是可重复、可逗号分隔的字符串参数。
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

// parseArgs 解析命令行参数并组装 Options。
//
// 只做解析与映射，不碰网络、不写文件，因此可以直接被测试调用。
func parseArgs(args []string, errOut io.Writer) (*cliConfig, error) {
	fs := flag.NewFlagSet("assetcrawl", flag.ContinueOnError)
	fs.SetOutput(errOut)

	var (
		urls        multiFlag
		headers     multiFlag
		known       multiFlag
		depth       = fs.Int("d", 3, "max crawl depth (0 = unlimited)")
		conc        = fs.Int("c", 10, "global concurrency")
		perHost     = fs.Int("phc", 4, "per-host concurrency")
		rateLimit   = fs.Int("rl", 0, "global requests per second (0 = unlimited)")
		timeout     = fs.Int("timeout", 10, "per-request timeout in seconds")
		maxURLs     = fs.Int("max-urls", 2000, "max pages to fetch")
		maxBody     = fs.Int64("max-body", 2<<20, "max response body bytes")
		scope       = fs.String("scope", "subdomain", "scope: same-host | subdomain | any")
		proxy       = fs.String("proxy", "", "proxy URL, e.g. http://127.0.0.1:8080")
		output      = fs.String("o", "", "write results to file (default stdout)")
		asJSONL     = fs.Bool("jsonl", false, "output JSONL instead of plain URLs")
		insecure    = fs.Bool("k", false, "skip TLS verification")
		noRobots    = fs.Bool("no-robots", false, "do not read robots.txt")
		noSitemap   = fs.Bool("no-sitemap", false, "do not read sitemap.xml")
		noJS        = fs.Bool("no-js", false, "disable JS/CSS endpoint extraction")
		jsEngine    = fs.String("js-engine", "auto", "JS extraction engine: auto | regex | goja | treesitter")
		noChunk     = fs.Bool("no-chunk-graph", false, "disable chunk/route graph recovery from bundles")
		routeExec   = fs.Bool("route-exec", false, "allow executing webpack chunks to recover routes (runs target site JS; off by default)")
		routeExpand = fs.Bool("route-expand", false, "expand :param routes with placeholders and fetch them (capped by -route-expand-max)")
		routeMax    = fs.Int("route-expand-max", 50, "max routes to expand when -route-expand is on")
		maxSeen     = fs.Int("max-seen", 200000, "cap on the in-memory seen set (0 = unlimited)")
		noCSP       = fs.Bool("no-csp", false, "do not report CSP allow-list hosts and report endpoints")
		silent      = fs.Bool("silent", false, "only print URLs, no summary")
		verbose     = fs.Bool("v", false, "print errors to stderr")
		ua          = fs.String("ua", "", "custom User-Agent")
		exclude     = fs.String("exclude", "", "exclude URLs matching this regex")
		include     = fs.String("include", "", "only keep URLs matching this regex")
	)

	fs.Var(&urls, "u", "target URL (repeatable or comma-separated)")
	fs.Var(&headers, "H", "extra header, e.g. -H 'Cookie: a=b' (repeatable)")
	fs.Var(&known, "known-file", "extra fixed path to probe, e.g. -known-file /.git/config")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if len(urls) == 0 {
		return nil, errNoTarget
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
	opts.JSEngine = assetcrawler.JSEngine(*jsEngine)
	opts.ExtractChunkGraph = !*noChunk
	opts.RouteExec = *routeExec
	opts.RouteExpand = *routeExpand
	opts.MaxRouteExpand = *routeMax
	opts.MaxSeen = *maxSeen
	opts.CSPHints = !*noCSP
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
			return nil, fmt.Errorf("invalid -exclude regex: %w", err)
		}
		opts.ExcludeRegex = append(opts.ExcludeRegex, re)
	}
	if *include != "" {
		re, err := regexp.Compile(*include)
		if err != nil {
			return nil, fmt.Errorf("invalid -include regex: %w", err)
		}
		opts.IncludeRegex = append(opts.IncludeRegex, re)
	}

	applyHeaders(opts, headers)

	return &cliConfig{
		URLs:       urls,
		Opts:       opts,
		OutputPath: *output,
		JSONL:      *asJSONL,
		Silent:     *silent,
		Verbose:    *verbose,
	}, nil
}

// applyHeaders 把 "K: V" 形式的 -H 合并进 Options.Headers。
// 没有冒号的条目直接忽略：它不可能是合法头，报错反而打断批量脚本。
func applyHeaders(opts *assetcrawler.Options, headers []string) {
	for _, h := range headers {
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if opts.Headers == nil {
			opts.Headers = make(map[string][]string)
		}
		opts.Headers[k] = append(opts.Headers[k], strings.TrimSpace(v))
	}
}

// seconds 把秒数转成 time.Duration，供 flag 使用。
func seconds(n int) time.Duration {
	if n <= 0 {
		return 10 * time.Second
	}
	return time.Duration(n) * time.Second
}
