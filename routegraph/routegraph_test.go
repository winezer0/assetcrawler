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
	// mapDeps 的元素相对 base（站点根），必须补上前导斜杠：
	// 否则在模块文件（/assets/chunks/x.js）下解析会变成
	// /assets/chunks/assets/chunks/... 这种重复前缀，实测全部 404。
	for _, want := range []string{
		"/assets/Admin.a1b2c3.js", "/assets/Login.d4e5f6.js", "/assets/index.7f8e9d.css",
	} {
		if !got[want] {
			t.Errorf("缺少 vite chunk %s，实际: %v", want, got)
		}
	}
	for p := range got {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("vite chunk 路径 %q 未锚定到站点根", p)
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

// TestTruncationReported 守住「超上限必须报出来」。
//
// 之前的行为是静默停止扫描：调用方只会看到「没找到 chunk」，
// 完全无法区分「包里没有」和「没扫到」。
func TestTruncationReported(t *testing.T) {
	small := []byte(`o.p="/static/js/";o.u=function(e){return"chunk-"+e+"."+{1:"aa"}[e]+".js"}`)
	if Extract(small).Truncated {
		t.Fatal("小文件不应被标记截断")
	}

	// 约 6MB，token 数肯定超过 maxTokens
	unit := []byte("a=1;")
	big := make([]byte, 0, 6<<20)
	for len(big) < 6<<20 {
		big = append(big, unit...)
	}
	res := Extract(big)
	if !res.Truncated {
		t.Fatalf("6MB 输入应当标记截断（token 上限 %d）", maxTokens)
	}
}

// TestViteMapDepsScopedToArray 确认只取 mapDeps 数组内的元素。
//
// 早期实现扫全文件的字符串字面量，把 `from"./framework.js"` 这类导入说明符
// 也按站点根锚定，拼出 /framework.js —— 站点上并不存在，实测 404。
func TestViteMapDepsScopedToArray(t *testing.T) {
	src := `const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["assets/chunks/A.js","assets/chunks/B.js"])))=>i.map(i=>d[i]);
import{f}from"./framework.Mlkvh5rC.js";import{g}from"./chunks/C.js";`
	res := Extract([]byte(src))

	got := map[string]string{}
	for _, c := range res.Chunks {
		got[c.Path] = c.Source
	}
	for _, want := range []string{"/assets/chunks/A.js", "/assets/chunks/B.js"} {
		if _, ok := got[want]; !ok {
			t.Errorf("缺少 %s，实际: %v", want, got)
		}
	}
	// 导入说明符不是 mapDeps，不应出现在这里（它们由 import 收集负责）
	for _, bad := range []string{"/framework.Mlkvh5rC.js", "/./framework.Mlkvh5rC.js", "/chunks/C.js"} {
		if _, ok := got[bad]; ok {
			t.Errorf("导入说明符被误当成 mapDeps: %s", bad)
		}
	}
}

// TestViteMapDepsRelativeToModule 守住 Nuxt 3 的真实形态。
//
// nuxt.com 入口是 /_nuxt/CfoxLZfe.js，mapDeps 里 211 个元素全部写成
// "./x.js"。以前统一补 "/" 得到 /./x.js → 归一化成 /x.js，整批 404；
// 现在保留相对形态，由 frontier 以当前 JS 的 URL 为 base 解析。
func TestViteMapDepsRelativeToModule(t *testing.T) {
	src := `const __vite__mapDeps=(i,m=__vite__mapDeps,d=(m.f||(m.f=["./A1b2C3.js","../shared/D4e5F6.js","assets/G7h8I9.js","/_nuxt/J0k1L2.js"])))=>i.map(i=>d[i]);`
	res := Extract([]byte(src))

	got := map[string]bool{}
	for _, c := range res.Chunks {
		got[c.Path] = true
	}
	for _, want := range []string{"./A1b2C3.js", "../shared/D4e5F6.js", "/assets/G7h8I9.js", "/_nuxt/J0k1L2.js"} {
		if !got[want] {
			t.Errorf("缺少 %s，实际: %v", want, got)
		}
	}
	if got["/./A1b2C3.js"] {
		t.Error("相对模块路径不该被补成 /./ 前缀")
	}
}

// TestDynamicImportTemplateLiteral 覆盖 Nuxt 的静态模板 import。
func TestDynamicImportTemplateLiteral(t *testing.T) {
	src := "const a=()=>import(`./A1b2C3.js`); const b=()=>import(`./${name}.js`);"
	res := Extract([]byte(src))
	got := map[string]bool{}
	for _, c := range res.Chunks {
		got[c.Path] = true
	}
	if !got["./A1b2C3.js"] {
		t.Errorf("缺少静态模板 chunk，实际: %v", got)
	}
	if got["./${name}.js"] {
		t.Error("含 ${} 的模板不该静态产出")
	}
}

// TestLooksLikeBundleFileRouteMarkers 确认文件路由框架的入口不会被粗筛跳过。
func TestLooksLikeBundleFileRouteMarkers(t *testing.T) {
	pad := strings.Repeat("x", 80)
	for _, mark := range []string{"__NUXT__", "_nuxt/", "_app/immutable/", "buildManifest", "__data.json"} {
		if !LooksLikeBundle([]byte(pad + mark)) {
			t.Errorf("标记 %q 未命中", mark)
		}
	}
	if LooksLikeBundle([]byte(pad + "nothing here")) {
		t.Error("无标记脚本不该命中")
	}
}
