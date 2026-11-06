// Command assetcrawl 是 assetcrawl 包的参考命令行工具。
//
// 用法示例：
//
//	assetcrawl -u https://example.com -d 3 -o urls.txt
//	assetcrawl -u https://example.com -jsonl -o result.jsonl
//	assetcrawl -u https://example.com -scope same-host -H "Cookie: a=b"
//
// 参数解析与 Options 的映射在 flags.go，本文件只负责跑起来并把结果打出去。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"assetcrawler"
)

func main() {
	cfg, err := parseArgs(os.Args[1:], os.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "%v\n", err)
		fmt.Fprintln(os.Stderr, "usage: assetcrawl -u <url> [-d depth] [-o out.txt] [-jsonl]")
		os.Exit(2)
	}

	opts := cfg.Opts

	outWriter := os.Stdout
	var file *os.File
	if cfg.OutputPath != "" {
		f, err := os.Create(cfg.OutputPath)
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
		wmu.Lock()
		defer wmu.Unlock()
		// 纯 URL 输出要去重；JSONL 保留每条结果的来源差异
		if _, dup := uniq[r.URL]; dup && !cfg.JSONL {
			return
		}
		uniq[r.URL] = struct{}{}
		count++
		if cfg.JSONL {
			b, err := json.Marshal(r)
			if err == nil {
				fmt.Fprintln(outWriter, string(b))
			}
			return
		}
		fmt.Fprintln(outWriter, r.URL)
	}
	if cfg.Verbose {
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

	results, err := crawler.Crawl(ctx, cfg.URLs...)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "crawl error: %v\n", err)
	}

	if file != nil {
		file.Sync()
	}
	if !cfg.Silent {
		fmt.Fprintf(os.Stderr, "\n[done] %d URLs collected\n", len(results))
	}
}
