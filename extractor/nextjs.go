package extractor

import (
	"regexp"
	"strconv"
	"strings"
)

// Next.js（App Router）把服务端组件树序列化成若干段 JSON，逐段塞进
// `<script>self.__next_f.push([1,"..."])</script>`。这些片段里同时带着
//
//   - chunk 路径：`/_next/static/chunks/x.js`，以及省略前缀的 `static/chunks/x.js`
//   - 页面路径：`/docs`、`/docs/15`、`/support-policy`
//   - 外链：`https://vercel.com/templates/...`
//
// 这是文件路由框架里**唯一结构化暴露导航结构**的地方——Next.js 的页面路径
// 来自构建时的目录扫描，运行时只剩这份 payload。所以它比"从路由表里找
// routes 数组"更可靠，值得单独解析。
//
// 设计上刻意不做 RSC 结构解析：payload 的语法随版本变化（`$L`、`$S` 等标记），
// 而我们要的只是"里面有哪些像地址的字符串"。逐片段取字符串字面量即可，
// 不需要跨片段拼接——单个片段内就已经是完整的路径值。

// nextFlightRe 匹配一段 `self.__next_f.push([1,"..."])`，捕获其中的字符串。
//
// 字符串里允许出现转义序列，所以用 `(?:[^"\\]|\\.)*` 而不是 `.*?`：
// 后者遇到 `\"` 会提前截断，半个 payload 被当成完整片段。
var nextFlightRe = regexp.MustCompile(`self\.__next_f\.push\(\s*\[\s*1\s*,\s*"((?:[^"\\]|\\.)*)"`)

// flightStringRe 取 payload 里的字符串字面量。
var flightStringRe = regexp.MustCompile(`"((?:[^"\\]|\\.){2,200})"`)

// maxFlightPaths 限制单页产出，避免超大 payload 把队列灌满。
const maxFlightPaths = 400

// LooksLikeNextFlight 判断一段内联脚本是否是 Next.js 的 RSC 流。
func LooksLikeNextFlight(script string) bool {
	return strings.Contains(script, "__next_f")
}

// FromNextFlight 从 Next.js 的 RSC 流式脚本里提取路径。
//
// 产出分两类：
//   - 构建产物（.js/.css/.woff 结尾）→ Kind=chunk，与 webpack/Vite 的 chunk 同义
//   - 其余路径 → 普通 URL（页面或接口），按正常流程抓取
func FromNextFlight(script string) []Request {
	matches := nextFlightRe.FindAllStringSubmatch(script, -1)
	if len(matches) == 0 {
		return nil
	}

	var out []Request
	seen := make(map[string]struct{}, 64)
	for _, m := range matches {
		payload, ok := unescapeJSString(m[1])
		if !ok {
			continue
		}
		for _, lit := range flightStringRe.FindAllStringSubmatch(payload, -1) {
			raw, ok := unescapeJSString(lit[1])
			if !ok {
				continue
			}
			p, kind, ok := nextFlightPath(raw)
			if !ok {
				continue
			}
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, Request{
				Raw: p, Method: "GET", Source: "next-flight", Kind: kind,
			})
			if len(out) >= maxFlightPaths {
				return out
			}
		}
	}
	return out
}

// nextFlightPath 判断一个 payload 字符串是否是可用地址，并补全缺失的前缀。
//
// App Router 的 chunk 引用有两种写法，实测同一份 payload 里混着出现：
//
//	"/_next/static/chunks/a.js"   带前缀
//	"static/chunks/b.js"          省略了 /_next/
//
// 后者按 Next.js 的固定目录约定补回 `/_next/`；`/_next/` 是构建产物目录，
// 不受 basePath / assetPrefix 影响（那两个只加在它前面）。
func nextFlightPath(s string) (string, string, bool) {
	if s == "" || len(s) > 300 {
		return "", "", false
	}
	// 含空白的一律丢弃：payload 里的文案、RSC 标记都会混进来
	if strings.ContainsAny(s, " \t\r\n") {
		return "", "", false
	}

	switch {
	case strings.HasPrefix(s, "//"):
		// 协议相对：交给上层按当前页面的 scheme 解析
		return s, "", true
	case strings.HasPrefix(s, "/"):
		// 排除 RSC 标记与模板占位：`/$L123`、`/x/EXPR`
		if strings.HasPrefix(s, "/$") || strings.Contains(s, "EXPR") {
			return "", "", false
		}
		return s, chunkKindOf(s), true
	case strings.HasPrefix(s, "static/"):
		p := "/_next/" + s
		return p, chunkKindOf(p), true
	}
	return "", "", false
}

// chunkKindOf 判断是否指向构建产物。
func chunkKindOf(p string) string {
	for _, ext := range []string{
		".js", ".mjs", ".css",
		".woff", ".woff2", ".ttf", ".otf",
		".svg", ".png", ".jpg", ".jpeg", ".webp", ".avif", ".ico",
	} {
		if strings.HasSuffix(p, ext) {
			return "chunk"
		}
	}
	return ""
}

// unescapeJSString 还原 JS 字符串字面量的转义。
//
// 不直接用 strconv.Unquote：JS 允许 `\/` 这种 Go 不认的转义，
// 混进一个就会让整段解析失败，而 payload 里 URL 前缀大量出现 `\/`。
func unescapeJSString(s string) (string, bool) {
	if !strings.ContainsRune(s, '\\') {
		return s, true
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", false
		}
		switch s[i] {
		case '"', '\'', '\\', '/':
			b.WriteByte(s[i])
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b', 'f', 'v', '0':
			// 对这几种控制符我们不关心内容，占位成空格避免粘连
			b.WriteByte(' ')
		case 'u':
			if i+4 >= len(s) {
				return "", false
			}
			r, err := strconv.ParseUint(s[i+1:i+5], 16, 32)
			if err != nil {
				return "", false
			}
			b.WriteRune(rune(r))
			i += 4
		default:
			// 未知转义按字面保留
			b.WriteByte(s[i])
		}
	}
	return b.String(), true
}
