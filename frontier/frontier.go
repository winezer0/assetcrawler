// Package frontier 负责 URL 的规范化、范围判定与去重。
//
// 它是本工程唯一「认识 URL 语义」的地方：extractor 只吐原始字符串，
// fetcher 只认已经解析好的 *url.URL，两者都不碰归一化规则。
// 归一化规则集中在这里，好处是改规则只有一处、也只需要测一处。
//
// 该包不依赖本工程的其他包，可独立使用（例如只想要 URL 去重的场景）。
package frontier

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/net/publicsuffix"
)

// Scope 定义采集范围。
type Scope int

const (
	// SameHost 仅同 host（含端口），最严格。
	SameHost Scope = iota
	// Subdomain 同根域（含任意子域），默认。
	Subdomain
	// Any 不限制范围（仍会去重，但可能失控，慎用）。
	Any
)

func (s Scope) String() string {
	switch s {
	case SameHost:
		return "same-host"
	case Subdomain:
		return "subdomain"
	default:
		return "any"
	}
}

// Options 控制范围与过滤规则。
type Options struct {
	Scope Scope
	// Include 非空时，仅保留命中的 URL。
	Include []*regexp.Regexp
	// Exclude 命中则丢弃该 URL。
	Exclude []*regexp.Regexp
	// MaxSeen 限制已见集合的条数，0 表示不限制。
	//
	// 这个集合是常驻内存的：10 万 URL 约几十 MB，且永远不会缩小。
	// 超大站点上它会成为唯一没有上限的增长点，所以给一个硬顶，
	// 超出后拒绝新 URL 并计数，由调用方决定是否提示。
	MaxSeen int
}

// Frontier 维护种子基准与已见集合，须并发安全。
type Frontier struct {
	opts    Options
	seeds   []*url.URL
	seen    map[string]struct{}
	dropped int
	mu      sync.Mutex
}

// New 创建 Frontier。
func New(opts Options) *Frontier {
	return &Frontier{
		opts: opts,
		seen: make(map[string]struct{}, 1024),
	}
}

// AddSeed 登记一个种子，作为后续 Scope 判定的基准。可多次调用以支持多目标。
func (f *Frontier) AddSeed(u *url.URL) {
	if u == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.seeds {
		if strings.EqualFold(s.Host, u.Host) {
			return
		}
	}
	f.seeds = append(f.seeds, u)
}

// Push 把原始地址（可为相对路径）归一化、判定范围、去重。
// base 为解析相对路径的基准，可为 nil（此时 raw 必须是绝对地址）。
// 返回 true 表示这是一个此前没见过且允许采集的新 URL。
func (f *Frontier) Push(raw string, base *url.URL) (*url.URL, bool) {
	u, ok := Normalize(raw, base)
	if !ok {
		return nil, false
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.inScopeLocked(u) || !f.passFiltersLocked(u.String()) {
		return nil, false
	}
	key := u.String()
	if _, dup := f.seen[key]; dup {
		return nil, false
	}
	if f.opts.MaxSeen > 0 && len(f.seen) >= f.opts.MaxSeen {
		f.dropped++
		return nil, false
	}
	f.seen[key] = struct{}{}
	return u, true
}

// MarkSeen 仅登记不返回，用于「已经处理过、无需再进队列」的场景。
func (f *Frontier) MarkSeen(u *url.URL) bool {
	if u == nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := u.String()
	if _, dup := f.seen[key]; dup {
		return false
	}
	f.seen[key] = struct{}{}
	return true
}

// Dropped 返回因 MaxSeen 上限被拒绝的 URL 数。
//
// 非零说明这次采集的结果不完整——调用方应当提示，而不是当作正常结束。
func (f *Frontier) Dropped() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropped
}

// Len 返回已见 URL 总数，便于统计。
func (f *Frontier) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.seen)
}

// InScope 对外暴露范围判定，便于调用方在入队前自行预判。
func (f *Frontier) InScope(u *url.URL) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inScopeLocked(u)
}

func (f *Frontier) inScopeLocked(u *url.URL) bool {
	if f.opts.Scope == Any {
		return true
	}
	host := strings.ToLower(u.Host)
	for _, s := range f.seeds {
		if strings.EqualFold(s.Host, host) {
			return true
		}
	}
	if f.opts.Scope == SameHost {
		return false
	}
	rd := strings.ToLower(RootDomain(u.Hostname()))
	for _, s := range f.seeds {
		if rd == strings.ToLower(RootDomain(s.Hostname())) {
			return true
		}
	}
	return false
}

func (f *Frontier) passFiltersLocked(rawURL string) bool {
	for _, re := range f.opts.Exclude {
		if re != nil && re.MatchString(rawURL) {
			return false
		}
	}
	if len(f.opts.Include) == 0 {
		return true
	}
	for _, re := range f.opts.Include {
		if re != nil && re.MatchString(rawURL) {
			return true
		}
	}
	return false
}

// Normalize 把 raw 相对于 base 解析为绝对 URL 并做保守归一化。
//
// 归一化只做到「同一资源不重复入队」为止，刻意不排序 query、不折叠路径大小写、
// 不去尾部斜杠——这些差异在真实站点上可能代表不同资源，过度归一会漏资产。
func Normalize(raw string, base *url.URL) (*url.URL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	raw = strings.NewReplacer("\n", "", "\r", "").Replace(raw)
	raw = strings.TrimSpace(raw)

	lower := strings.ToLower(raw)
	for _, p := range []string{"javascript:", "data:", "mailto:", "tel:", "about:", "blob:", "file:"} {
		if strings.HasPrefix(lower, p) {
			return nil, false
		}
	}
	if strings.HasPrefix(raw, "#") {
		return nil, false
	}

	var (
		u   *url.URL
		err error
	)
	if base != nil {
		u, err = base.Parse(raw)
	} else {
		u, err = url.Parse(raw)
	}
	if err != nil || u == nil {
		return nil, false
	}

	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return nil, false
	}
	if u.Host == "" {
		return nil, false
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""

	// 去掉默认端口，让 http://a.com:80 与 http://a.com 视为同一资源
	host := u.Hostname()
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		if strings.Contains(host, ":") { // IPv6
			u.Host = "[" + host + "]"
		} else {
			u.Host = host
		}
	}

	if u.Path != "" {
		u.Path = CleanPath(u.Path)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u, true
}

// CleanPath 折叠路径中的 "."、".." 与重复 "//"，保留尾部斜杠语义。
func CleanPath(p string) string {
	if p == "" {
		return p
	}
	trailing := strings.HasSuffix(p, "/")
	segs := strings.Split(p, "/")
	out := make([]string, 0, len(segs))
	for _, s := range segs {
		switch s {
		case "", ".":
			continue
		case "..":
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
		default:
			out = append(out, s)
		}
	}
	res := "/" + strings.Join(out, "/")
	if trailing && res != "/" {
		res += "/"
	}
	return res
}

// RootDomain 取可注册域（eTLD+1）。IP 与非常规域名退化为自身。
func RootDomain(host string) string {
	h := StripPort(host)
	if ip := net.ParseIP(h); ip != nil {
		return h
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(h)
	if err != nil {
		return h
	}
	return d
}

// StripPort 去掉 host 中的端口部分。
func StripPort(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
