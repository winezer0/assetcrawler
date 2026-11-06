package extractor

import "testing"

// TestFormParamValues 锁定「值可静态确定」的取值收集口径。
//
// hidden 的 value 是确定默认值，select/radio/checkbox 的候选集合写在 HTML 里，
// 这些不需要发请求就能得到。而文本框、空值、浏览器默认值（on）、提交按钮
// 都不携带信息，必须排除。
func TestFormParamValues(t *testing.T) {
	const body = `<html><body>
<form action="/search" method="get">
  <input name="q">
  <input type="hidden" name="cat" value="12">
  <input type="hidden" name="csrf" value="">
  <select name="sort">
    <option value="price_asc">价格</option>
    <option value="newest">最新</option>
    <option value="">全部</option>
  </select>
  <input type="radio" name="type" value="sale">
  <input type="radio" name="type" value="rent">
  <input type="checkbox" name="flags" value="on">
  <input type="checkbox" name="tags" value="a">
  <input type="checkbox" name="tags" value="b">
  <input type="submit" name="go" value="Go">
</form>
</body></html>`

	var got []ParamValue
	for _, r := range FromHTML(parseHTML(t, body), DefaultOptions()) {
		if r.Attribute == "action" {
			got = r.ParamValues
		}
	}
	if len(got) == 0 {
		t.Fatal("未收集到任何参数取值")
	}

	want := map[string]struct {
		kind   string
		values []string
	}{
		"cat":  {"hidden", []string{"12"}},
		"sort": {"select", []string{"price_asc", "newest"}},
		"type": {"radio", []string{"sale", "rent"}},
		"tags": {"checkbox", []string{"a", "b"}},
	}
	if len(got) != len(want) {
		t.Errorf("期望 %d 个参数，实际 %d 个：%+v", len(want), len(got), got)
	}
	for _, p := range got {
		w, ok := want[p.Name]
		if !ok {
			t.Errorf("不该收集参数 %q：%+v", p.Name, p)
			continue
		}
		if p.Kind != w.kind {
			t.Errorf("%s 的来源期望 %s，实际 %s", p.Name, w.kind, p.Kind)
		}
		if len(p.Values) != len(w.values) {
			t.Errorf("%s 的取值期望 %v，实际 %v", p.Name, w.values, p.Values)
			continue
		}
		for i := range w.values {
			if p.Values[i] != w.values[i] {
				t.Errorf("%s 的取值期望 %v，实际 %v", p.Name, w.values, p.Values)
			}
		}
	}
}
