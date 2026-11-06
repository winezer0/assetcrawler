package jsext_test

import "testing"

// TestConfigObjectPair 锁定对象属性值的取舍。
//
// 对象属性是最模糊的一类载体：`{url:"/api/orders"}` 是端点，
// 而 `{STATIC:"/static/"}`、`{VUE_APP_API:"/api/v3"}` 是可拼接前缀。
// 早期两者一视同仁，导致每个配置块贡献 1~3 条必然 404 的地址。
func TestConfigObjectPair(t *testing.T) {
	src := `
window.__ENV__ = { VUE_APP_API: "/api/v3", STATIC: "/static/" };
const conf = { url: "/api/orders", path: "/api/v2", endpoint: "/api/v9/items" };
`
	got := urlsOf(t, src)

	// 键名有地址语义 → 收（即使段数少）。
	for _, want := range []string{"/api/orders", "/api/v2", "/api/v9/items"} {
		if _, ok := got[want]; !ok {
			t.Errorf("缺少 %s，实际 %v", want, scopeKeys(got))
		}
	}
	// 键名无语义且形状是前缀 → 不收。
	for _, bad := range []string{"/api/v3", "/static/"} {
		if _, ok := got[bad]; ok {
			t.Errorf("误收前缀型配置值 %s", bad)
		}
	}
}
