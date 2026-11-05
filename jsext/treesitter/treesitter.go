// Package treesitter 用 odvcencio/gotreesitter（纯 Go 的 tree-sitter 运行时）
// 实现 jsext 引擎。
//
// 与 goja 版的差别在于数据来源：goja 提供 Go 结构体形式的 AST，可以直接类型断言；
// tree-sitter 提供按语法规则命名的节点树，需要按节点类型名（call_expression /
// member_expression / pair ...）匹配，并用 ChildByFieldName 取具名子节点。
//
// 语法覆盖更完整（支持 ESM 的 import/export 与动态 import），代价是解析速度
// 明显慢于 goja（实测 47KB 约 100ms，goja 约 1.3ms）。
package treesitter

import (
	"regexp"
	"strings"

	"github.com/odvcencio/gotreesitter"
	jsgrammar "github.com/odvcencio/gotreesitter/grammars/javascript"

	"assetcrawler/jsext"
)

// sourceMapRe 匹配 //# sourceMappingURL=app.js.map 这类注释。
var sourceMapRe = regexp.MustCompile(`[#@]\s*sourceMappingURL=(\S+)`)

var httpVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT",
	"delete": "DELETE", "patch": "PATCH", "head": "HEAD", "options": "OPTIONS",
}

var httpClients = map[string]bool{
	"axios": true, "$": true, "jQuery": true,
	"http": true, "request": true, "client": true, "api": true, "req": true,
}

// Extract 解析 JS 源码并返回请求候选。
func Extract(src []byte) ([]jsext.Request, error) {
	lang := jsgrammar.Language()
	p := gotreesitter.NewParser(lang)
	tree, err := p.Parse(src)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	e := &extractor{src: src, lang: lang, vars: map[string]string{}}
	root := tree.RootNode()
	e.collectConsts(root)
	e.collectCalls(root)
	e.collectBinaries(root)
	e.collectComments(root)
	e.collectImports(root)
	return jsext.Dedup(e.out), nil
}

type extractor struct {
	src  []byte
	lang *gotreesitter.Language
	vars map[string]string
	out  []jsext.Request
}

// collectConsts 收集 `var X = "字面量"` 与 `const X = "字面量"`。
func (e *extractor) collectConsts(root *gotreesitter.Node) {
	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "variable_declarator" {
			return
		}
		name := n.ChildByFieldName("name", e.lang)
		value := n.ChildByFieldName("value", e.lang)
		if name == nil || value == nil || name.Type(e.lang) != "identifier" {
			return
		}
		if s, ok := e.literal(value); ok {
			e.vars[name.Text(e.src)] = s
		}
	})
}

// collectImports 提取静态 import 语句的模块路径（`import x from "./dep.mjs"`）。
func (e *extractor) collectImports(root *gotreesitter.Node) {
	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "import_statement" {
			return
		}
		src := n.ChildByFieldName("source", e.lang)
		if src == nil {
			return
		}
		if v, ok := e.literal(src); ok && urlLike(v) {
			e.out = append(e.out, jsext.Request{URL: v, Confidence: jsext.Certain})
		}
	})
}

// collectComments 提取注释里的 sourceMappingURL——它能顺藤摸到前端源码结构。
func (e *extractor) collectComments(root *gotreesitter.Node) {
	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "comment" {
			return
		}
		m := sourceMapRe.FindStringSubmatch(n.Text(e.src))
		if len(m) < 2 {
			return
		}
		if urlLike(m[1]) {
			e.out = append(e.out, jsext.Request{URL: m[1], Confidence: jsext.Certain})
		}
	})
}

func (e *extractor) collectCalls(root *gotreesitter.Node) {
	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "call_expression" {
			return
		}
		if req, ok := e.fromCall(n); ok {
			e.out = append(e.out, req)
		}
	})
}

func (e *extractor) fromCall(n *gotreesitter.Node) (jsext.Request, bool) {
	fnNode := n.ChildByFieldName("function", e.lang)
	argsNode := n.ChildByFieldName("arguments", e.lang)
	if fnNode == nil || argsNode == nil {
		return jsext.Request{}, false
	}

	obj, name := e.calleeOf(fnNode)
	if name == "" {
		return jsext.Request{}, false
	}
	// 动态 import("./chunk.js")：callee 是关键字 import，参数就是要加载的模块
	if name == "import" && len(namedChildren(argsNode)) >= 1 {
		arg := namedChildren(argsNode)[0]
		if raw, certain := e.eval(arg); urlLike(raw) {
			conf := jsext.Certain
			if !certain {
				conf = jsext.Template
			}
			return jsext.Request{URL: raw, Confidence: conf}, true
		}
		return jsext.Request{}, false
	}
	args := namedChildren(argsNode)

	// $.ajax({url:"...", type:"POST", data:{...}}) —— URL 与参数都在配置对象里
	if len(args) == 1 {
		if u := e.objField(args[0], "url"); u != nil {
			raw, certain := e.eval(u)
			if urlLike(raw) {
				method := ""
				for _, f := range []string{"type", "method"} {
					if v := e.objField(args[0], f); v != nil {
						if sv, ok := e.literal(v); ok {
							method = strings.ToUpper(sv)
							break
						}
					}
				}
				conf := jsext.Certain
				if !certain {
					conf = jsext.Template
				}
				return jsext.Request{
					URL: raw, Method: method,
					BodyParams: e.objKeys(e.objField(args[0], "data")),
					Confidence: conf,
				}, true
			}
		}
	}

	// xhr.open("PUT", url)：URL 在第二个位置
	urlIdx := 0
	method := ""
	if strings.EqualFold(name, "open") && len(args) >= 2 {
		urlIdx = 1
		if m, ok := e.literal(args[0]); ok {
			method = strings.ToUpper(strings.TrimSpace(m))
		}
	}
	if urlIdx >= len(args) {
		return jsext.Request{}, false
	}

	raw, certain := e.eval(args[urlIdx])
	if !urlLike(raw) {
		return jsext.Request{}, false
	}
	if method == "" {
		method = e.methodOf(obj, name, args)
	}
	query, body := e.paramsOf(obj, name, args)

	conf := jsext.Certain
	if !certain {
		conf = jsext.Template
	}
	return jsext.Request{
		URL: raw, Method: method,
		QueryParams: query, BodyParams: body,
		Confidence: conf,
	}, true
}

// collectBinaries 处理调用参数之外的字符串拼接。
func (e *extractor) collectBinaries(root *gotreesitter.Node) {
	type cand struct {
		text       string
		start, end uint32
		certain    bool
	}
	var cands []cand

	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "binary_expression" {
			return
		}
		op := n.ChildByFieldName("operator", e.lang)
		if op == nil || op.Text(e.src) != "+" {
			return
		}
		s, certain := e.eval(n)
		if !urlLike(s) {
			return
		}
		cands = append(cands, cand{s, n.StartByte(), n.EndByte(), certain})
	})

	for i, a := range cands {
		inner := false
		for j, b := range cands {
			if i != j && b.start <= a.start && a.end <= b.end && (b.start < a.start || a.end < b.end) {
				inner = true
				break
			}
		}
		if inner {
			continue
		}
		conf := jsext.Certain
		if !a.certain {
			conf = jsext.Template
		}
		e.out = append(e.out, jsext.Request{URL: a.text, Confidence: conf})
	}
}

func (e *extractor) methodOf(obj, name string, args []*gotreesitter.Node) string {
	lower := strings.ToLower(name)
	if httpClients[obj] {
		if m, ok := httpVerbs[lower]; ok {
			return m
		}
	}
	switch lower {
	case "fetch":
		if len(args) >= 2 {
			if v := e.objField(args[1], "method"); v != nil {
				if s, ok := e.literal(v); ok {
					return strings.ToUpper(s)
				}
			}
		}
		return "GET"
	case "sendbeacon":
		return "POST"
	case "ajax":
		if len(args) >= 1 {
			for _, f := range []string{"type", "method"} {
				if v := e.objField(args[0], f); v != nil {
					if s, ok := e.literal(v); ok {
						return strings.ToUpper(s)
					}
				}
			}
		}
	}
	return ""
}

func (e *extractor) paramsOf(obj, name string, args []*gotreesitter.Node) (query, body []string) {
	if len(args) < 2 {
		return nil, nil
	}
	second := args[1]
	lower := strings.ToLower(name)

	if httpClients[obj] {
		switch lower {
		case "get", "delete", "head", "options":
			if p := e.objField(second, "params"); p != nil {
				query = e.objKeys(p)
			} else if obj == "$" || obj == "jQuery" {
				query = e.objKeys(second)
			}
		case "post", "put", "patch":
			body = e.objKeys(second)
		}
		return query, body
	}
	if lower == "fetch" {
		if b := e.objField(second, "body"); b != nil {
			body = e.objKeys(b)
		}
	}
	return query, body
}

func (e *extractor) objKeys(n *gotreesitter.Node) []string {
	if n == nil || n.Type(e.lang) != "object" {
		return nil
	}
	var keys []string
	for i := 0; i < n.NamedChildCount(); i++ {
		p := n.NamedChild(i)
		switch p.Type(e.lang) {
		case "pair":
			if k := p.ChildByFieldName("key", e.lang); k != nil {
				keys = jsext.AddParam(keys, strings.Trim(k.Text(e.src), `"'`))
			}
		case "shorthand_property_identifier":
			keys = jsext.AddParam(keys, p.Text(e.src))
		}
	}
	return keys
}

func (e *extractor) objField(n *gotreesitter.Node, field string) *gotreesitter.Node {
	if n == nil || n.Type(e.lang) != "object" {
		return nil
	}
	for i := 0; i < n.NamedChildCount(); i++ {
		p := n.NamedChild(i)
		if p.Type(e.lang) != "pair" {
			continue
		}
		k := p.ChildByFieldName("key", e.lang)
		if k != nil && strings.Trim(k.Text(e.src), `"'`) == field {
			return p.ChildByFieldName("value", e.lang)
		}
	}
	return nil
}

// eval 做部分求值。
func (e *extractor) eval(n *gotreesitter.Node) (string, bool) {
	if n == nil {
		return "EXPR", false
	}
	switch n.Type(e.lang) {
	case "string", "template_string":
		return e.literal(n)
	case "identifier":
		if s, ok := e.vars[n.Text(e.src)]; ok {
			return s, true
		}
	case "binary_expression":
		op := n.ChildByFieldName("operator", e.lang)
		if op != nil && op.Text(e.src) == "+" {
			l, lok := e.eval(n.ChildByFieldName("left", e.lang))
			r, rok := e.eval(n.ChildByFieldName("right", e.lang))
			return l + r, lok && rok
		}
	case "parenthesized_expression":
		if c := namedChildren(n); len(c) == 1 {
			return e.eval(c[0])
		}
	}
	return "EXPR", false
}

// literal 取字符串或模板字符串的字面值，含插值则判为不确定。
func (e *extractor) literal(n *gotreesitter.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	switch n.Type(e.lang) {
	case "string":
		for i := 0; i < n.ChildCount(); i++ {
			if c := n.Child(i); c.Type(e.lang) == "string_fragment" {
				return c.Text(e.src), true
			}
		}
		return "", false
	case "template_string":
		for i := 0; i < n.ChildCount(); i++ {
			if n.Child(i).Type(e.lang) == "template_substitution" {
				return "", false
			}
		}
		return strings.Trim(n.Text(e.src), "`"), true
	}
	return "", false
}

func (e *extractor) calleeOf(n *gotreesitter.Node) (obj, name string) {
	switch n.Type(e.lang) {
	case "identifier", "import", "import_statement":
		return "", n.Text(e.src)
	case "member_expression":
		objNode := n.ChildByFieldName("object", e.lang)
		propNode := n.ChildByFieldName("property", e.lang)
		if propNode == nil {
			return "", ""
		}
		if objNode != nil && objNode.Type(e.lang) == "identifier" {
			obj = objNode.Text(e.src)
		}
		return obj, propNode.Text(e.src)
	}
	return "", ""
}

// walk 深度优先遍历所有节点（含未命名节点）。
func (e *extractor) walk(n *gotreesitter.Node, fn func(*gotreesitter.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for i := 0; i < n.ChildCount(); i++ {
		e.walk(n.Child(i), fn)
	}
}

func namedChildren(n *gotreesitter.Node) []*gotreesitter.Node {
	if n == nil {
		return nil
	}
	out := make([]*gotreesitter.Node, 0, n.NamedChildCount())
	for i := 0; i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

func urlLike(s string) bool {
	if s == "" || len(s) > 2048 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return false
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "//") {
		return true
	}
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return len(s) > 1
	}
	return strings.Contains(s, "/") || strings.Contains(s, ".")
}
