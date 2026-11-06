package extractor

import "testing"

// TestFromJSON 验证从响应体里提取「下一跳」。
//
// 这类地址既不在 HTML 里也不在 JS 源码里，只出现在 JSON 响应中，
// 静态提取完全看不到——之前最明显的召回缺口之一。
func TestFromJSON(t *testing.T) {
	body := []byte(`{
	  "next": "/api/orders?page=2",
	  "links": {"self": "/api/items/1", "avatar": "/static/u/1.png"},
	  "items": [{"href": "https://cdn.example.com/a.js"}],
	  "id": "abc-123",
	  "name": "张三",
	  "flag": true,
	  "empty": "/"
	}`)

	list := FromJSON(body)
	got := map[string]bool{}
	for _, r := range list {
		got[r.Raw] = true
	}

	for _, want := range []string{
		"/api/orders?page=2", "/api/items/1", "/static/u/1.png",
		"https://cdn.example.com/a.js",
	} {
		if !got[want] {
			t.Errorf("缺少 %s，实际: %v", want, got)
		}
	}
	// 普通字符串不能被当成地址
	for _, bad := range []string{"abc-123", "张三"} {
		if got[bad] {
			t.Errorf("%s 不应被当成地址", bad)
		}
	}
}

// TestFromJSONInvalidBody 保证非 JSON 输入不会 panic 也不会产出。
func TestFromJSONInvalidBody(t *testing.T) {
	for _, in := range []string{"", "<html>", "not json"} {
		if list := FromJSON([]byte(in)); len(list) != 0 {
			t.Errorf("输入 %q 产出 %d 条，期望 0", in, len(list))
		}
	}
}
