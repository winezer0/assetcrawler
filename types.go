// Package assetcrawler 是一个零浏览器依赖的 Web URL 资产采集引擎。
//
// 本包只做编排，具体能力由三个子包提供，三者互不依赖：
//
//	fetcher    HTTP 抓取（连接池/限速/重试/重定向链/body 截断）
//	extractor  URL 提取，纯函数，只输出原始字符串，不碰 URL 解析
//	frontier   URL 归一化、范围判定、去重
//
// 数据流是单向的：
//
//	seed → frontier.Push → fetcher.Get → extractor.From* → frontier.Push → ...
//
// 这样拆的意义在于：想单独要「HTML 里所有 URL」的人可以只 import extractor，
// 完全不牵扯 HTTP 栈；想复用去重规则的人也只需要 frontier。
//
// 非目标（明确不做，避免范围蔓延）：
//   - 不执行 JavaScript、不渲染 DOM
//   - 不做漏洞检测、不做指纹识别
//   - 不做敏感信息提取（身份证/手机/邮箱/密钥等），这块交给 URLFinder 之类专用工具
//   - 不做目录重组 fuzz，不主动猜路径
//   - 不自动提交表单，只提取 action 与 method
package assetcrawler

import (
	"net/http"
	"regexp"
	"time"

	"assetcrawler/extractor"
	"assetcrawler/fetcher"
	"assetcrawler/frontier"
)

// Scope 是采集范围，等价于 frontier.Scope。
type Scope = frontier.Scope

const (
	// ScopeSameHost 仅同 host（含端口），最严格。
	ScopeSameHost = frontier.SameHost
	// ScopeSubdomain 同根域（含任意子域），默认。
	ScopeSubdomain = frontier.Subdomain
	// ScopeAny 不限制范围（仍会去重，但可能失控，慎用）。
	ScopeAny = frontier.Any
)

// Result 是一条采集结果。
//
// Source 标记该 URL 从何处被发现，取值如：
// "seed" / "a" / "script" / "form" / "header:location" / "js" / "robots" / "sitemap" / "redirect"。
// 这个字段对排查误报、评估提取质量很有用，不建议丢弃。
type Result struct {
	URL         string `json:"url"`
	Method      string `json:"method,omitempty"`
	Source      string `json:"source,omitempty"`
	Attribute   string `json:"attribute,omitempty"`
	Tag         string `json:"tag,omitempty"`
	Depth       int    `json:"depth"`
	StatusCode  int    `json:"status,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Referer     string `json:"referer,omitempty"`
	// Confidence 为空表示地址可直接访问；"template" 表示含 EXPR 占位符，
	// 这类结果只作为「接口形状」输出，不入队抓取（抓了必然 404）。
	Confidence string `json:"confidence,omitempty"`
	// Kind 标记资产类别："chunk"（打包分片）、"route"（前端路由），空为普通 URL。
	Kind string `json:"kind,omitempty"`
	// Params 是从上下文推断出的参数名（表单控件、JS 调用参数对象）。
	// 只有名字没有取值：真实取值要么在客户端生成，要么需要交互才能得到。
	Params []string `json:"params,omitempty"`
}

// Options 控制采集行为。请用 DefaultOptions() 起步，再按需覆盖字段。
type Options struct {
	// MaxDepth 最大爬取深度，种子页为 0。0 表示不限制（谨慎）。
	MaxDepth int
	// MaxURLs 最多抓取的页面数上限，防止失控。0 表示不限制。
	MaxURLs int
	// Concurrency 全局并发抓取数，默认 10。
	Concurrency int
	// PerHostConcurrency 单 host 并发上限，默认 4。对目标更友好，也降低被封概率。
	PerHostConcurrency int
	// RateLimit 全局 QPS 上限，0 表示不限速（仍受 Concurrency 约束）。
	RateLimit int
	// Timeout 单请求超时，默认 10s。
	Timeout time.Duration
	// MaxBodySize 单个响应体最大读取字节数，默认 2MB，超出部分截断（不报错）。
	MaxBodySize int64
	// Scope 爬取范围，默认 ScopeSubdomain。
	Scope Scope
	// FollowRobots 是否抓取并解析 robots.txt（其中 Sitemap 会被纳入，Disallow 仅作线索输出）。
	FollowRobots bool
	// FollowSitemap 是否抓取并解析 sitemap.xml（递归处理 sitemap index）。
	FollowSitemap bool
	// ExtractJS 是否对 .js / .css 及内联脚本做 endpoint 提取。
	ExtractJS bool
	// JSEngine 选择 JS 提取引擎，见 JSEngine 常量。零值等价于 JSEngineRegex。
	JSEngine JSEngine
	// ExtractFromStyle 是否从 style 属性与 <style> 块中提取 url()。
	ExtractFromStyle bool
	// ExtractJSON 是否从 JSON 响应体里提取地址（next/links 这类「下一跳」）。
	ExtractJSON bool
	// ExtractChunkGraph 是否从打包产物里恢复懒加载 chunk 与前端路由表。
	ExtractChunkGraph bool
	// RouteExec 是否允许「执行 chunk」来兜底恢复路由。
	// 默认关闭：它会运行目标站的代码，只在明确需要时开启。
	RouteExec bool
	// Insecure 跳过 TLS 证书校验。
	Insecure bool
	// Proxy 形如 http://127.0.0.1:8080 或 socks5://127.0.0.1:1080。空表示直连。
	Proxy string
	// UserAgent 自定义 UA，空则用默认值。
	UserAgent string
	// Headers 额外请求头（如 Cookie、Authorization），会被附加到每个请求。
	Headers http.Header
	// Retries 失败重试次数，默认 1。仅对传输层错误与 5xx 重试。
	Retries int
	// MaxRedirects 手动跟随重定向的最大跳数，默认 5。
	MaxRedirects int
	// ExcludeRegex 命中则丢弃该 URL（匹配完整 URL）。
	ExcludeRegex []*regexp.Regexp
	// IncludeRegex 非空时，仅保留命中的 URL。
	IncludeRegex []*regexp.Regexp
	// KnownFiles 除 robots/sitemap 外额外探测的固定路径，如 "/.git/config"。
	KnownFiles []string
	// OnResult 每抓取完一个 URL 回调一次。库内部已串行化，
	// 调用方无需自行加锁即可直接写文件或 channel。
	OnResult func(Result)
	// OnError 错误回调。与 OnResult 一样由库串行化调用。
	OnError func(url string, err error)
}

// DefaultOptions 返回一套对生产环境友好的默认值。
func DefaultOptions() *Options {
	return &Options{
		MaxDepth:           3,
		MaxURLs:            2000,
		Concurrency:        10,
		PerHostConcurrency: 4,
		Timeout:            10 * time.Second,
		MaxBodySize:        2 << 20,
		Scope:              ScopeSubdomain,
		FollowRobots:       true,
		FollowSitemap:      true,
		ExtractJS:          true,
		JSEngine:           JSEngineAuto,
		ExtractFromStyle:   true,
		ExtractJSON:        true,
		ExtractChunkGraph:  true,
		RouteExec:          false,
		Retries:            1,
		MaxRedirects:       5,
	}
}

// normalize 兜底非法值，避免调用方传 0/负数导致死锁或除零。
func (o *Options) normalize() {
	if o.Concurrency <= 0 {
		o.Concurrency = 10
	}
	if o.PerHostConcurrency <= 0 {
		o.PerHostConcurrency = 4
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxBodySize <= 0 {
		o.MaxBodySize = 2 << 20
	}
	if o.MaxRedirects <= 0 {
		o.MaxRedirects = 5
	}
	if o.MaxDepth < 0 {
		o.MaxDepth = 0
	}
}

// fetcherOptions 把编排层配置映射成抓取层配置。
// 各子包的默认值由子包自己兜底，这里只做字段搬运，不做二次判断。
func (o *Options) fetcherOptions() fetcher.Options {
	return fetcher.Options{
		Timeout:            o.Timeout,
		MaxBodySize:        o.MaxBodySize,
		MaxRedirects:       o.MaxRedirects,
		Retries:            o.Retries,
		Insecure:           o.Insecure,
		Proxy:              o.Proxy,
		UserAgent:          o.UserAgent,
		Headers:            o.Headers,
		PerHostConcurrency: o.PerHostConcurrency,
		QPS:                o.RateLimit,
	}
}

// frontierOptions 把编排层配置映射成范围/过滤配置。
func (o *Options) frontierOptions() frontier.Options {
	return frontier.Options{
		Scope:   o.Scope,
		Include: o.IncludeRegex,
		Exclude: o.ExcludeRegex,
	}
}

// extractorOptions 把编排层配置映射成提取配置。
func (o *Options) extractorOptions() extractor.Options {
	return extractor.Options{
		ExtractJS:    o.ExtractJS,
		ExtractStyle: o.ExtractFromStyle,
		JSExtract:    adaptEngine(engineChain(o.JSEngine)),
	}
}
