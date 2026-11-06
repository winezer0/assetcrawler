package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"assetcrawler"
)

// TestParseArgsDefaults 锁定「不给任何可选参数时的映射」。
//
// 默认值最容易在重构里被悄悄改掉：改一个数字不会有任何测试变红，
// 但会改变所有使用者的行为。
func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"-u", "https://example.com/"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(cfg.URLs) != 1 || cfg.URLs[0] != "https://example.com/" {
		t.Errorf("URLs = %v", cfg.URLs)
	}

	o := cfg.Opts
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MaxDepth", o.MaxDepth, 3},
		{"Concurrency", o.Concurrency, 10},
		{"PerHostConcurrency", o.PerHostConcurrency, 4},
		{"RateLimit", o.RateLimit, 0},
		{"MaxURLs", o.MaxURLs, 2000},
		{"MaxBodySize", o.MaxBodySize, int64(2 << 20)},
		{"Timeout", o.Timeout, 10 * time.Second},
		{"Scope", o.Scope, assetcrawler.ScopeSubdomain},
		{"FollowRobots", o.FollowRobots, true},
		{"FollowSitemap", o.FollowSitemap, true},
		{"ExtractJS", o.ExtractJS, true},
		{"JSEngine", o.JSEngine, assetcrawler.JSEngineAuto},
		{"ExtractChunkGraph", o.ExtractChunkGraph, true},
		{"RouteExec", o.RouteExec, false},
		{"RouteExpand", o.RouteExpand, false},
		{"MaxRouteExpand", o.MaxRouteExpand, 50},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v", c.name, c.got, c.want)
		}
	}
}

// TestParseArgsMapping 逐个 flag 验证「名字 → 字段」的接线没有错位。
func TestParseArgsMapping(t *testing.T) {
	args := []string{
		"-u", "https://a.example/", "-u", "https://b.example/",
		"-d", "5", "-c", "20", "-phc", "8", "-rl", "50", "-timeout", "3",
		"-max-urls", "100", "-max-body", "1024",
		"-scope", "same-host", "-proxy", "http://127.0.0.1:8080", "-k",
		"-no-robots", "-no-sitemap", "-no-js", "-js-engine", "treesitter",
		"-no-chunk-graph", "-route-exec", "-route-expand", "-route-expand-max", "7",
		"-ua", "probe/1.0", "-o", "out.txt", "-jsonl", "-silent", "-v",
	}
	cfg, err := parseArgs(args, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(cfg.URLs) != 2 {
		t.Errorf("URLs = %v，期望 2 条", cfg.URLs)
	}
	if cfg.OutputPath != "out.txt" || !cfg.JSONL || !cfg.Silent || !cfg.Verbose {
		t.Errorf("输出控制未接线：%+v", cfg)
	}

	o := cfg.Opts
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MaxDepth", o.MaxDepth, 5},
		{"Concurrency", o.Concurrency, 20},
		{"PerHostConcurrency", o.PerHostConcurrency, 8},
		{"RateLimit", o.RateLimit, 50},
		{"MaxURLs", o.MaxURLs, 100},
		{"MaxBodySize", o.MaxBodySize, int64(1024)},
		{"Timeout", o.Timeout, 3 * time.Second},
		{"Scope", o.Scope, assetcrawler.ScopeSameHost},
		{"Proxy", o.Proxy, "http://127.0.0.1:8080"},
		{"Insecure", o.Insecure, true},
		{"FollowRobots", o.FollowRobots, false},
		{"FollowSitemap", o.FollowSitemap, false},
		{"ExtractJS", o.ExtractJS, false},
		{"JSEngine", o.JSEngine, assetcrawler.JSEngineTreesitter},
		{"ExtractChunkGraph", o.ExtractChunkGraph, false},
		{"RouteExec", o.RouteExec, true},
		{"RouteExpand", o.RouteExpand, true},
		{"MaxRouteExpand", o.MaxRouteExpand, 7},
		{"UserAgent", o.UserAgent, "probe/1.0"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v，期望 %v", c.name, c.got, c.want)
		}
	}
}

// TestParseArgsScope 覆盖 scope 三档：非法值回落到默认的 subdomain 而不是报错，
// 因为扫描脚本里偶尔会传入旧版本的值，中断整个批处理不划算。
func TestParseArgsScope(t *testing.T) {
	cases := map[string]assetcrawler.Scope{
		"same-host": assetcrawler.ScopeSameHost,
		"subdomain": assetcrawler.ScopeSubdomain,
		"any":       assetcrawler.ScopeAny,
		"bogus":     assetcrawler.ScopeSubdomain,
	}
	for in, want := range cases {
		cfg, err := parseArgs([]string{"-u", "https://x/", "-scope", in}, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("scope=%s: %v", in, err)
		}
		if cfg.Opts.Scope != want {
			t.Errorf("scope=%s 映射到 %v，期望 %v", in, cfg.Opts.Scope, want)
		}
	}
}

// TestParseArgsHeaders 覆盖 -H 的两种写法与两类脏数据。
func TestParseArgsHeaders(t *testing.T) {
	cfg, err := parseArgs([]string{
		"-u", "https://x/",
		"-H", "Cookie: a=b",
		"-H", "X-Token: t1",
		"-H", "X-Token: t2",
		"-H", "no-colon-here",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	h := cfg.Opts.Headers
	if got := h["Cookie"]; len(got) != 1 || got[0] != "a=b" {
		t.Errorf("Cookie = %v", got)
	}
	if got := h["X-Token"]; len(got) != 2 || got[0] != "t1" || got[1] != "t2" {
		t.Errorf("X-Token = %v，重复头应累加", got)
	}
	if _, ok := h["no-colon-here"]; ok {
		t.Errorf("无冒号的 -H 应被忽略，实际写入 %v", h)
	}
}

// TestParseArgsErrors 覆盖三类失败：没有目标、正则写错。
func TestParseArgsErrors(t *testing.T) {
	if _, err := parseArgs(nil, &bytes.Buffer{}); err == nil {
		t.Error("没有 -u 时应报错")
	}
	if _, err := parseArgs([]string{"-u", "https://x/", "-exclude", "("}, &bytes.Buffer{}); err == nil {
		t.Error("-exclude 非法正则应报错")
	}
	if _, err := parseArgs([]string{"-u", "https://x/", "-include", "["}, &bytes.Buffer{}); err == nil {
		t.Error("-include 非法正则应报错")
	}
}

// TestMultiFlagComma 验证逗号分隔与重复使用可以混用。
func TestMultiFlagComma(t *testing.T) {
	cfg, err := parseArgs([]string{"-u", "https://a/,https://b/", "-u", "https://c/"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if len(cfg.URLs) != 3 {
		t.Errorf("URLs = %v，期望 3 条", cfg.URLs)
	}
	for _, u := range cfg.URLs {
		if strings.TrimSpace(u) == "" {
			t.Errorf("URLs 里出现空条目：%v", cfg.URLs)
		}
	}
}
