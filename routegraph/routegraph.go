package routegraph

import "strings"

// Result 是一次静态恢复的产出。
type Result struct {
	Chunks []Chunk
	Routes []Route
}

// Empty 表示没有任何产出。
func (r Result) Empty() bool { return len(r.Chunks) == 0 && len(r.Routes) == 0 }

// Extract 从一份前端产物里静态恢复 chunk 与路由。
//
// 纯静态：不执行任何代码，对 ESM / Vite 产物同样有效。
// 输入应当是 JS 文件内容（内联脚本也可以）。
func Extract(src []byte) Result {
	if len(src) == 0 {
		return Result{}
	}
	tl := lexAll(src)

	var res Result
	publicPath := tl.webpackPublicPath()

	// chunk 来源按可靠性排序：webpack 运行时清单 > vite 依赖表 > 动态 import
	res.Chunks = append(res.Chunks, tl.webpackChunks(publicPath)...)
	res.Chunks = append(res.Chunks, tl.viteChunks()...)
	res.Chunks = append(res.Chunks, tl.dynamicImports()...)
	res.Chunks = dedupeChunks(res.Chunks)

	res.Routes = tl.routeTables()
	return res
}

// dedupeChunks 按路径去重，保留先出现的来源（越靠前越可靠）。
func dedupeChunks(in []Chunk) []Chunk {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, c := range in {
		key := c.Path
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

// LooksLikeBundle 粗判一段脚本是否值得做 chunk/路由恢复。
//
// 只做「有没有必要」的判断，不做精确性判断：
// 内容太短、或既没有运行时清单特征也没有路由特征时直接跳过，
// 避免在每个小脚本上跑一遍词法分析。
func LooksLikeBundle(src []byte) bool {
	if len(src) < 64 {
		return false
	}
	s := string(src)
	for _, mark := range []string{
		"__webpack_require__", "__vite__", "webpackJsonp", "webpackChunk",
		"createRouter", "createWebHistory", "createBrowserRouter",
		"routes:", "routes=", "__vitePreload", "miniCssF",
	} {
		if strings.Contains(s, mark) {
			return true
		}
	}
	return false
}
