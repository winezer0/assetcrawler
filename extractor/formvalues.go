package extractor

import (
	"strings"

	"golang.org/x/net/html"
)

// 表单参数的「值」在多数情况下是客户端生成或交互得到的，静态拿不到。
// 但有一批是例外：hidden 的 value 就是确定默认值，select/radio/checkbox
// 的候选集合本身写在 HTML 里。这些不需要发任何请求就能得到，
// 对「这个接口能带什么参数、取什么值」很有用。
//
// 取舍：只输出候选值，不合成 URL。合成 URL 意味着额外请求，
// 而 hidden 值常带 csrf token / 会话态，拼出来的地址必然失败。

// ParamValue 是一个参数及其可静态确定的候选值。
type ParamValue struct {
	// Name 参数名。
	Name string `json:"name"`
	// Values 候选取值，按 HTML 中出现顺序、已去重。
	Values []string `json:"values"`
	// Kind 取值来源：hidden / select / radio / checkbox。
	Kind string `json:"kind,omitempty"`
}

const (
	// maxFormParams 限制单个表单产出的参数个数。
	maxFormParams = 32
	// maxParamValues 限制单个参数的候选值个数。
	maxParamValues = 20
	// maxParamValueLen 限制单个取值的长度，超长的多半是 base64 或序列化数据。
	maxParamValueLen = 256
)

// paramValueKinds 是允许收集取值的控件类型 → 取值来源标签。
var paramValueKinds = map[string]string{
	"hidden":   "hidden",
	"radio":    "radio",
	"checkbox": "checkbox",
}

// formParamValues 收集表单里可静态确定的参数取值。
//
// 返回值按出现顺序排列且已去重；只含「至少有一个可用值」的参数，
// 单纯的文本框（没有 value 或 value 为空）不会出现在结果里。
func formParamValues(form *html.Node) []ParamValue {
	var out []ParamValue
	idx := map[string]int{}
	var walk func(n *html.Node, depth int)
	walk = func(n *html.Node, depth int) {
		if n == nil || depth > 12 || len(out) >= maxFormParams {
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != html.ElementNode {
				continue
			}
			tag := strings.ToLower(c.Data)
			if tag == "form" && c != form {
				continue // 嵌套表单不属于当前表单
			}
			switch tag {
			case "input":
				addInputValue(c, idx, &out)
			case "select":
				addSelectValues(c, idx, &out)
			case "textarea":
				// 文本框的初始内容不是参数值（可编辑），不收集
			}
			walk(c, depth+1)
		}
	}
	walk(form, 0)
	return out
}

func addInputValue(n *html.Node, idx map[string]int, out *[]ParamValue) {
	if _, disabled := AttrVal(n, "disabled"); disabled {
		return
	}
	typ, _ := AttrVal(n, "type")
	kind, ok := paramValueKinds[strings.ToLower(strings.TrimSpace(typ))]
	if !ok {
		return
	}
	name, ok := AttrVal(n, "name")
	if !ok || !validParamName(name) {
		return
	}
	value, ok := AttrVal(n, "value")
	if !ok {
		return
	}
	appendParamValue(idx, out, strings.TrimSpace(name), kind, value)
}

func addSelectValues(n *html.Node, idx map[string]int, out *[]ParamValue) {
	name, ok := AttrVal(n, "name")
	if !ok || !validParamName(name) {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || strings.ToLower(c.Data) != "option" {
			continue
		}
		if _, disabled := AttrVal(c, "disabled"); disabled {
			continue
		}
		// 没有 value 属性时，浏览器提交的是选项文本
		v, ok := AttrVal(c, "value")
		if !ok {
			continue
		}
		appendParamValue(idx, out, strings.TrimSpace(name), "select", v)
	}
}

// appendParamValue 把取值并入对应参数，同名参数聚合（radio 组、多选）。
func appendParamValue(idx map[string]int, out *[]ParamValue, name, kind, raw string) {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > maxParamValueLen {
		return
	}
	if v == "on" {
		// checkbox 未显式给 value 时的浏览器默认值，不携带信息
		return
	}
	i, exists := idx[name]
	if !exists {
		if len(*out) >= maxFormParams {
			return
		}
		*out = append(*out, ParamValue{Name: name, Kind: kind, Values: []string{v}})
		idx[name] = len(*out) - 1
		return
	}
	p := &(*out)[i]
	if len(p.Values) >= maxParamValues {
		return
	}
	for _, existing := range p.Values {
		if existing == v {
			return
		}
	}
	p.Values = append(p.Values, v)
}

// validParamName 与参数名收集保持同一口径：不含分隔符与控制字符。
func validParamName(name string) bool {
	name = strings.TrimSpace(name)
	return name != "" && !strings.ContainsAny(name, " \t\n\r&=")
}
