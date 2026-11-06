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

// keyClass 是键的归类。只区分「我们需要保存的值」与「能证明这是路由对象的兄弟键」，
// 不必保留原始键名，因此帧里不需要 map。
type keyClass int

const (
	keyOther keyClass = iota
	keyPath
	keyName
	keyComponent
	keySibling
)

// classifyKey 把 `:` 前的 token 归类。
// 只认 path 会把普通配置对象也当成路由，加上兄弟键能大幅收敛。
func classifyKey(t Token, src []byte) keyClass {
	switch strings.ToLower(strings.TrimSpace(t.Value(src))) {
	case "path":
		return keyPath
	case "name":
		return keyName
	case "component", "element", "lazy":
		return keyComponent
	case "components", "children", "redirect", "meta", "loader":
		return keySibling
	}
	return keyOther
}

// routeTables 用括号栈扫出所有形如 {path: "...", component: ...} 的对象。
//
// 不用 AST 的原因：这里要的是「结构形状」而不是语法树，
// 栈扫描对压缩、嵌套、以及 ESM/CJS 两种产物都一致，实现也只要几十行。
//
// 帧只用固定字段，不用 map + key 切片：后者会给每个 `{` 分配一个 map 和若干切片，
// 实测占掉剩余分配的大头（1MB 源码 21MB）。我们只需要 path / name / component
// 三个值，外加「有没有出现过路由兄弟键」这一个布尔量。
func (tl *tokenList) routeTables() []Route {
	type frame struct {
		hasPath    bool
		path       string
		hasSibling bool
		name       string
		component  string
	}
	var stack []frame
	var pending keyClass
	var out []Route
	seen := map[string]struct{}{}
	var prev Token
	hasPrev := false

	for i := 0; i < len(tl.toks); i++ {
		t := tl.toks[i]
		if t.Kind == TokPunct {
			switch {
			case tl.is(i, "{"):
				if len(stack) < 512 {
					stack = append(stack, frame{})
				}
				pending = keyOther
			case tl.is(i, "}"):
				if n := len(stack); n > 0 {
					f := stack[n-1]
					stack = stack[:n-1]
					if f.hasPath && f.hasSibling && validRoutePath(f.path) {
						if _, dup := seen[f.path]; !dup {
							seen[f.path] = struct{}{}
							out = append(out, Route{
								Path:      f.path,
								Name:      f.name,
								Component: f.component,
								Source:    "route-table",
							})
						}
					}
				}
				pending = keyOther
			case tl.is(i, ":"):
				if len(stack) > 0 && hasPrev {
					pending = classifyKey(prev, tl.src)
					// 兄弟键在「见到键名」时就足以判定这是路由对象，
					// 不能等它的值：children/redirect 的值是数组或对象，
					// 值 token 不是字符串，等下去会漏掉。
					switch pending {
					case keySibling:
						stack[len(stack)-1].hasSibling = true
						pending = keyOther
					case keyComponent:
						stack[len(stack)-1].hasSibling = true
					}
				}
			}
			prev, hasPrev = t, true
			continue
		}

		if pending != keyOther && len(stack) > 0 {
			f := &stack[len(stack)-1]
			var val string
			ok := false
			switch t.Kind {
			case TokString, TokTemplate:
				val, ok = t.Value(tl.src), true
			case TokIdent:
				// component: Admin —— 标识符形式也记下来，便于知道指向哪个模块
				if pending == keyComponent || pending == keyName {
					val, ok = t.Text(tl.src), true
				}
			}
			if ok {
				switch pending {
				case keyPath:
					f.hasPath, f.path = true, val
				case keyName:
					f.name = val
				case keyComponent:
					// component/element/lazy 既是「要保存的值」，也是「这是路由对象」的证据
					f.hasSibling = true
					if f.component == "" {
						f.component = val
					}
				case keySibling:
					f.hasSibling = true
				}
				pending = keyOther
			}
		}
		prev, hasPrev = t, true
	}
	return out
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
