package routegraph

import (
	"sort"
	"strings"
)

// Chunk 是一个恢复出来的 chunk 资源。
type Chunk struct {
	// Path 原始路径（可能是相对 publicPath 的片段）。
	Path string
	// ID 打包器内部编号（webpack 的 chunk id）。
	ID string
	// Name 语义名（webpack 的 chunk name，存在时才有）。
	Name string
	// Source 命中的识别点，如 webpack.u / vite.mapDeps / dynamic-import。
	Source string
}

// tokenList 是一次性词法分析的结果，方便按下标做括号配平。
type tokenList struct {
	src  []byte
	toks []Token
	// truncated 表示 token 数量触到了上限，后面的源码没有被扫描。
	// 必须往上报：否则调用方只会看到「没找到清单」，而不知道是被截断了。
	truncated bool
}

func lexAll(src []byte) *tokenList {
	lx := NewLexer(src)
	// 预分配：Token 是 40 字节，不预分配时 append 的倍增扩容会反复拷贝，
	// 实测占掉全部内存分配的 91%（1MB 源码约 111MB）。
	// 压缩代码大约 2 字节一个 token，按 /2 估能一次分配到位；源码含注释时略高估，
	// 但峰值分配比扩容拷贝小得多。
	est := len(src)/2 + 8
	if est > maxTokens {
		est = maxTokens
	}
	out := &tokenList{src: src, toks: make([]Token, 0, est)}
	for i := 0; i < maxTokens; i++ {
		t := lx.Next()
		if t.Kind == TokEOF {
			break
		}
		if i == maxTokens-1 {
			out.truncated = true // 达到上限仍未见 EOF
		}
		if t.Kind == TokComment {
			continue // 注释里的清单不是活的
		}
		out.toks = append(out.toks, t)
	}
	return out
}

// maxTokens 防止超大文件把内存吃光（1MB 压缩代码大约 30 万 token）。
const maxTokens = 2 << 20

func (tl *tokenList) text(i int) string {
	if i < 0 || i >= len(tl.toks) {
		return ""
	}
	return tl.toks[i].Text(tl.src)
}

// is 判断第 i 个 token 的原文是否等于 s，**不分配字符串**。
//
// 扫描循环里每个 token 都调一次 text() 会产生一次 string(src[a:b]) 拷贝，
// 实测 1MB 源码 11.9 万次分配、94MB 内存，主因就在这里。
func (tl *tokenList) is(i int, s string) bool {
	if i < 0 || i >= len(tl.toks) {
		return false
	}
	t := tl.toks[i]
	if int(t.End-t.Start) != len(s) {
		return false
	}
	for k := 0; k < len(s); k++ {
		if tl.src[int(t.Start)+k] != s[k] {
			return false
		}
	}
	return true
}

// identIs 判断第 i 个 token 是否是指定标识符。
func (tl *tokenList) identIs(i int, s string) bool {
	return tl.kind(i) == TokIdent && tl.is(i, s)
}

// identHasPrefix 判断第 i 个 token 是否是以 p 开头的标识符。
func (tl *tokenList) identHasPrefix(i int, pfx string) bool {
	if tl.kind(i) != TokIdent {
		return false
	}
	t := tl.toks[i]
	if int(t.End-t.Start) < len(pfx) {
		return false
	}
	for k := 0; k < len(pfx); k++ {
		if tl.src[int(t.Start)+k] != pfx[k] {
			return false
		}
	}
	return true
}

func (tl *tokenList) kind(i int) Kind {
	if i < 0 || i >= len(tl.toks) {
		return TokEOF
	}
	return tl.toks[i].Kind
}

func (tl *tokenList) value(i int) string {
	if i < 0 || i >= len(tl.toks) {
		return ""
	}
	return tl.toks[i].Value(tl.src)
}

// matchClose 从开括号位置返回配对的闭括号下标，找不到返回 -1。
func (tl *tokenList) matchClose(open int) int {
	if open < 0 || open >= len(tl.toks) || tl.kind(open) != TokPunct {
		return -1
	}
	var openCh, closeCh string
	switch {
	case tl.is(open, "{"):
		openCh, closeCh = "{", "}"
	case tl.is(open, "["):
		openCh, closeCh = "[", "]"
	case tl.is(open, "("):
		openCh, closeCh = "(", ")"
	default:
		return -1
	}
	depth := 0
	for i := open; i < len(tl.toks); i++ {
		if tl.kind(i) != TokPunct {
			continue
		}
		if tl.is(i, openCh) {
			depth++
			continue
		}
		if tl.is(i, closeCh) {
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// webpackPublicPath 找 `__webpack_require__.p = "/static/js/"` 里的前缀。
// 压缩后形如 `o.p="/static/js/"`，取「名为 p 的属性被赋值为字符串」这一形态。
func (tl *tokenList) webpackPublicPath() string {
	for i := 1; i+2 < len(tl.toks); i++ {
		if !tl.identIs(i, "p") {
			continue
		}
		if !tl.is(i-1, ".") {
			continue
		}
		if !tl.is(i+1, "=") || tl.kind(i+2) != TokString {
			continue
		}
		return tl.value(i + 2)
	}
	return ""
}

// chunkExpr 是 `u` 函数体拆出来的模板。
type chunkExpr struct {
	// parts 依次是字面量片段与占位符；占位符用 idMark / hashMark 表示。
	parts []string
	// table 是 hash 查找表：chunk id -> 内容哈希。
	table map[string]string
	// names 是可选的名字查找表：chunk id -> 语义名。
	names map[string]string
}

const (
	idMark   = "\x00ID\x00"
	hashMark = "\x00HASH\x00"
	nameMark = "\x00NAME\x00"
)

// webpackChunks 从 `__webpack_require__.u` 恢复 chunk 文件名。
//
// 关键点：`{101:"a1b2c3",...}[e]` 必须当成**一个整体**，
// 下标里的 e 与文件名里的 e 是同一个变量，要按同一个 key 一起替换；
// 拆开单独替换会产出 `chunk-101.d4e5f6.js` 这种非法组合。
func (tl *tokenList) webpackChunks(publicPath string) []Chunk {
	var out []Chunk
	for i := 0; i+2 < len(tl.toks); i++ {
		// 形如 . u = function / . u = ( / . u = e =>
		if !tl.identIs(i, "u") {
			continue
		}
		if !tl.is(i-1, ".") || !tl.is(i+1, "=") {
			continue
		}
		rhsStart, rhsEnd := tl.rhsRange(i + 1)
		if rhsEnd <= rhsStart {
			continue
		}
		expr, ok := tl.parseChunkExpr(rhsStart, rhsEnd)
		if !ok {
			continue
		}
		for _, c := range expr.render(publicPath) {
			c.Source = "webpack.u"
			out = append(out, c)
		}
	}
	return out
}

// rhsRange 返回赋值右侧表达式的 token 区间 [start, end]。
func (tl *tokenList) rhsRange(eq int) (int, int) {
	start := eq + 1
	depth := 0
	for i := start; i < len(tl.toks); i++ {
		if tl.kind(i) != TokPunct {
			continue
		}
		switch {
		case tl.is(i, "{") || tl.is(i, "[") || tl.is(i, "("):
			depth++
		case tl.is(i, "}") || tl.is(i, "]") || tl.is(i, ")"):
			depth--
			if depth < 0 {
				// 赋值处在对象/数组内部，遇到外层闭合就结束
				return start, i - 1
			}
		case tl.is(i, ",") || tl.is(i, ";"):
			if depth == 0 {
				return start, i - 1
			}
		}
	}
	return start, len(tl.toks) - 1
}

// parseChunkExpr 解析 `function(e){return "chunk-"+e+"."+{...}[e]+".js"}` 这类表达式。
func (tl *tokenList) parseChunkExpr(start, end int) (chunkExpr, bool) {
	// 跳过 function(e){ return ... } 的外壳，只留 return 之后的表达式
	if tl.identIs(start, "function") {
		body := -1
		for i := start; i <= end; i++ {
			if tl.is(i, "{") {
				body = i
				break
			}
		}
		if body < 0 {
			return chunkExpr{}, false
		}
		start = body + 1
	}
	for i := start; i <= end; i++ {
		if tl.identIs(i, "return") {
			start = i + 1
			break
		}
	}

	expr := chunkExpr{table: map[string]string{}}
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			expr.parts = append(expr.parts, cur.String())
			cur.Reset()
		}
	}

	for i := start; i <= end; i++ {
		// `{...}[param]` —— 查表，整体消费
		if tl.is(i, "{") {
			close := tl.matchClose(i)
			if close > 0 && close+3 <= end && tl.is(close+1, "[") && tl.is(close+3, "]") {
				if t, n, ok := tl.parseStringMap(i, close); ok {
					flush()
					// 名字表与哈希表形态相同，靠内容区分：值里带扩展名的当名字表
					if looksLikeFile(tableFirst(t)) {
						expr.names = t
						expr.parts = append(expr.parts, nameMark)
					} else {
						expr.table = t
						expr.parts = append(expr.parts, hashMark)
					}
					_ = n
					i = close + 3
					continue
				}
			}
		}
		if tl.kind(i) == TokString {
			cur.WriteString(tl.value(i))
			continue
		}
		// 模板字符串：拆 ${...} 里的占位符
		if tl.kind(i) == TokTemplate {
			for _, seg := range splitTemplate(tl.value(i)) {
				if seg.placeholder {
					flush()
					expr.parts = append(expr.parts, idMark)
					continue
				}
				cur.WriteString(seg.text)
			}
			continue
		}
		if tl.kind(i) == TokIdent && !tl.is(i, "return") {
			if tl.is(i+1, "[") {
				// 形如 map[param] 但没有字面表，无法还原
				i += 2
				continue
			}
			flush()
			expr.parts = append(expr.parts, idMark)
			continue
		}
		if tl.is(i, "+") {
			flush()
		}
	}
	flush()

	if len(expr.table) == 0 {
		return chunkExpr{}, false
	}
	return expr, true
}

func tableFirst(m map[string]string) string {
	for _, v := range m {
		return v
	}
	return ""
}

// parseStringMap 解析 `{101:"a1b2c3",102:"d4e5f6"}`。
func (tl *tokenList) parseStringMap(open, close int) (map[string]string, int, bool) {
	out := map[string]string{}
	for i := open + 1; i < close; i++ {
		if tl.kind(i) != TokString && tl.kind(i) != TokNumber && tl.kind(i) != TokIdent {
			continue
		}
		if !tl.is(i+1, ":") || tl.kind(i+2) != TokString {
			continue
		}
		out[tl.value(i)] = tl.value(i + 2)
		i += 2
	}
	if len(out) == 0 {
		return nil, 0, false
	}
	return out, len(out), true
}

func looksLikeFile(v string) bool {
	lower := strings.ToLower(v)
	for _, ext := range []string{".js", ".mjs", ".css", ".jsx", ".ts"} {
		if strings.HasSuffix(lower, ext) {
			return true
		}
	}
	return false
}

// render 按 key 对齐展开成具体文件名。
func (e chunkExpr) render(publicPath string) []Chunk {
	keys := make([]string, 0, len(e.table))
	for k := range e.table {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Chunk, 0, len(keys))
	for _, k := range keys {
		var sb strings.Builder
		for _, p := range e.parts {
			switch p {
			case idMark:
				sb.WriteString(k)
			case hashMark:
				sb.WriteString(e.table[k])
			case nameMark:
				name := k
				if n, ok := e.names[k]; ok && n != "" {
					name = n
				}
				sb.WriteString(name)
			default:
				sb.WriteString(p)
			}
		}
		path := sb.String()
		if path == "" {
			continue
		}
		out = append(out, Chunk{
			Path:   joinURL(publicPath, path),
			ID:     k,
			Name:   e.names[k],
			Source: "webpack.u",
		})
	}
	return out
}

func joinURL(prefix, path string) string {
	if path == "" {
		return prefix
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") || strings.HasPrefix(path, "/") {
		return path
	}
	if prefix == "" {
		return path
	}
	return strings.TrimSuffix(prefix, "/") + "/" + path
}

type tmplSeg struct {
	text        string
	placeholder bool
}

// splitTemplate 把模板字符串内容拆成「文本 / 占位符」片段。
func splitTemplate(s string) []tmplSeg {
	var out []tmplSeg
	for len(s) > 0 {
		i := strings.Index(s, "${")
		if i < 0 {
			out = append(out, tmplSeg{text: s})
			break
		}
		if i > 0 {
			out = append(out, tmplSeg{text: s[:i]})
		}
		depth := 0
		j := i
		for ; j < len(s); j++ {
			if s[j] == '{' {
				depth++
			} else if s[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		out = append(out, tmplSeg{placeholder: true})
		if j+1 >= len(s) {
			break
		}
		s = s[j+1:]
	}
	return out
}

// viteChunks 从 Vite 内联的依赖表里恢复 chunk。
// Vite 会把 `__vite__mapDeps` 的数组直接写在入口文件里，
// 元素就是形如 `assets/Admin.a1b2c3.js` 的相对路径。
func (tl *tokenList) viteChunks() []Chunk {
	hasVite := false
	for i := range tl.toks {
		if tl.identHasPrefix(i, "__vite__") {
			hasVite = true
			break
		}
	}
	if !hasVite {
		return nil
	}
	var out []Chunk
	seen := map[string]struct{}{}
	for i := range tl.toks {
		if tl.kind(i) != TokString {
			continue
		}
		v := tl.value(i)
		if !looksLikeChunkPath(v) {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, Chunk{Path: v, Source: "vite.mapDeps"})
	}
	return out
}

// dynamicImports 收集 import("...") 的模块说明符。
func (tl *tokenList) dynamicImports() []Chunk {
	var out []Chunk
	seen := map[string]struct{}{}
	for i := 0; i+2 < len(tl.toks); i++ {
		if !tl.identIs(i, "import") {
			continue
		}
		if !tl.is(i+1, "(") {
			continue
		}
		j := i + 2
		if j < len(tl.toks) && tl.kind(j) == TokString {
			v := tl.value(j)
			if looksLikeChunkPath(v) {
				if _, dup := seen[v]; !dup {
					seen[v] = struct{}{}
					out = append(out, Chunk{Path: v, Source: "dynamic-import"})
				}
			}
			continue
		}
		// 拼接形式：import("./chunk-" + id + ".js")，只有常量前缀能确定
		var sb strings.Builder
		for ; j < len(tl.toks) && !tl.is(j, ")"); j++ {
			if tl.kind(j) == TokString {
				sb.WriteString(tl.value(j))
				continue
			}
			if tl.is(j, "+") {
				continue
			}
			sb.WriteString("\x00")
		}
		v := sb.String()
		if strings.Contains(v, "\x00") && looksLikeChunkPath(v) {
			if _, dup := seen[v]; !dup {
				seen[v] = struct{}{}
				out = append(out, Chunk{Path: v, Source: "dynamic-import"})
			}
		}
	}
	return out
}

func looksLikeChunkPath(p string) bool {
	if len(p) < 5 || len(p) > 300 {
		return false
	}
	if strings.HasPrefix(p, "data:") || strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return false
	}
	return looksLikeFile(p) || strings.Contains(p, "/") && looksLikeFile(p)
}
