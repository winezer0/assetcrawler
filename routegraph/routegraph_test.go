package routegraph

import (
	"strings"
	"testing"
)

// TestWebpackChunkMap 验证 chunk 文件名按同一 key 对齐还原。
// 这是整个静态层最核心的一条：如果把 {...}[e] 拆开单独换，
// 会产出 chunk-101.d4e5f6.js 这种不存在的组合。
func TestWebpackChunkMap(t *testing.T) {
	src := `o.p="/static/js/",o.u=function(e){return"chunk-"+e+"."+{101:"a1b2c3",102:"d4e5f6",103:"9f8e7d"}[e]+".js"}`
	res := Extract([]byte(src))

	want := map[string]string{
		"/static/js/chunk-101.a1b2c3.js": "101",
		"/static/js/chunk-102.d4e5f6.js": "102",
		"/static/js/chunk-103.9f8e7d.js": "103",
	}
	got := map[string]string{}
	for _, c := range res.Chunks {
		got[c.Path] = c.ID
	}
	for p, id := range want {
		if got[p] != id {
			t.Errorf("缺少 chunk %s（ID=%s），实际: %v", p, id, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("chunk 数 = %d，期望 %d: %v", len(got), len(want), got)
	}
	// 不能出现交叉组合
	for p := range got {
		if strings.Contains(p, "101.d4e5f6") || strings.Contains(p, "102.a1b2c3") {
			t.Errorf("出现错配的 chunk: %s", p)
		}
	}
}

// TestWebpackChunkTemplate 覆盖模板字符串写法。
func TestWebpackChunkTemplate(t *testing.T) {
	src := "a.p=\"/static/\";a.u=e=>`js/" + `${e}` + ".chunk.js`"
	res := Extract([]byte(src))
	for _, c := range res.Chunks {
		if strings.Contains(c.Path, "chunk-") {
			continue
		}
		if !strings.HasSuffix(c.Path, ".js") {
			t.Errorf("chunk 路径异常: %s", c.Path)
		}
	}
}

// TestViteMapDeps 验证 Vite 内联依赖表。
func TestViteMapDeps(t *testing.T) {
	src := `const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["assets/Admin.a1b2c3.js","assets/Login.d4e5f6.js","assets/index.7f8e9d.css"])))=>i.map(i=>d[i]);`
	res := Extract([]byte(src))

	got := map[string]bool{}
	for _, c := range res.Chunks {
		got[c.Path] = true
	}
	for _, want := range []string{"assets/Admin.a1b2c3.js", "assets/Login.d4e5f6.js", "assets/index.7f8e9d.css"} {
		if !got[want] {
			t.Errorf("缺少 vite chunk %s，实际: %v", want, got)
		}
	}
}

// TestDynamicImport 验证动态 import 的模块说明符。
func TestDynamicImport(t *testing.T) {
	src := `var m = () => import("./chunk-report.js"); var n = import("./pages/Admin.mjs");`
	res := Extract([]byte(src))
	got := map[string]bool{}
	for _, c := range res.Chunks {
		got[c.Path] = true
	}
	for _, want := range []string{"./chunk-report.js", "./pages/Admin.mjs"} {
		if !got[want] {
			t.Errorf("缺少动态 import %s，实际: %v", want, got)
		}
	}
}

// TestRouteTable 验证路由表提取（含嵌套 children）。
func TestRouteTable(t *testing.T) {
	src := `var routes=[
	  {path:"/",name:"home",component:function(){return 1}},
	  {path:"/admin/users/:id",name:"user",component:()=>import("./U.js"),meta:{title:"u"}},
	  {path:"/parent",children:[{path:"/parent/child",name:"c",component:X}]},
	  {path:"/login",name:"login",component:L},
	  {notAPath:"/should/not/appear",name:"x",component:Y}
	];`
	res := Extract([]byte(src))

	got := map[string]Route{}
	for _, r := range res.Routes {
		got[r.Path] = r
	}
	for _, want := range []string{"/", "/admin/users/:id", "/parent", "/parent/child", "/login"} {
		if _, ok := got[want]; !ok {
			t.Errorf("缺少路由 %s，实际: %v", want, keysOf(got))
		}
	}
	if _, ok := got["/should/not/appear"]; ok {
		t.Error("只有 notAPath 的对象不应被当成路由")
	}
	if r := got["/admin/users/:id"]; r.Name != "user" {
		t.Errorf("路由名 = %q，期望 user", r.Name)
	}
}

// TestCommentManifestIgnored 说明注释里的清单不是活的。
func TestCommentManifestIgnored(t *testing.T) {
	src := "// o.u=function(e){return\"chunk-\"+e+\".\"+{1:\"dead\"}[e]+\".js\"}\n" +
		"/* a.u=e=>{return \"x-\"+e+\".\"+{2:\"beef\"}[e]+\".js\"} */\n" +
		`real.p="/static/"` + "\n"
	res := Extract([]byte(src))
	for _, c := range res.Chunks {
		if strings.Contains(c.Path, "dead") || strings.Contains(c.Path, "beef") {
			t.Errorf("注释里的清单被当成真实的: %s", c.Path)
		}
	}
}

// TestRegexDoesNotBreakStack 保证正则字面量里的花括号不会打乱括号栈。
func TestRegexDoesNotBreakStack(t *testing.T) {
	src := `var re=/{[^}]+}/g; var routes=[{path:"/a",name:"a",component:X}];`
	res := Extract([]byte(src))
	found := false
	for _, r := range res.Routes {
		if r.Path == "/a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("正则字面量干扰了路由扫描: %+v", res.Routes)
	}
}

// TestLooksLikeBundle 守住粗筛：无关脚本不该进入词法分析。
func TestLooksLikeBundle(t *testing.T) {
	if LooksLikeBundle([]byte("var a=1;console.log(a);")) {
		t.Error("普通脚本被判定为 bundle")
	}
	if !LooksLikeBundle([]byte(`o.u=function(e){return"c-"+e+"."+{1:"a"}[e]+".js"};__webpack_require__`)) {
		t.Error("webpack 运行时未被识别")
	}
}

func keysOf(m map[string]Route) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
