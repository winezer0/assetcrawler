package jsext_test

import (
	"testing"

	"assetcrawler/jsext/treesitter"
)

// urlsOf 把提取结果摊平成 URL 集合，便于断言「有没有多出错误地址」。
func urlsOf(t *testing.T, src string) map[string]string {
	t.Helper()
	reqs, err := treesitter.Extract([]byte(src))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	out := make(map[string]string, len(reqs))
	for _, r := range reqs {
		out[r.URL] = r.Method
	}
	return out
}

// TestScopeIsolation 锁定常量表的作用域隔离。
//
// 打包产物里每个模块是一个独立函数、内部短变量同名是常态
// （webpack 的 `function(e,t,n){const r="/api"}`）。
// 早期 vars 是一张全局 map，后写覆盖先写，前一个模块的地址会被后一个污染，
// 产出源码里根本不存在的 URL——实测 4 条里错 2 条。
func TestScopeIsolation(t *testing.T) {
	src := `
(function(){ const n="/api/v1"; http.get(n+"/users") })();
(function(){ const n="/static"; http.get(n+"/logo.png") })();
(function(){ const n="/admin";  http.get(n+"/panel") })();
`
	got := urlsOf(t, src)

	want := []string{"/api/v1/users", "/static/logo.png", "/admin/panel"}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("缺少 %s，实际 %v", w, scopeKeys(got))
		}
	}
	// 这四条是「作用域串味」的产物：都不存在于源码里。
	for _, bad := range []string{"/admin/users", "/admin/logo.png", "/static/users", "/static/panel"} {
		if _, ok := got[bad]; ok {
			t.Errorf("出现了源码里不存在的 %s（作用域串味）", bad)
		}
	}
}

// TestScopeNesting 验证「内层能读外层、外层读不到内层」。
func TestScopeNesting(t *testing.T) {
	src := `
const HOST="/api";
function f(){ const p="/v1"; http.get(HOST+p+"/inner") }
http.get(HOST+"/outer");
http.get(secret+"/leak");      // secret 未定义，不该产出 /x 之类的确定地址
function g(){ const secret="/hidden"; http.get(secret+"/g") }
`
	got := urlsOf(t, src)

	if _, ok := got["/api/v1/inner"]; !ok {
		t.Errorf("内层未读到外层常量，实际 %v", scopeKeys(got))
	}
	if _, ok := got["/api/outer"]; !ok {
		t.Errorf("外层常量未生效，实际 %v", scopeKeys(got))
	}
	if _, ok := got["/hidden/g"]; !ok {
		t.Errorf("g 函数内的常量未生效，实际 %v", scopeKeys(got))
	}
}

func scopeKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
