package jsext_test

import (
	"fmt"
	"strings"
	"testing"

	"assetcrawler/jsext"
	"assetcrawler/jsext/goja"
	"assetcrawler/jsext/treesitter"
)

// 覆盖拼接、方法识别、参数提取、ESM 四类场景。
var cases = []struct{ name, src string }{
	{"变量拼接+常量传播", `var mode="debug"; fetch("/api/"+mode+"/config");`},
	{"axios.post", `axios.post("/api/users", {role:"admin", active:true});`},
	{"axios.get+params", `axios.get("/api/list", {params:{page:1, size:20}});`},
	{"jQuery get", `$.get("/api/orders", {status:"new"});`},
	{"fetch PUT", `fetch("/api/items/1", {method:"PUT", body:{name:"x"}});`},
	{"xhr.open", `var x=new XMLHttpRequest(); x.open("DELETE", "/api/users/9");`},
	{"location 赋值", `document.location = "/login?redirect=" + r + "&method=oauth";`},
	{"ESM import", "import x from 'y';\nfetch('/api/esm');\nexport default x;"},
	{"动态 import", `const m = () => import("./chunk-a.js");`},
	{"无拼接字面量", `fetch("/api/plain");`},
}

func engines() map[string]jsext.Engine {
	return map[string]jsext.Engine{
		"goja":       goja.Extract,
		"treesitter": treesitter.Extract,
	}
}

func TestEngineComparison(t *testing.T) {
	for _, c := range cases {
		t.Logf("\n--- %s ---", c.name)
		t.Logf("  源码: %s", strings.ReplaceAll(strings.TrimSpace(c.src), "\n", " "))

		for _, name := range []string{"goja", "treesitter"} {
			fn := engines()[name]
			reqs, err := fn([]byte(c.src))
			if err != nil {
				t.Logf("  %-11s 解析失败: %v", name, err)
				continue
			}
			if len(reqs) == 0 {
				t.Logf("  %-11s 无结果", name)
				continue
			}
			var parts []string
			for _, r := range reqs {
				s := r.URL
				if r.Method != "" {
					s += " [" + r.Method + "]"
				}
				if len(r.QueryParams) > 0 {
					s += " ?" + strings.Join(r.QueryParams, ",")
				}
				if len(r.BodyParams) > 0 {
					s += " body:" + strings.Join(r.BodyParams, ",")
				}
				if r.Confidence == jsext.Template {
					s += " (模板)"
				}
				parts = append(parts, s)
			}
			t.Logf("  %-11s %s", name, strings.Join(parts, " | "))
		}
	}
}

// TestConsistency 记录两个引擎在「都能解析」的用例上是否给出相同结果。
func TestConsistency(t *testing.T) {
	same, diff := 0, 0
	for _, c := range cases {
		g, gerr := goja.Extract([]byte(c.src))
		s, serr := treesitter.Extract([]byte(c.src))
		if gerr != nil || serr != nil {
			continue
		}
		if normalize(g) == normalize(s) {
			same++
		} else {
			diff++
			t.Logf("不一致 [%s]\n  goja:       %s\n  treesitter: %s", c.name, normalize(g), normalize(s))
		}
	}
	t.Logf("一致 %d 例，不一致 %d 例", same, diff)
}

func normalize(in []jsext.Request) string {
	var parts []string
	for _, r := range in {
		parts = append(parts, fmt.Sprintf("%s|%s|%v|%v|%s",
			r.URL, r.Method, r.QueryParams, r.BodyParams, r.Confidence))
	}
	return strings.Join(parts, " ; ")
}
