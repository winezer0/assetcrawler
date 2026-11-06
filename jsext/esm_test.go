package jsext_test

import (
	"fmt"
	"strings"
	"testing"

	"assetcrawler/jsext"
	"assetcrawler/jsext/goja"
	"assetcrawler/jsext/treesitter"
)

// esmCase 是一个 ESM/边界语法用例。
type esmCase struct {
	name string
	src  string
	want []string // 期望在提取结果里出现的 URL 片段
}

// esmCases 来自真实 Vite 产物与 ESM 规范的高频写法。
//
// goja 不支持 ESM（含 import.meta、顶层 await），因此这一组只对
// treesitter 做断言：它是「ESM 兜底」的最后一层，一旦它退化，
// 所有 Vite 站点会整文件退化成正则。
var esmCases = []esmCase{
	{"export const 数组", `export const routes=[{path:"/a",component:F}];`, []string{"/a"}},
	{"export default 对象", `export default {base:"/api/default",list:"/api/list"};`, []string{"/api/default", "/api/list"}},
	{"export default 函数", `export default function(){return fetch("/api/fn")}`, []string{"/api/fn"}},
	{"import 默认+具名", "import a from \"./a.js\";\nimport {b,c as d} from \"./b.js\";\nfetch(\"/api/two\");", []string{"/api/two"}},
	{"export {x} from", `export {x} from "./reexport.js"; fetch("/api/three");`, []string{"/api/three", "./reexport.js"}},
	{"export * from", `export * from "./star.js"; fetch("/api/star");`, []string{"/api/star", "./star.js"}},
	{"import.meta.url", `const u=new URL("./x.json",import.meta.url); fetch("/api/meta");`, []string{"/api/meta"}},
	{"import.meta.env", `const API=import.meta.env.VITE_API; fetch(API+"/v1/users");`, []string{"/v1/users"}},
	{"顶层 await", `const r=await fetch("/api/tla");`, []string{"/api/tla"}},
	{"动态 import 拼接", `const m=id=>import("./chunk-"+id+".js");`, []string{"./chunk-"}},
	{"ESM+模板字符串", "export const c=\"/api/v9\";\nfetch(`/api/v9/orders`);", []string{"/api/v9/orders"}},
	{"导出后紧接 IIFE", `export const x=1;(function(){fetch("/api/iife")})();`, []string{"/api/iife"}},
}

// TestESMCoverage 锁定 treesitter 的 ESM 覆盖能力。
func TestESMCoverage(t *testing.T) {
	for _, c := range esmCases {
		reqs, err := treesitter.Extract([]byte(c.src))
		if err != nil {
			t.Errorf("%s: 解析失败 %v", c.name, err)
			continue
		}
		joined := ""
		for _, r := range reqs {
			joined += r.URL + " "
		}
		for _, w := range c.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: 缺少 %q，实际: %v", c.name, w, urlsOnly(reqs))
			}
		}
	}
}

// TestGojaESMLimitation 记录 goja 的能力边界。
//
// 这条不是「期望失败」，而是显式声明：一旦 goja 将来支持了 ESM，
// 这个测试会失败，提示我们重新评估是否需要 treesitter 兜底。
func TestGojaESMLimitation(t *testing.T) {
	failing := []string{
		`export default {a:"/x"};`,
		`export * from "./a.js";`,
		`fetch(import.meta.env.VITE_API);`,
		`await fetch("/x");`,
	}
	for _, src := range failing {
		if _, err := goja.Extract([]byte(src)); err == nil {
			t.Errorf("goja 现在能解析 %q 了，请重新评估 treesitter 兜底是否还需要", src)
		}
	}
}

// TestTruncatedBundleRecovers 验证 body 被截断时仍能继续提取。
//
// 爬虫默认把响应体截到 2MB，大 bundle 一定会在函数中途断开。
// treesitter 容错解析，能拿到已下载部分里的地址；goja 会整文件失败。
func TestTruncatedBundleRecovers(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("(function(){\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&sb, "fetch(\"/api/item/%d\");\n", i)
	}
	sb.WriteString("})();\n")
	full := sb.String()
	src := []byte(full[:len(full)/2]) // 切在函数中间

	if _, err := goja.Extract(src); err == nil {
		t.Log("goja 也能解析截断内容了，容错兜底的意义需要重新评估")
	}
	reqs, err := treesitter.Extract(src)
	if err != nil {
		t.Fatalf("treesitter 应能容错解析: %v", err)
	}
	if len(reqs) < 30 {
		t.Fatalf("截断后应仍能提取到大量地址，实际只有 %d 条", len(reqs))
	}
}

// TestCSSSelectorNotURL 防止把 CSS 选择器当成地址。
//
// 真实产物里 `e.querySelector(".blocks")` 曾被当成 URL 输出。
func TestCSSSelectorNotURL(t *testing.T) {
	src := []byte("(function(){var r=document.querySelector(\".blocks\");if(!r)return;fetch(\"/api/ok\")})()")
	for name, fn := range map[string]jsext.Engine{"goja": goja.Extract, "treesitter": treesitter.Extract} {
		reqs, err := fn(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, r := range reqs {
			if r.URL == ".blocks" {
				t.Errorf("%s: CSS 选择器被当成地址", name)
			}
		}
	}
}

func urlsOnly(reqs []jsext.Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.URL)
	}
	return out
}

// TestNonURLLiterals 防止把「像路径但不是地址」的字面量当成资产。
//
// 实测来源：Vite 站点 JS 里的 "text/plain"、"modulepreload"、"Running..."、"/*"。
func TestNonURLLiterals(t *testing.T) {
	src := []byte(`var a="text/plain";var b="modulepreload";var c="Running...";
var d="/*";fetch("/api/real");`)
	for name, fn := range map[string]jsext.Engine{"goja": goja.Extract, "treesitter": treesitter.Extract} {
		reqs, err := fn(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var urls []string
		for _, r := range reqs {
			urls = append(urls, r.URL)
		}
		for _, bad := range []string{"text/plain", "modulepreload", "Running...", "/*"} {
			for _, u := range urls {
				if u == bad {
					t.Errorf("%s: 非 URL 字面量被收集: %q", name, bad)
				}
			}
		}
		found := false
		for _, u := range urls {
			if u == "/api/real" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: 正常路径 /api/real 丢失，实际 %v", name, urls)
		}
	}
}

// TestConstLetPropagation 锁定 const/let 的常量传播。
//
// goja 把 `var` 与 `const`/`let` 分成两种 AST 节点（VariableStatement /
// LexicalDeclaration）。早期只处理了前者，导致现代代码里最常见的
// `const BASE = "/api/v3"` 完全无法传播，拼接结果退化成 EXPR/orders。
// 这个 bug 在夹具里没暴露，因为夹具写的是 `var`。
func TestConstLetPropagation(t *testing.T) {
	cases := []string{
		`var BASE="/api/v3"; axios.post(BASE+"/orders");`,
		`const BASE="/api/v3"; axios.post(BASE+"/orders");`,
		`let BASE="/api/v3"; axios.post(BASE+"/orders");`,
		`const BASE="/api/v3"; const U=BASE+"/orders"; fetch(U);`,
	}
	for name, fn := range map[string]jsext.Engine{"goja": goja.Extract, "treesitter": treesitter.Extract} {
		for _, src := range cases {
			reqs, err := fn([]byte(src))
			if err != nil {
				t.Errorf("%s: %q 解析失败: %v", name, src, err)
				continue
			}
			found := false
			for _, r := range reqs {
				if r.URL == "/api/v3/orders" {
					found = true
				}
			}
			if !found {
				var got []string
				for _, r := range reqs {
					got = append(got, r.URL)
				}
				t.Errorf("%s: %q 未还原出 /api/v3/orders，实际 %v", name, src, got)
			}
		}
	}
}
