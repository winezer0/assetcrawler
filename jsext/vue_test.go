package jsext_test

import (
	"strings"
	"testing"

	"assetcrawler/jsext"
	"assetcrawler/jsext/goja"
	"assetcrawler/jsext/treesitter"
)

// vueSrc 复刻 webpack 5 + Vue 3 CLI 产物里最典型的几种写法。
//
// 关键特征：变量被压缩成单字母（axios -> n）、接口地址大量用模板字符串、
// 派生常量（var u = c + "/orders"）代替重复字面量。
const vueSrc = `
var c = "/api/v3";
var u = c + "/orders";

n.get(` + "`${c}/products/${id}`" + `);
n.put(` + "`${c}/orders/${id}`" + `, {status: "paid"});
n.delete(c + "/orders/" + id);
n.post(u, {sku: "A-1", qty: 2});
fetch(` + "`${c}/search?q=${q}`" + `);
axios.get("/api/v3/list", {params: {page: 1, size: 20}});
store.get("token");
`

// TestVueWebpack 锁住对 Vue/webpack 产物的三件事：
// 模板字符串求值、压缩后方法推断、派生常量传播。
func TestVueWebpack(t *testing.T) {
	engines := map[string]jsext.Engine{
		"goja":       goja.Extract,
		"treesitter": treesitter.Extract,
	}

	for name, fn := range engines {
		t.Run(name, func(t *testing.T) {
			reqs, err := fn([]byte(vueSrc))
			if err != nil {
				t.Fatalf("解析失败: %v", err)
			}
			got := map[string]jsext.Request{}
			for _, r := range reqs {
				got[r.URL] = r
			}

			cases := []struct {
				url    string
				method string
				conf   string
			}{
				// 模板字符串：常量 base 代入，变量记 EXPR
				{"/api/v3/products/EXPR", "GET", jsext.Template},
				{"/api/v3/orders/EXPR", "PUT", jsext.Template},
				{"/api/v3/search?q=EXPR", "GET", jsext.Template},
				// 派生常量 + 压缩后方法名：u 必须还原成 /api/v3/orders
				{"/api/v3/orders", "POST", jsext.Certain},
				// 参数对象
				{"/api/v3/list", "GET", jsext.Certain},
			}
			for _, c := range cases {
				r, ok := got[c.url]
				if !ok {
					t.Errorf("缺少 %s，实际: %v", c.url, keys(got))
					continue
				}
				if r.Method != c.method {
					t.Errorf("%s 方法 = %q，期望 %q", c.url, r.Method, c.method)
				}
				if r.Confidence != c.conf {
					t.Errorf("%s 置信度 = %q，期望 %q", c.url, r.Confidence, c.conf)
				}
			}

			if r, ok := got["/api/v3/list"]; ok {
				if len(r.QueryParams) != 2 {
					t.Errorf("/api/v3/list 参数 = %v，期望 [page size]", r.QueryParams)
				}
			}

			// store.get("token") 不是地址，不能因属性名是 get 就判成请求
			for u := range got {
				if strings.Contains(u, "token") {
					t.Errorf("store.get(\"token\") 被误判为请求: %s", u)
				}
			}
		})
	}
}

func keys(m map[string]jsext.Request) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
