package routegraph

import "strings"

// Route 是从前端路由配置里恢复出来的一条路由。
//
// 注意：前端路由不等于服务端资源。SPA 的 /admin/users 在服务器上
// 往往只是同一个 index.html，所以调用方应把 Kind=route 的结果
// 与真实抓取到的 URL 区分对待。
type Route struct {
	// Path 路由路径，可能含 :id 这类参数占位。
	Path string
	// Name 路由名（存在时才有）。
	Name string
	// Component 指向的组件名或模块路径（存在时才有）。
	Component string
	// Source 命中的识别点，如 route-table。
	Source string
}

// routeSiblings 说明「这是一个路由对象」需要同时出现的兄弟键。
// 只认 path 会把普通配置对象也当成路由，加上这些键能大幅收敛。
var routeSiblings = []string{"component", "components", "element", "children", "redirect", "meta", "loader", "lazy"}

// routeTables 用括号栈扫出所有形如 {path: "...", component: ...} 的对象。
//
// 不用 AST 的原因：这里要的是「结构形状」而不是语法树，
// 栈扫描对压缩、嵌套、以及 ESM/CJS 两种产物都一致，实现也只要几十行。
func (tl *tokenList) routeTables() []Route {
	type frame struct {
		keys   []string
		values map[string]string
	}
	var stack []frame
	var pendingKey string
	var out []Route
	seen := map[string]struct{}{}
	var prev Token
	hasPrev := false

	for i := 0; i < len(tl.toks); i++ {
		t := tl.toks[i]
		if t.Kind == TokPunct {
			switch tl.text(i) {
			case "{":
				if len(stack) < 512 {
					stack = append(stack, frame{values: map[string]string{}})
				}
				pendingKey = ""
			case "}":
				if n := len(stack); n > 0 {
					f := stack[n-1]
					stack = stack[:n-1]
					p, ok := f.values["path"]
					if ok && hasRouteSibling(f.keys) && validRoutePath(p) {
						if _, dup := seen[p]; !dup {
							seen[p] = struct{}{}
							out = append(out, Route{
								Path:      p,
								Name:      f.values["name"],
								Component: componentOf(f.values),
								Source:    "route-table",
							})
						}
					}
				}
				pendingKey = ""
			case ":":
				if n := len(stack); n > 0 && hasPrev {
					pendingKey = keyName(prev, tl.src)
					if pendingKey != "" && len(stack[n-1].keys) < 64 {
						stack[n-1].keys = append(stack[n-1].keys, pendingKey)
					}
				}
			}
			prev, hasPrev = t, true
			continue
		}

		if pendingKey != "" && len(stack) > 0 {
			switch t.Kind {
			case TokString, TokTemplate:
				if len(stack[len(stack)-1].values) < 64 {
					stack[len(stack)-1].values[pendingKey] = t.Value(tl.src)
				}
				pendingKey = ""
			case TokIdent:
				// component: Admin —— 标识符形式也记下来，便于知道指向哪个模块
				switch pendingKey {
				case "component", "name", "element", "lazy":
					if len(stack[len(stack)-1].values) < 64 {
						stack[len(stack)-1].values[pendingKey] = t.Text(tl.src)
					}
					pendingKey = ""
				}
			}
		}
		prev, hasPrev = t, true
	}
	return out
}

// keyName 把 `:` 之前的 token 归一成键名（去引号、去大小写差异）。
func keyName(t Token, src []byte) string {
	switch t.Kind {
	case TokIdent, TokString, TokTemplate:
		v := strings.TrimSpace(t.Value(src))
		if len(v) == 0 || len(v) > 32 {
			return ""
		}
		return strings.ToLower(v)
	}
	return ""
}

func hasRouteSibling(keys []string) bool {
	for _, k := range keys {
		for _, want := range routeSiblings {
			if k == want {
				return true
			}
		}
	}
	return false
}

func componentOf(values map[string]string) string {
	for _, k := range []string{"component", "element", "lazy"} {
		if v := values[k]; v != "" && len(v) < 128 {
			return v
		}
	}
	return ""
}

// validRoutePath 过滤掉明显不是路由的字符串。
func validRoutePath(p string) bool {
	if len(p) == 0 || len(p) > 200 {
		return false
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "*") {
		return false
	}
	if strings.ContainsAny(p, " \t\n\r\"'<>\\") {
		return false
	}
	// 纯通配/根路径也保留，排除全是占位符的
	if strings.Trim(p, "/*:.") == "" && p != "/" {
		return false
	}
	return true
}
