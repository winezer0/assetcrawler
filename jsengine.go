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
// 三个实现共用同一套验收夹具，输出契约一致，可随时切换：
//   - regex      零依赖正则，最快，但拿不到拼接上下文、方法与参数
//   - goja       AST 解析，50 MB/s，支持 ES2020 但**不支持 ESM 的 import/export**
//   - treesitter 纯 Go tree-sitter，语法覆盖最全（含 ESM），但解析慢约 80 倍
type JSEngine string

const (
	// JSEngineRegex 只用正则，不引入任何第三方依赖（默认）。
	JSEngineRegex JSEngine = "regex"
	// JSEngineGoja 用 dop251/goja 的解析器。
	JSEngineGoja JSEngine = "goja"
	// JSEngineTreesitter 用 odvcencio/gotreesitter。
	JSEngineTreesitter JSEngine = "treesitter"
)

// engineFunc 按名称返回引擎实现，未知名称返回 nil（等价于只用正则）。
func engineFunc(name JSEngine) jsext.Engine {
	switch name {
	case JSEngineGoja:
		return goja.Extract
	case JSEngineTreesitter:
		return treesitter.Extract
	default:
		return nil
	}
}

// adaptEngine 把 jsext 的输出适配成 extractor.Request。
func adaptEngine(fn jsext.Engine) func(string) ([]extractor.Request, bool) {
	if fn == nil {
		return nil
	}
	return func(src string) ([]extractor.Request, bool) {
		reqs, err := fn([]byte(src))
		if err != nil {
			return nil, false // 解析失败，交给调用方回退正则
		}
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
		return out, true
	}
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
