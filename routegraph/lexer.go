// Package routegraph 从前端打包产物中恢复「静态链接看不见」的两类目标：
//
//   - chunk 文件：打包器把懒加载 chunk 的文件名藏在运行时的清单里，
//     没有任何 HTML 链接指向它们，普通爬虫永远抓不到。
//   - 路由表：SPA 的页面路径写在 JS 的路由配置里，不是服务端链接。
//
// 设计约束：
//   - 默认走**静态**路径：自己的词法分析器 + 结构判断，不执行任何代码，
//     因此对 ESM / Vite 产物同样有效（goja 解析不了 ESM）。
//   - 执行路径（exec.go）是兜底，只用于静态拿不到、且确认是 CJS 产物的场景，
//     必须带超时，因为它会运行目标站的代码。
//   - 纯函数，不依赖网络与包内其他模块。
package routegraph

// Kind 是词法单元的类型。
type Kind uint8

const (
	// TokEOF 表示输入结束。
	TokEOF Kind = iota
	// TokString 是单双引号字符串字面量。
	TokString
	// TokTemplate 是模板字符串（含反引号与内部的 ${} 部分，作为一个整体）。
	TokTemplate
	// TokIdent 是标识符或关键字。
	TokIdent
	// TokNumber 是数字字面量。
	TokNumber
	// TokPunct 是运算符与标点。
	TokPunct
	// TokComment 是行注释或块注释。
	TokComment
	// TokRegex 是正则字面量。
	TokRegex
)

// Token 是一个词法单元。
//
// 刻意压缩到 12 字节：一个 1MB 的压缩包能产出约 50 万 token，
// 结构每多 8 字节就是 4MB 常驻内存。Kind 用 uint8、偏移用 int32
// （单文件超 2GB 不在考虑范围），并去掉 ValStart/ValEnd —— 值区间
// 可以由 Start/End 与类型推导出来（见 Value）。
type Token struct {
	Kind  Kind
	Start int32
	End   int32
}

// Text 返回 token 的原文。
func (t Token) Text(src []byte) string { return string(src[t.Start:t.End]) }

// Value 返回字面量的内容（已去引号），其他类型返回原文。
func (t Token) Value(src []byte) string {
	lo, hi := t.valueRange(src)
	return string(src[lo:hi])
}

// valueRange 推导字面量的内容区间。
//
// 正常闭合的字符串/模板是「首尾各去掉一个引号」；未闭合（换行截断）时
// 只去掉开头那个。非字面量返回原文区间。
func (t Token) valueRange(src []byte) (int32, int32) {
	switch t.Kind {
	case TokString, TokTemplate:
		if t.End-t.Start >= 2 && isQuote(src[t.End-1]) {
			return t.Start + 1, t.End - 1
		}
		return t.Start + 1, t.End
	}
	return t.Start, t.End
}

func isQuote(c byte) bool { return c == '"' || c == '\'' || c == '`' }

// Lexer 是增量式的 JS 词法分析器。
//
// 只做结构识别需要的那部分：区分「代码 / 字符串 / 模板 / 注释 / 正则」，
// 不做数字归一化、不做关键字分类。
type Lexer struct {
	src []byte
	pos int
	// prevSignificant 用于区分除号与正则字面量的起始位置。
	prevSignificant Token
	hasPrev         bool
}

// NewLexer 构造词法分析器。
func NewLexer(src []byte) *Lexer { return &Lexer{src: src} }

// Next 返回下一个有意义的 token（注释也会返回，便于调用方判断区间）。
func (l *Lexer) Next() Token {
	l.skipSpace()
	if l.pos >= len(l.src) {
		end := int32(len(l.src))
		return Token{Kind: TokEOF, Start: end, End: end}
	}
	c := l.src[l.pos]
	start := l.pos

	switch {
	case c == '/' && l.peek(1) == '/':
		l.pos += 2
		for l.pos < len(l.src) && l.src[l.pos] != '\n' {
			l.pos++
		}
		return l.tok(TokComment, start)
	case c == '/' && l.peek(1) == '*':
		l.pos += 2
		for l.pos < len(l.src) {
			if l.src[l.pos] == '*' && l.peek(1) == '/' {
				l.pos += 2
				break
			}
			l.pos++
		}
		return l.tok(TokComment, start)
	case c == '"' || c == '\'':
		return l.scanQuoted(c)
	case c == '`':
		return l.scanTemplate()
	case isIdentStart(c):
		l.pos++
		for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
			l.pos++
		}
		return l.tok(TokIdent, start)
	case c >= '0' && c <= '9':
		l.pos++
		for l.pos < len(l.src) && (isIdentPart(l.src[l.pos]) || l.src[l.pos] == '.') {
			l.pos++
		}
		return l.tok(TokNumber, start)
	case c == '/' && l.regexAllowed():
		return l.scanRegex()
	default:
		l.pos++
		// 三字符运算符（=== !== >>> 等）与两字符运算符都按单字符切分即可，
		// 结构判断只关心 { } [ ] ( ) : , . + = 这些。
		return l.tok(TokPunct, start)
	}
}

func (l *Lexer) tok(k Kind, start int) Token {
	t := Token{Kind: k, Start: int32(start), End: int32(l.pos)}
	l.prevSignificant, l.hasPrev = t, true
	return t
}

func (l *Lexer) peek(n int) byte {
	if l.pos+n >= len(l.src) {
		return 0
	}
	return l.src[l.pos+n]
}

func (l *Lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\r', '\n', '\v', '\f':
			l.pos++
		default:
			return
		}
	}
}

func (l *Lexer) scanQuoted(quote byte) Token {
	start := l.pos
	l.pos++ // 开引号
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
		case quote:
			l.pos++
			t := Token{Kind: TokString, Start: int32(start), End: int32(l.pos)}
			l.prevSignificant, l.hasPrev = t, true
			return t
		case '\n':
			// 未闭合的字符串：到这里为止，避免吞掉后面整段代码
			t := Token{Kind: TokString, Start: int32(start), End: int32(l.pos)}
			l.prevSignificant, l.hasPrev = t, true
			return t
		default:
			l.pos++
		}
	}
	return l.tok(TokString, start)
}

// scanTemplate 把整个模板字符串当作一个 token。
// 内部的 ${} 表达式这里不做拆分——需要静态求值的场景由调用方按字符串处理。
func (l *Lexer) scanTemplate() Token {
	start := l.pos
	l.pos++ // 反引号
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
		case '`':
			l.pos++
			t := Token{Kind: TokTemplate, Start: int32(start), End: int32(l.pos)}
			l.prevSignificant, l.hasPrev = t, true
			return t
		default:
			l.pos++
		}
	}
	t := Token{Kind: TokTemplate, Start: int32(start), End: int32(l.pos)}
	l.prevSignificant, l.hasPrev = t, true
	return t
}

func (l *Lexer) scanRegex() Token {
	start := l.pos
	l.pos++ // 斜杠
	inClass := false
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case '\\':
			l.pos += 2
			continue
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				l.pos++
				for l.pos < len(l.src) && isIdentPart(l.src[l.pos]) {
					l.pos++
				}
				t := Token{Kind: TokRegex, Start: int32(start), End: int32(l.pos)}
				l.prevSignificant, l.hasPrev = t, true
				return t
			}
		case '\n':
			// 换行说明不是正则，退回按除号处理
			l.pos = start + 1
			return l.tok(TokPunct, start)
		}
		l.pos++
	}
	l.pos = start + 1
	return l.tok(TokPunct, start)
}

// regexAllowed 判断当前位置的 / 是正则起始还是除号。
//
// 规则：前一个有意义 token 是标识符 / 数字 / 右括号 / 右方括号 / 字符串时按除号处理，
// 其余（运算符、逗号、左括号、return 等关键字之后）按正则处理。
func (l *Lexer) regexAllowed() bool {
	if !l.hasPrev {
		return true
	}
	p := l.prevSignificant
	switch p.Kind {
	case TokString, TokTemplate, TokNumber, TokRegex:
		return false
	case TokIdent:
		// return /re/ 、typeof /re/ 这类关键字后面是正则
		switch p.Text(l.src) {
		case "return", "typeof", "instanceof", "in", "of", "new", "delete", "void", "do", "else", "case":
			return true
		}
		return false
	case TokPunct:
		t := p.Text(l.src)
		return t != ")" && t != "]" && t != "}"
	}
	return true
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}
