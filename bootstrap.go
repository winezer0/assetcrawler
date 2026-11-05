package assetcrawler

import (
	"context"
	"net/url"
	"strings"

	"assetcrawler/extractor"
	"assetcrawler/frontier"
)

// bootstrap 在正式爬取前处理 robots.txt / sitemap.xml / KnownFiles，
// 把它们提供的入口作为 depth 0 任务返回。
//
// 这三者是资产收集里性价比最高的一步：很多站点的主导航是 JS 渲染的，
// 爬虫顺着 a[href] 走不了几页，但 sitemap.xml 会直接给出全站页面清单。
func (c *Crawler) bootstrap(ctx context.Context, seeds []task) []task {
	if !c.opts.FollowRobots && !c.opts.FollowSitemap && len(c.opts.KnownFiles) == 0 {
		return nil
	}

	// 按 origin 去重，多个种子同域时只探一次
	origins := make(map[string]*url.URL)
	var order []string
	for _, s := range seeds {
		o := &url.URL{Scheme: s.url.Scheme, Host: s.url.Host}
		k := o.String()
		if _, ok := origins[k]; !ok {
			origins[k] = o
			order = append(order, k)
		}
	}

	var out []task
	add := func(raw, source string) {
		u, added := c.frontier.Push(raw, nil)
		if !added {
			return
		}
		out = append(out, task{url: u, depth: 0, source: source, method: "GET"})
	}

	for _, k := range order {
		if ctx.Err() != nil {
			break
		}
		origin := origins[k]

		var sitemaps []string
		if c.opts.FollowRobots {
			sitemaps = c.readRobots(ctx, origin)
		}
		if c.opts.FollowSitemap {
			if len(sitemaps) == 0 {
				sitemaps = []string{origin.String() + "/sitemap.xml"}
			}
			seen := make(map[string]struct{})
			for _, sm := range sitemaps {
				c.walkSitemap(ctx, sm, 0, seen, func(raw string) { add(raw, "sitemap") })
			}
		}
		for _, kf := range c.opts.KnownFiles {
			kf = strings.TrimSpace(kf)
			if kf == "" {
				continue
			}
			add(origin.String()+"/"+strings.TrimPrefix(kf, "/"), "known-file")
		}
	}
	return out
}

// readRobots 读取 robots.txt，返回其中声明的 sitemap 地址。
// Disallow/Allow 路径仅作为线索输出，不进入抓取队列（见 extractor.ParseRobots 的说明）。
func (c *Crawler) readRobots(ctx context.Context, origin *url.URL) []string {
	u, ok := frontier.Normalize(origin.String()+"/robots.txt", nil)
	if !ok {
		return nil
	}
	res, err := c.fetcher.Get(ctx, u, "")
	if res == nil || res.StatusCode != 200 || len(res.Body) == 0 {
		if err != nil && !isCancellation(err) {
			c.emitError(u.String(), err)
		}
		return nil
	}

	info := extractor.ParseRobots(res.Body)

	for _, p := range dedupStrings(info.Disallow) {
		// 通配符与正则式路径无法直接还原成可访问地址，跳过
		if strings.ContainsAny(p, "*$") {
			continue
		}
		if clue, ok := frontier.Normalize(origin.String()+"/"+strings.TrimPrefix(p, "/"), nil); ok {
			c.emit(Result{
				URL:    clue.String(),
				Method: "GET",
				Source: "robots",
				Depth:  0,
			})
		}
	}
	return resolveAgainst(u, dedupStrings(info.Sitemaps))
}

// walkSitemap 递归解析 sitemap（含 sitemap index），把页面地址交给 add。
func (c *Crawler) walkSitemap(ctx context.Context, raw string, depth int, seen map[string]struct{}, add func(string)) {
	if depth > 3 || ctx.Err() != nil {
		return
	}
	u, ok := frontier.Normalize(raw, nil)
	if !ok {
		return
	}
	k := u.String()
	if _, dup := seen[k]; dup {
		return
	}
	seen[k] = struct{}{}

	res, err := c.fetcher.Get(ctx, u, "")
	if res == nil || res.StatusCode != 200 || len(res.Body) == 0 {
		if err != nil && !isCancellation(err) {
			c.emitError(u.String(), err)
		}
		return
	}

	urls, sitemaps := extractor.ParseSitemap(res.Body)
	for _, p := range urls {
		// sitemap 规范要求 loc 是绝对地址，但现实中存在写相对路径的站点，
		// 这里用 sitemap 自身的地址做基准兜一层。
		if ru, ok := frontier.Normalize(p, u); ok {
			add(ru.String())
		}
	}
	for _, sm := range sitemaps {
		c.walkSitemap(ctx, sm, depth+1, seen, add)
	}
}

// resolveAgainst 把可能是相对路径的地址按基准 URL 展开，丢弃无法解析的项。
func resolveAgainst(base *url.URL, in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if u, ok := frontier.Normalize(s, base); ok {
			out = append(out, u.String())
		}
	}
	return out
}

// dedupStrings 保序去重。
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
