package assetcrawler

import (
	"strings"
	"testing"
)

// TestDefaultJSEngineIsAuto 锁住默认引擎：零值配置也必须拿到 AST 能力。
//
// 之前默认是 regex，导致 AST 的全部收益（拼接还原、方法、参数、去噪）
// 在默认使用下都拿不到——探针只有 11/19，而切到 AST 是 13/19。
func TestDefaultJSEngineIsAuto(t *testing.T) {
	if got := DefaultOptions().JSEngine; got != JSEngineAuto {
		t.Fatalf("默认引擎 = %q，期望 %q", got, JSEngineAuto)
	}
	if len(engineChain("")) == 0 {
		t.Fatal("零值引擎名必须落到默认链上，否则库使用者会静默退化成 regex")
	}
}

// TestAutoEngineFallsBackOnESM 验证「goja 解析失败 → treesitter 接手」。
//
// 这是真实 Vite 产物的普遍形态：入口 chunk 用 `import ... from "./chunks/x.js"`，
// goja 在第一个 token 就报错。若不兜底，整份入口文件会退化成正则，丢掉所有 chunk 路径。
func TestAutoEngineFallsBackOnESM(t *testing.T) {
	const src = `import{a}from"./chunks/runtime.js";
export{a}from"./chunks/shared.js";
const API=import.meta.env.VITE_API;
fetch(API+"/v1/users");
export{API};`

	// goja 单独用必定失败，这是兜底的前提
	if _, err := engineChain(JSEngineGoja)[0]([]byte(src)); err == nil {
		t.Fatal("goja 应当无法解析 ESM，若已能解析则本兜底逻辑需要重新评估")
	}

	fn := adaptEngine(engineChain(JSEngineAuto))
	if fn == nil {
		t.Fatal("auto 链不应为空")
	}
	reqs, ok := fn(src)
	if !ok {
		t.Fatal("auto 链应当通过 treesitter 兜底成功")
	}

	var urls []string
	for _, r := range reqs {
		urls = append(urls, r.Raw)
	}
	joined := strings.Join(urls, " ")
	for _, want := range []string{"runtime.js", "shared.js", "/v1/users"} {
		if !strings.Contains(joined, want) {
			t.Errorf("缺少 %q，实际: %v", want, urls)
		}
	}
}

// TestVarLiteralNeedsEndpointShape 说明裸变量赋值的取舍：
// 深层路径算端点，浅层前缀不算。
func TestVarLiteralNeedsEndpointShape(t *testing.T) {
	cases := []struct {
		src  string
		want string
		bad  string
	}{
		{"var ep=\"/api/hidden/endpoint\";", "/api/hidden/endpoint", ""},
		{"var f=\"/static/js/app.js\";", "/static/js/app.js", ""},
		{"var base=\"/api/v3\";", "", "/api/v3"},
		{"var base=\"/api/v3\";fetch(base+\"/users\");", "", "/api/v3"},
	}
	fn := adaptEngine(engineChain(JSEngineAuto))
	for _, c := range cases {
		reqs, ok := fn(c.src)
		if !ok {
			t.Errorf("%q 解析失败", c.src)
			continue
		}
		var urls []string
		for _, r := range reqs {
			urls = append(urls, r.Raw)
		}
		joined := " " + strings.Join(urls, " ") + " "
		if c.want != "" && !strings.Contains(joined, " "+c.want+" ") {
			t.Errorf("%q 应拿到 %q，实际 %v", c.src, c.want, urls)
		}
		if c.bad != "" && strings.Contains(joined, " "+c.bad+" ") {
			t.Errorf("%q 不应把 %q 当端点，实际 %v", c.src, c.bad, urls)
		}
	}
}
