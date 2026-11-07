package extractor

import "golang.org/x/net/html"

// fromInlineScript 统一分派内联 <script> 的内容。
//
// 分派顺序按「结构化程度」从高到低：专用载体解析 → 通用 JS 提取。
// 关键约定是**任何一层产出为空都要继续往下走**，不能整段丢弃：
// 例如只含 `self.__next_f=self.__next_f||[]` 的初始化脚本里，
// 既有 __next_f 标记，也可能同时写着普通的接口地址。
func fromInlineScript(n *html.Node, src string, o Options) []Request {
	if kind, ok := inlineJSONKind(n); ok {
		// 结构化解析 + 原有 AST 提取**合并**，不是二选一。
		// 2026-11-07 的 800 配额 A/B 实测：只跑 FromInlineJSON 会丢掉旧版
		// 从同一份 payload 里拿到的 75 条真实 URL（外链脚本、logo 等）——
		// 它们要么排在 200 条上限之后，要么是被我们过滤的静态资源。
		// 两者语义不同：一个按 JSON 解码还原 \u002F，一个覆盖更宽的字段，
		// 合并后由 frontier 去重，不会互相抵消。
		reqs := FromInlineJSON(src, kind)
		return append(reqs, FromJS(src, o)...)
	}
	if LooksLikeNextFlight(src) {
		if reqs := FromNextFlight(src); len(reqs) > 0 {
			return reqs
		}
	}
	return FromJS(src, o)
}
