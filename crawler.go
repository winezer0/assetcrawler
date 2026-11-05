package assetcrawler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"golang.org/x/net/html"

	"assetcrawler/extractor"
	"assetcrawler/fetcher"
	"assetcrawler/frontier"
)

// Crawler 是采集引擎。零值不可用，请用 New 构造。
//
// 它自身不实现抓取、解析、去重中的任何一项，只负责把这三者串起来，
// 因此状态很少：配置、两个协作者、一个结果缓冲区。
type Crawler struct {
	opts     *Options
	fetcher  *fetcher.Fetcher
	frontier *frontier.Frontier

	mu      sync.Mutex
	emitMu  sync.Mutex
	fetched int
	results []Result
}

// task 是一个待抓取项。来源字段会一路带到结果里，
// 便于事后区分「这条 URL 是从 a 标签来的，还是从 JS 里抠出来的」。
type task struct {
	url       *url.URL
	depth     int
	referer   string
	source    string
	attribute string
	tag       string
	method    string
}

// New 创建采集器。
func New(opts *Options) (*Crawler, error) {
	if opts == nil {
		opts = DefaultOptions()
	}
	opts.normalize()

	f, err := fetcher.New(opts.fetcherOptions())
	if err != nil {
		return nil, err
	}
	return &Crawler{
		opts:     opts,
		fetcher:  f,
		frontier: frontier.New(opts.frontierOptions()),
	}, nil
}

// Close 释放连接池与后台协程。
func (c *Crawler) Close() error {
	if c.fetcher != nil {
		c.fetcher.Close()
	}
	return nil
}

// Crawl 从给定种子开始采集，返回全部命中结果。
// 若设置了 Options.OnResult，会在抓取过程中流式回调，返回值用于收尾统计。
func (c *Crawler) Crawl(ctx context.Context, seeds ...string) ([]Result, error) {
	if len(seeds) == 0 {
		return nil, fmt.Errorf("no seed URL provided")
	}
	if c.fetcher == nil {
		return nil, fmt.Errorf("crawler closed")
	}

	current, err := c.seedTasks(seeds)
	if err != nil {
		return nil, err
	}

	// robots.txt / sitemap.xml / 已知路径 作为 depth 0 的补充来源
	current = append(current, c.bootstrap(ctx, current)...)

	for depth := 0; len(current) > 0; depth++ {
		if ctx.Err() != nil {
			break
		}
		if c.opts.MaxDepth > 0 && depth > c.opts.MaxDepth {
			break
		}
		current = c.runLevel(ctx, current)
	}

	c.mu.Lock()
	out := make([]Result, len(c.results))
	copy(out, c.results)
	c.mu.Unlock()
	return out, ctx.Err()
}

// seedTasks 规范化种子、登记范围基准，返回第一层任务。
func (c *Crawler) seedTasks(seeds []string) ([]task, error) {
	var (
		out []task
		bad []string
	)
	for _, s := range seeds {
		u, ok := frontier.Normalize(s, nil)
		if !ok {
			bad = append(bad, s)
			continue
		}
		c.frontier.AddSeed(u)
		if nu, added := c.frontier.Push(u.String(), nil); added {
			out = append(out, task{url: nu, depth: 0, source: "seed", method: "GET"})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid http(s) seed URL in %v", bad)
	}
	return out, nil
}

// runLevel 并发抓取一层，返回下一层任务。
//
// 并发收敛方式：worker 从 jobs 取任务，结果通过互斥锁汇总到 next。
// 达到 MaxURLs 时 cancel 掉本层 context，派发循环随之退出，
// 不依赖 goroutine 被强行终止——所有 worker 都靠 channel 关闭或 ctx 结束自然返回。
func (c *Crawler) runLevel(ctx context.Context, tasks []task) []task {
	workers := c.opts.Concurrency
	if workers > len(tasks) {
		workers = len(tasks)
	}
	if workers <= 0 {
		return nil
	}

	levelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		jobs = make(chan task)
		mu   sync.Mutex
		next []task
		wg   sync.WaitGroup
	)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if levelCtx.Err() != nil {
					return
				}
				if !c.reserveFetch() {
					cancel() // 达到 MaxURLs，停止本层继续派发
					return
				}
				found := c.processOne(levelCtx, t)
				if len(found) == 0 {
					continue
				}
				mu.Lock()
				next = append(next, found...)
				mu.Unlock()
			}
		}()
	}

	for _, t := range tasks {
		select {
		case jobs <- t:
		case <-levelCtx.Done():
			close(jobs)
			wg.Wait()
			return next
		}
	}
	close(jobs)
	wg.Wait()
	return next
}

// processOne 抓取并解析单个 URL，输出一条结果并返回新发现的下层任务。
func (c *Crawler) processOne(ctx context.Context, t task) []task {
	res, err := c.fetcher.Get(ctx, t.url, t.referer)
	if err != nil && !isCancellation(err) {
		c.emitError(t.url.String(), err)
	}
	if res == nil {
		return nil
	}

	c.emit(c.resultOf(t, res))

	nextDepth := t.depth + 1
	if c.opts.MaxDepth > 0 && nextDepth > c.opts.MaxDepth {
		return nil
	}
	reqs, base := c.extractAll(res)
	return c.toTasks(reqs, base, nextDepth, res.URL.String())
}

// extractAll 从一次响应里穷尽式提取 URL 候选，并返回相对路径的解析基准。
// 提取本身是纯函数，这里只负责「选择用哪个提取函数」以及 base 基准的确定。
func (c *Crawler) extractAll(res *fetcher.Response) ([]extractor.Request, *url.URL) {
	var out []extractor.Request
	base := res.URL

	// 跳转链上的每一跳都是资产
	for _, loc := range res.Redirects {
		out = append(out, extractor.Request{Raw: loc, Method: "GET", Source: "redirect"})
	}

	out = append(out, extractor.FromHeaders(res.Header)...)

	switch {
	case extractor.LooksLikeHTML(res.ContentType, res.Body):
		doc, err := html.Parse(bytes.NewReader(res.Body))
		if err != nil {
			c.emitError(res.URL.String(), err)
			return out, base
		}
		out = append(out, extractor.FromHTML(doc, c.opts.extractorOptions())...)
		// <base href> 会改变相对路径的解析基准；是否采纳由编排层决定，
		// extractor 只负责把值读出来。
		if bh := extractor.BaseHref(doc); bh != "" {
			if bu, ok := frontier.Normalize(bh, res.URL); ok {
				base = bu
			}
		}
	case extractor.LooksLikeCSS(res.ContentType, res.URL.Path):
		if c.opts.ExtractFromStyle {
			out = append(out, extractor.FromCSS(string(res.Body))...)
		}
	case c.opts.ExtractJS && extractor.LooksLikeJS(res.ContentType, res.URL.Path):
		out = append(out, extractor.FromJS(string(res.Body), c.opts.extractorOptions())...)
	}

	return out, base
}

// toTasks 把提取结果送入 frontier 归一化/去重，转换为下一层任务。
func (c *Crawler) toTasks(reqs []extractor.Request, base *url.URL, depth int, referer string) []task {
	var next []task
	for _, r := range reqs {
		u, ok := c.frontier.Push(r.Raw, base)
		if !ok {
			continue
		}
		next = append(next, task{
			url:       u,
			depth:     depth,
			referer:   referer,
			source:    r.Source,
			attribute: r.Attribute,
			tag:       r.Tag,
			method:    r.Method,
		})
	}
	return next
}

// resultOf 把抓取响应与任务上下文组装成对外结果。
func (c *Crawler) resultOf(t task, res *fetcher.Response) Result {
	src := t.source
	if src == "" {
		src = "page"
	}
	method := t.method
	if method == "" {
		method = "GET"
	}
	return Result{
		URL:         res.URL.String(),
		Method:      method,
		Source:      src,
		Attribute:   t.attribute,
		Tag:         t.tag,
		Depth:       t.depth,
		StatusCode:  res.StatusCode,
		ContentType: res.ContentType,
		Referer:     t.referer,
	}
}

// emit 回调并收集结果。
//
// 回调通过独立的 emitMu 串行化：抓取是并发的，但调用方不需要为了
// 往文件/channel 写结果而自己加锁。
func (c *Crawler) emit(r Result) {
	c.emitMu.Lock()
	defer c.emitMu.Unlock()

	if c.opts.OnResult != nil {
		c.opts.OnResult(r)
	}
	c.mu.Lock()
	c.results = append(c.results, r)
	c.mu.Unlock()
}

// emitError 串行化地派发错误回调，避免调用方需要自己加锁。
func (c *Crawler) emitError(url string, err error) {
	if c.opts.OnError == nil || err == nil {
		return
	}
	c.emitMu.Lock()
	defer c.emitMu.Unlock()
	c.opts.OnError(url, err)
}

// reserveFetch 预留一次抓取配额。
func (c *Crawler) reserveFetch() bool {
	if c.opts.MaxURLs <= 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetched >= c.opts.MaxURLs {
		return false
	}
	c.fetched++
	return true
}

// isCancellation 判断错误是否只是上下文取消，这类错误不应当作采集失败上报。
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
