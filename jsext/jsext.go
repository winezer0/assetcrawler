// Package jsext 定义从 JavaScript 源码中提取「请求候选」的统一类型。
//
// 包本身不含任何实现，只规定两个引擎（goja / gotreesitter）共同的输出契约，
// 便于在同一套验收夹具下横向对比。选择哪个引擎由上层编排决定。
//
// 设计约束：
//   - 不执行 JavaScript，只做静态解析与部分求值
//   - 输出保留「置信度」：能完全确定的 URL 与含占位符的模板必须区分开，
//     否则模板会被当成真实资产去抓取，制造 404
package jsext

// Request 是从 JS 源码中识别出的一个请求候选。
type Request struct {
	// URL 提取到的地址，可能是相对路径。
	URL string
	// Method 推断出的 HTTP 方法，空表示未知。
	Method string
	// QueryParams 从调用参数里推断出的 query 参数名（有序、去重）。
	QueryParams []string
	// BodyParams 从请求体对象里推断出的字段名。
	BodyParams []string
	// Confidence 见 Certain / Template。
	Confidence string
}

// 置信度取值。
const (
	// Certain 表示 URL 由字面量与已知常量拼接而成，可直接访问。
	Certain = "certain"
	// Template 表示 URL 中含有无法静态求值的占位符（EXPR）。
	Template = "template"
)

// Engine 是两个实现共同满足的签名。
type Engine func(src []byte) ([]Request, error)

// Dedup 按 URL 保序去重，保留首次出现的条目。
func Dedup(in []Request) []Request {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]Request, 0, len(in))
	for _, r := range in {
		if _, ok := seen[r.URL]; ok {
			continue
		}
		seen[r.URL] = struct{}{}
		out = append(out, r)
	}
	return out
}

// AddParam 向切片追加不重复的参数名。
func AddParam(dst []string, name string) []string {
	if name == "" {
		return dst
	}
	for _, s := range dst {
		if s == name {
			return dst
		}
	}
	return append(dst, name)
}
