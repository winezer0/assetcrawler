// Package goja 用 dop251/goja 的 JavaScript 解析器实现 jsext 引擎。
//
// 只使用 parser / ast / token 三个子包，**不引入 goja 的 VM**：
// 全程静态解析，不执行任何代码。
//
// 相比纯正则，它能做两件正则做不到的事：
//   - 字符串拼接部分求值：`"/api/" + mode + "/config"` 还原成 `/api/debug/config`
//   - 调用点语义：从 `axios.post(url, {a,b})` 同时拿到 method=POST 与参数 a、b
package goja

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"

	"assetcrawler/jsext"
)

// httpVerbs 只在能确定是 HTTP 客户端的对象上才使用，避免把普通 get() 误判成 GET。
var httpVerbs = map[string]string{
	"get": "GET", "post": "POST", "put": "PUT",
	"delete": "DELETE", "patch": "PATCH", "head": "HEAD", "options": "OPTIONS",
}

// 被视为 HTTP 客户端的接收者名称。
var httpClients = map[string]bool{
	"axios": true, "$": true, "jQuery": true,
	"http": true, "request": true, "client": true, "api": true, "req": true,
}

// Extract 解析 JS 源码并返回请求候选。解析失败时返回 error，由调用方决定是否回退正则。
func Extract(src []byte) ([]jsext.Request, error) {
	// 保留原始源码：ESM 预处理会把 import 语句整行删掉，
	// 而静态 import 的模块路径恰恰只能从原文里取。
	orig := src

	f, err := parser.ParseFile(nil, "", src, 0)
	if err != nil {
		// goja 的 parser 不支持 ESM 语法。剥离 import/export 后再试一次：
		// 这一步只影响语法形状，不改变字面量与拼接关系，因此不影响提取结果。
		fixed, changed := StripModuleSyntax(src)
		if !changed {
			return nil, err
		}
		f, err = parser.ParseFile(nil, "", fixed, 0)
		if err != nil {
			return nil, err
		}
		src = fixed
	}
	e := &extractor{vars: map[string]string{}}
	e.collectConsts(f)
	e.collectCalls(f)
	e.collectBinaries(f)
	e.collectLiterals(f)
	e.collectComments(src)
	e.collectStaticImports(orig)
	return jsext.Dedup(e.out), nil
}

type extractor struct {
	vars map[string]string
	out  []jsext.Request
}

// collectConsts 收集 `var X = "字面量"`，供常量传播使用。
func (e *extractor) collectConsts(root ast.Node) {
	walk(root, func(n any) {
		vs, ok := n.(*ast.VariableStatement)
		if !ok {
			return
		}
		for _, b := range vs.List {
			id, ok := b.Target.(*ast.Identifier)
			if !ok || b.Initializer == nil {
				continue
			}
			// 不只看纯字面量：`var u = c + "/orders"` 也是可静态确定的，
			// webpack 压缩后的产物里这种派生常量非常常见。
			// 因为按源码顺序遍历，被引用的常量此时已经入表。
			if v, certain := e.eval(b.Initializer); certain && v != "" {
				e.vars[id.Name.String()] = v
			}
		}
	})
}

// collectCalls 从调用点提取 URL、方法、参数。这一类结果置信度更高，先入队。
func (e *extractor) collectCalls(root ast.Node) {
	walk(root, func(n any) {
		ce, ok := n.(*ast.CallExpression)
		if !ok {
			return
		}
		if req, ok := e.fromCall(ce); ok {
			e.out = append(e.out, req)
		}
	})
}

func (e *extractor) fromCall(ce *ast.CallExpression) (jsext.Request, bool) {
	obj, name := calleeOf(ce.Callee)
	args := ce.ArgumentList
	if len(args) == 0 || name == "" {
		return jsext.Request{}, false
	}

	// $.ajax({url:"...", type:"POST", data:{...}}) —— URL 与参数都在配置对象里
	if len(args) == 1 {
		if u := e.objField(args[0], "url"); u != nil {
			raw, certain := e.eval(u)
			if urlLike(raw) {
				method := ""
				for _, f := range []string{"type", "method"} {
					if v := e.objField(args[0], f); v != nil {
						if sv, ok := v.(*ast.StringLiteral); ok {
							method = strings.ToUpper(sv.Value.String())
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

	// 动态 import("./chunk.js")：预处理后可能被改成 __import(...)
	if name == "import" || name == "__import" {
		if raw, certain := e.eval(args[0]); urlLike(raw) {
			conf := jsext.Certain
			if !certain {
				conf = jsext.Template
			}
			return jsext.Request{URL: raw, Confidence: conf}, true
		}
		return jsext.Request{}, false
	}

	// xhr.open("PUT", url) 的 URL 在第二个位置，第一个是方法
	urlIdx := 0
	method := ""
	if strings.EqualFold(name, "open") && len(args) >= 2 {
		urlIdx = 1
		if m, certain := e.eval(args[0]); certain {
			method = strings.ToUpper(strings.TrimSpace(m))
		}
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
		URL:         raw,
		Method:      method,
		QueryParams: query,
		BodyParams:  body,
		Confidence:  conf,
	}, true
}

// collectBinaries 处理不在调用参数中的字符串拼接（例如赋值给 location、变量初始化）。
func (e *extractor) collectBinaries(root ast.Node) {
	type cand struct {
		text       string
		start, end int
		certain    bool
	}
	var cands []cand

	walk(root, func(n any) {
		be, ok := n.(*ast.BinaryExpression)
		if !ok || be.Operator != token.PLUS {
			return
		}
		s, certain := e.eval(be)
		if !urlLike(s) {
			return
		}
		cands = append(cands, cand{s, int(be.Idx0()), int(be.Idx1()), certain})
	})

	for i, a := range cands {
		// 丢掉被其它候选严格包含的子表达式（a+b+c 里会同时产出 a+b 和 a+b+c）
		inner := false
		for j, b := range cands {
			if i == j {
				continue
			}
			if b.start <= a.start && a.end <= b.end && (b.start < a.start || a.end < b.end) {
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

// collectLiterals 收集「不是调用参数、也不是拼接」的独立字符串字面量。
//
// 之前的实现只看调用参数和 + 拼接，导致三类常见写法整体丢失：
//   - a.href = "/dyn/page"        赋值给成员的字符串
//   - {plain: "/api/in-js-file"}  对象属性值
//   - ["/api/a", "/api/b"]        数组元素
//
// 只认「完全可确定」的字面量，且赋值只认成员赋值（a.x = ...），
// 不认 `var base = "/api"` 这类通常作为前缀的常量，避免把 base 当成接口。
func (e *extractor) collectLiterals(root ast.Node) {
	emit := func(x ast.Expression) {
		if x == nil {
			return
		}
		v, certain := e.eval(x)
		if !certain || !standaloneURL(v) {
			return
		}
		e.out = append(e.out, jsext.Request{URL: v, Confidence: jsext.Certain})
	}
	walk(root, func(n any) {
		switch v := n.(type) {
		case *ast.AssignExpression:
			// 只认「赋值给 URL 语义的成员」：a.href / f.src / cfg.url。
			// 不加这层会把打包器的 o.p = "/static/js/"（publicPath）
			// 当成一个地址，凭空多出一条 /static/js/ 的 404。
			if !urlishMember(v.Left) {
				return
			}
			emit(v.Right)
		case *ast.ObjectLiteral:
			for _, prop := range v.Value {
				if kv, ok := prop.(*ast.PropertyKeyed); ok {
					emit(kv.Value)
				}
			}
		case *ast.ArrayLiteral:
			for _, el := range v.Value {
				emit(el)
			}
		}
	})
}

// standaloneURL 是 collectLiterals 专用判定，比 urlLike 更严：
// 必须能从站点根或绝对地址锚定。
//
// 放宽到 urlLike 会把路由配置里的相对子路由（path:"user/:id"）当成地址，
// 解析后变成 /static/js/user/:id 这种不存在的路径。
func standaloneURL(s string) bool {
	if !urlLike(s) {
		return false
	}
	for _, p := range []string{"/", "http://", "https://", "./", "../"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// urlishMember 判断赋值左侧是否具有 URL 语义。
func urlishMember(left ast.Expression) bool {
	var name string
	switch v := left.(type) {
	case *ast.DotExpression:
		name = v.Identifier.Name.String()
	case *ast.BracketExpression:
		if sl, ok := v.Member.(*ast.StringLiteral); ok {
			name = sl.Value.String()
		}
	default:
		return false
	}
	switch strings.ToLower(name) {
	case "href", "src", "action", "url", "uri", "path", "pathname",
		"location", "redirect", "endpoint", "link", "target", "data":
		return true
	}
	return false
}

// methodOf 从调用点推断 HTTP 方法。
func (e *extractor) methodOf(obj, name string, args []ast.Expression) string {
	lower := strings.ToLower(name)

	// 已知 HTTP 客户端对象上的动词方法：axios.post、$.get、http.delete ...
	if httpClients[obj] {
		if m, ok := httpVerbs[lower]; ok {
			return m
		}
	}

	// 压缩后的调用点：webpack/terser 会把 axios 压成 n，对象名不再可信。
	// 此时退化为「属性名是 HTTP 动词 + 首参确实像地址」才判定，
	// 避免把 store.get("token") 这类普通读取误判成请求。
	if m, ok := httpVerbs[lower]; ok && len(args) > 0 {
		if raw, _ := e.eval(args[0]); urlLike(raw) {
			return m
		}
	}

	switch lower {
	case "fetch":
		if len(args) >= 2 {
			if v := e.objField(args[1], "method"); v != nil {
				if s, ok := v.(*ast.StringLiteral); ok {
					return strings.ToUpper(s.Value.String())
				}
			}
		}
		return "GET" // fetch 默认 GET
	case "sendbeacon":
		return "POST"
	case "ajax":
		if len(args) >= 1 {
			if v := e.objField(args[0], "type"); v != nil {
				if s, ok := v.(*ast.StringLiteral); ok {
					return strings.ToUpper(s.Value.String())
				}
			}
			if v := e.objField(args[0], "method"); v != nil {
				if s, ok := v.(*ast.StringLiteral); ok {
					return strings.ToUpper(s.Value.String())
				}
			}
		}
	}
	return ""
}

// paramsOf 从调用参数中提取 query / body 参数名。
func (e *extractor) paramsOf(obj, name string, args []ast.Expression) (query, body []string) {
	if len(args) < 2 {
		return nil, nil
	}
	second := args[1]
	lower := strings.ToLower(name)

	if httpClients[obj] {
		switch lower {
		case "get", "delete", "head", "options":
			// axios.get(url, {params:{a,b}}) / $.get(url, {a,b})
			if p := e.objField(second, "params"); p != nil {
				query = e.objKeys(p)
			} else if obj == "$" || obj == "jQuery" {
				query = e.objKeys(second)
			}
		case "post", "put", "patch":
			// axios.post(url, {a,b})
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

// objKeys 取对象字面量的键名。
func (e *extractor) objKeys(x ast.Expression) []string {
	ol, ok := x.(*ast.ObjectLiteral)
	if !ok {
		return nil
	}
	var keys []string
	for _, p := range ol.Value {
		switch v := p.(type) {
		case *ast.PropertyKeyed:
			if k := keyName(v.Key); k != "" {
				keys = jsext.AddParam(keys, k)
			}
		case *ast.PropertyShort:
			keys = jsext.AddParam(keys, v.Name.Name.String())
		}
	}
	return keys
}

// objField 取对象字面量中指定字段的值。
func (e *extractor) objField(x ast.Expression, field string) ast.Expression {
	ol, ok := x.(*ast.ObjectLiteral)
	if !ok {
		return nil
	}
	for _, p := range ol.Value {
		if kv, ok := p.(*ast.PropertyKeyed); ok && keyName(kv.Key) == field {
			return kv.Value
		}
	}
	return nil
}

// eval 做部分求值：字面量与已知常量拼接，未知节点记为 EXPR。
func (e *extractor) eval(n ast.Expression) (string, bool) {
	switch v := n.(type) {
	case *ast.StringLiteral:
		return v.Value.String(), true
	case *ast.Identifier:
		if s, ok := e.vars[v.Name.String()]; ok {
			return s, true
		}
	case *ast.BinaryExpression:
		if v.Operator == token.PLUS {
			l, lok := e.eval(v.Left)
			r, rok := e.eval(v.Right)
			return l + r, lok && rok
		}
	case *ast.TemplateLiteral:
		// ``${base}/orders/${id}``：Vue 3 / Vite / 现代 axios 里极常见。
		// Elements 比 Expressions 多一个（首尾各一个纯文本段），逐段拼。
		var sb strings.Builder
		certain := true
		for i, el := range v.Elements {
			if el != nil {
				sb.WriteString(el.Literal)
			}
			if i < len(v.Expressions) {
				v2, ok := e.eval(v.Expressions[i])
				if !ok {
					certain = false
				}
				sb.WriteString(v2)
			}
		}
		return sb.String(), certain
	}
	return "EXPR", false
}

// calleeOf 拆出调用接收者与方法名：axios.post -> ("axios","post")，fetch -> ("","fetch")。
func calleeOf(callee ast.Expression) (obj, name string) {
	switch c := callee.(type) {
	case *ast.Identifier:
		return "", c.Name.String()
	case *ast.DotExpression:
		if id, ok := c.Left.(*ast.Identifier); ok {
			return id.Name.String(), c.Identifier.Name.String()
		}
		return "", c.Identifier.Name.String()
	}
	return "", ""
}

func keyName(x ast.Expression) string {
	switch k := x.(type) {
	case *ast.Identifier:
		return k.Name.String()
	case *ast.StringLiteral:
		return k.Value.String()
	}
	return ""
}

// sourceMapRe 匹配 //# sourceMappingURL=app.js.map 这类注释。
var sourceMapRe = regexp.MustCompile(`[#@]\s*sourceMappingURL=(\S+)`)

var (
	importFromRe  = regexp.MustCompile(`(?m)^\s*import\s+[^;\n]*?from\s*['"][^'"\n]+['"]\s*;?`)
	importBareRe  = regexp.MustCompile(`(?m)^\s*import\s*['"][^'"\n]+['"]\s*;?`)
	exportHeadRe  = regexp.MustCompile(`(?m)^\s*export\s+(?:default\s+)?`)
	dynamicImport = regexp.MustCompile(`\bimport\s*\(`)
)

// StripModuleSyntax 去掉 goja 不认识的 ESM 语法，返回改写后的源码与是否发生改动。
//
// 处理三类：`import x from "y"`、`export ...`、`import(...)`（改名为 __import）。
// 只删除声明与关键字，不动字符串字面量与运算符，因此后续求值结果不变。
func StripModuleSyntax(src []byte) ([]byte, bool) {
	orig := string(src)
	out := importFromRe.ReplaceAllString(orig, "")
	out = importBareRe.ReplaceAllString(out, "")
	out = exportHeadRe.ReplaceAllString(out, "")
	out = dynamicImport.ReplaceAllString(out, "__import(")
	if out == orig {
		return src, false
	}
	return []byte(out), true
}

// staticImportRe 匹配 `import x from "y"` / `import "y"` 的模块路径。
var staticImportRe = regexp.MustCompile(`(?m)^\s*import\s+(?:[^;'"\n]*?from\s*)?['"]([^'"\n]+)['"]`)

// collectStaticImports 提取静态 import 的模块路径。
// 这一步必须针对原始源码做——StripModuleSyntax 已经把这些语句整行删掉了。
func (e *extractor) collectStaticImports(src []byte) {
	for _, m := range staticImportRe.FindAllSubmatch(src, -1) {
		if raw := string(m[1]); urlLike(raw) {
			e.out = append(e.out, jsext.Request{URL: raw, Confidence: jsext.Certain})
		}
	}
}

// collectComments 提取注释里的 sourceMappingURL。
func (e *extractor) collectComments(src []byte) {
	for _, m := range sourceMapRe.FindAllSubmatch(src, -1) {
		if raw := string(m[1]); urlLike(raw) {
			e.out = append(e.out, jsext.Request{URL: raw, Confidence: jsext.Certain})
		}
	}
}

// urlLike 判断字符串是否像一个可采集的地址，过滤掉文案、类名等噪音。
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
	// 无前导斜杠时要求含 "/" 或形如 a.b 的扩展名
	return strings.Contains(s, "/") || strings.Contains(s, ".")
}

// walk 反射遍历 AST（goja 未导出 Walker）。
func walk(n any, fn func(any)) {
	if n == nil {
		return
	}
	v := reflect.ValueOf(n)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		fn(n)
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		walkField(v.Field(i), fn)
	}
}

func walkField(f reflect.Value, fn func(any)) {
	switch f.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !f.IsNil() {
			walk(f.Interface(), fn)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < f.Len(); i++ {
			walkField(f.Index(i), fn)
		}
	}
}
