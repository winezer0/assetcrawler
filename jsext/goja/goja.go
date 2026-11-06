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
	"errors"
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
	f, err := parser.ParseFile(nil, "", src, 0)
	if err != nil {
		// goja 的 parser 不支持 ESM 语法（import/export/import.meta/顶层 await）。
		//
		// 早期实现是「剥掉 import/export 再解析一次」，但那条路会静默出错：
		// `export default {a:"/x"}` 剥离后变成 `{a:"/x"}`，被当成代码块解析成功，
		// 于是既不报错、也提取不到任何东西——自动兜底链完全不会触发。
		// 因此这里明确拒绝，让上层（auto 链）交给 treesitter。
		if looksLikeESM(src) {
			return nil, ErrESMUnsupported
		}
		return nil, err
	}
	e := &extractor{vars: map[string]string{}, prefixVars: map[string]bool{}}
	e.collectConsts(f)
	e.collectPrefixVars(f)
	e.collectCalls(f)
	e.collectBinaries(f)
	e.collectLiterals(f)
	e.collectComments(src)
	return jsext.Dedup(e.out), nil
}

type extractor struct {
	vars map[string]string
	// prefixVars 记录「被当作拼接前缀使用」的变量名。
	// var api = "/api/v3" 若后续出现 api + "/x"，它就是前缀而不是端点；
	// 反之 var ep = "/api/hidden/endpoint" 从未参与拼接，本身就是一个地址。
	prefixVars map[string]bool
	out        []jsext.Request
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

// collectPrefixVars 记录被用作拼接前缀的变量。
//
// 判据是「出现在 + 的任一操作数位置，或出现在模板字符串的插值里」。
// 这样 `var API="/api/v3"; fetch(API+"/users")` 中的 API 不会被当成端点，
// 而 `var ep="/api/hidden/endpoint"`（从未参与拼接）会作为完整地址被收集。
func (e *extractor) collectPrefixVars(root ast.Node) {
	walk(root, func(n any) {
		switch v := n.(type) {
		case *ast.BinaryExpression:
			if v.Operator != token.PLUS {
				return
			}
			markIdent(e.prefixVars, v.Left)
			markIdent(e.prefixVars, v.Right)
		case *ast.TemplateLiteral:
			for _, ex := range v.Expressions {
				markIdent(e.prefixVars, ex)
			}
		}
	})
}

// endpointShaped 判断一个「裸变量赋值」是否更像完整端点而不是可拼接的前缀。
//
// 变量赋值是最模糊的一类载体：`var base="/api/v3"` 是前缀，
// `var ep="/api/hidden/endpoint"` 是端点，两者都是「以 / 开头的字符串」。
// 除了「是否参与 + 拼接」之外，再叠一层形状判断：
//   - 绝对地址、带 query/fragment → 端点
//   - 路径段数 >= 3              → 端点（/api/v3 这类前缀通常只有 1~2 段）
//   - 末段含扩展名（.js/.json…）  → 端点
func endpointShaped(s string) bool {
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	if strings.ContainsAny(s, "?#") {
		return true
	}
	trimmed := strings.Trim(s, "/")
	if segs := strings.Count(trimmed, "/") + 1; segs >= 3 {
		return true
	}
	last := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		last = trimmed[i+1:]
	}
	return strings.Contains(last, ".")
}

// markIdent 把表达式里的标识符记进集合。
func markIdent(set map[string]bool, x ast.Expression) {
	if id, ok := x.(*ast.Identifier); ok {
		set[id.Name.String()] = true
	}
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
		case *ast.VariableStatement:
			// var/let/const 直接赋一个完整地址（而非前缀）时也要收：
			// `var api = "/api/hidden/endpoint"` 只在正则时代能拿到，
			// AST 独占结果后就会漏。用 prefixVars 排除前缀型常量。
			for _, b := range v.List {
				id, ok := b.Target.(*ast.Identifier)
				if !ok || b.Initializer == nil {
					continue
				}
				if e.prefixVars[id.Name.String()] {
					continue
				}
				if v, certain := e.eval(b.Initializer); !certain || !endpointShaped(v) {
					continue
				}
				emit(b.Initializer)
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

// esmMarkerRe 识别 ESM 专有语法，用于把「goja 不支持」与「源码本身有错」区分开。
var esmMarkerRe = regexp.MustCompile(
	`(?m)(?:^|\n)\s*(?:import\s*[\w{*'"]|export\s+(?:default|const|let|var|function|class|\{|\*))|\bimport\s*\(|\bimport\.meta|(?:^|\n)\s*await\s`)

// ErrESMUnsupported 表示源码含 ESM 语法，goja 的解析器无法处理。
var ErrESMUnsupported = errors.New("goja 不支持 ESM 语法，请改用 treesitter 或 auto")

// looksLikeESM 判断源码是否含 ESM 专有语法。
func looksLikeESM(src []byte) bool { return esmMarkerRe.Match(src) }

// collectComments 提取注释里的 sourceMappingURL。
func (e *extractor) collectComments(src []byte) {
	for _, m := range sourceMapRe.FindAllSubmatch(src, -1) {
		if raw := string(m[1]); urlLike(raw) {
			e.out = append(e.out, jsext.Request{URL: raw, Confidence: jsext.Certain})
		}
	}
}

// urlLike 判断字符串是否像一个可采集的地址，过滤掉文案、类名等噪音。
// mimeRe 匹配 MIME 类型（text/plain、application/json）。
var mimeRe = regexp.MustCompile(`^[a-z][a-z0-9.+-]*/[a-z][a-z0-9.+-]*$`)

// linkRelWords 是 link 的 rel 取值，常作为字符串常量出现在 JS 里。
var linkRelWords = map[string]bool{
	"preload": true, "modulepreload": true, "prefetch": true, "preconnect": true,
	"dns-prefetch": true, "stylesheet": true, "canonical": true, "alternate": true,
	"icon": true, "manifest": true, "noopener": true, "noreferrer": true,
}

// nonURLLiteral 判断字面量是否属于「像路径但其实不是地址」的常见形态。
//
// 实测在 Vite 站点的 JS 里，`"text/plain"`、`"modulepreload"`、`"Running..."`
// 都会被当成候选，解析成 /assets/text/plain 这类必然 404 的地址。
func nonURLLiteral(s string) bool {
	if mimeRe.MatchString(s) {
		return true
	}
	if linkRelWords[strings.ToLower(s)] {
		return true
	}
	if strings.Contains(s, "...") { // 省略号，UI 文案
		return true
	}
	if strings.ContainsAny(s, "*") { // 通配（含正则片段 /* ）
		return true
	}
	return false
}

func urlLike(s string) bool {
	if s == "" || len(s) > 2048 || nonURLLiteral(s) {
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
	// 无前导斜杠时要求含 "/" 或形如 a.b 的扩展名。
	// 以 "." 开头的字符串一律不认：那是 CSS 选择器（`.blocks`）、
	// 相对模块（已在上面的分支处理）或省略号，都不是地址。
	if strings.HasPrefix(s, ".") {
		return false
	}
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
