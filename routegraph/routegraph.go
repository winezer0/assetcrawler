package routegraph

import "bytes"

// Result 是一次静态恢复的产出。
type Result struct {
	Chunks []Chunk
	Routes []Route
	// Truncated 表示源码超过词法上限，后半部分没有参与扫描，
	// 因此 Chunks/Routes 可能不完整。调用方应当据此提示用户，
	// 而不是把「没找到」当成「不存在」。
	Truncated bool
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
	res.Truncated = tl.truncated
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
	// 用 bytes.Contains 而不是先转成 string：body 最大 2MB，
	// 每个脚本多一次整包拷贝没有意义。
	for _, mark := range [][]byte{
		[]byte("__webpack_require__"), []byte("__vite__"), []byte("webpackJsonp"),
		[]byte("webpackChunk"), []byte("createRouter"), []byte("createWebHistory"),
		[]byte("createBrowserRouter"), []byte("routes:"), []byte("routes="),
		[]byte("__vitePreload"), []byte("miniCssF"),
	} {
		if bytes.Contains(src, mark) {
			return true
		}
	}
	return false
}
