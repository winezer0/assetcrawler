package assetcrawler

import (
	"strings"

	"assetcrawler/extractor"
	"assetcrawler/jsext"
	"assetcrawler/jsext/goja"
	"assetcrawler/jsext/treesitter"
)

// JSEngine 选择 JS 提取引擎。
//
// 各实现的差异（实测）：
//   - regex      零依赖正则，最快，但拿不到拼接上下文、方法与参数
//   - goja       AST 解析，约 50 MB/s，支持 ES2020，**不支持 ESM 的 import/export**
//     及 import.meta、顶层 await；遇到即整文件解析失败
//   - treesitter 纯 Go tree-sitter，语法覆盖最全（含 ESM）且容错，
//     但慢约 1.8 倍、二进制多约 7.7MB
//
// 默认用 auto：goja 快路径优先，失败时用 treesitter 兜底。
// 这样既不牺牲常规脚本的速度，也不会在 Vite/ESM 产物上整文件放弃。
type JSEngine string

const (
	// JSEngineRegex 只用正则，不引入任何第三方依赖。
	JSEngineRegex JSEngine = "regex"
	// JSEngineAuto goja 优先，解析失败时回退 treesitter（默认）。
	JSEngineAuto JSEngine = "auto"
	// JSEngineGoja 强制只用 dop251/goja 的解析器。
	JSEngineGoja JSEngine = "goja"
	// JSEngineTreesitter 强制只用 odvcencio/gotreesitter。
	JSEngineTreesitter JSEngine = "treesitter"
)

// engineChain 按名称返回引擎链（按优先级排列），空链表示只用正则。
func engineChain(name JSEngine) []jsext.Engine {
	switch name {
	case JSEngineGoja:
		return []jsext.Engine{goja.Extract}
	case JSEngineTreesitter:
		return []jsext.Engine{treesitter.Extract}
	case JSEngineRegex:
		return nil
	default:
		// 含 JSEngineAuto 与零值：零值也要拿到默认能力，
		// 否则库使用者不显式设置时就退化成正则了。
		return []jsext.Engine{goja.Extract, treesitter.Extract}
	}
}

// adaptEngine 把引擎链适配成 extractor 需要的注入函数。
//
// 链上任何一个引擎解析成功即采用其结果；全部失败才返回 ok=false，
// 由调用方回退正则。串行的代价可以忽略：goja 遇到 ESM 在第一个 token 就报错。
func adaptEngine(chain []jsext.Engine) func(string) ([]extractor.Request, bool) {
	if len(chain) == 0 {
		return nil
	}
	return func(src string) ([]extractor.Request, bool) {
		data := []byte(src)
		for _, fn := range chain {
			reqs, err := fn(data)
			if err != nil {
				continue
			}
			return toRequests(reqs), true
		}
		return nil, false
	}
}

// toRequests 把 jsext 的输出适配成 extractor.Request。
func toRequests(reqs []jsext.Request) []extractor.Request {
	out := make([]extractor.Request, 0, len(reqs))
	for _, r := range reqs {
		// 参数名单独带出：query 参数同时补进 URL（便于按「带参接口」判读），
		// body 参数只作为形状保留——POST 的字段拼到 query 上会改变语义。
		params := append([]string(nil), r.QueryParams...)
		for _, b := range r.BodyParams {
			dup := false
			for _, q := range params {
				if q == b {
					dup = true
					break
				}
			}
			if !dup {
				params = append(params, b)
			}
		}
		out = append(out, extractor.Request{
			Raw:        withParams(r.URL, r.QueryParams),
			Method:     r.Method,
			Source:     "js-ast",
			Confidence: r.Confidence,
			Params:     params,
		})
	}
	return out
}

// withParams 把只有参数名、没有值的 query 补到 URL 上。
//
// AST 能从 `$.get("/api/list", {page:1})` 拿到参数名 page，但拿不到它的取值；
// 补成 `?page=` 是为了让下游把它当作「带参数的接口」保留下来，
// 而不是丢成一个无参路径。已有同名参数时不重复追加。
func withParams(raw string, params []string) string {
	if len(params) == 0 {
		return raw
	}
	query := ""
	if i := strings.Index(raw, "?"); i >= 0 {
		query = raw[i+1:]
	}
	var missing []string
	for _, p := range params {
		if p == "" || strings.Contains(query, p+"=") {
			continue
		}
		missing = append(missing, p)
	}
	if len(missing) == 0 {
		return raw
	}
	sep := "?"
	if query != "" {
		sep = "&"
	}
	var kv []string
	for _, p := range missing {
		kv = append(kv, p+"=")
	}
	return raw + sep + strings.Join(kv, "&")
}
