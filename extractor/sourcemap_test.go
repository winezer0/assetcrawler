package extractor

import "testing"

// TestFromSourceMap 覆盖 sources 的归一化。
//
// 只有 webpack 伪协议里的绝对路径能还原成站点可访问的地址，
// 构建机上的相对源码路径（../src/x.ts）部署后并不存在，必须跳过。
func TestFromSourceMap(t *testing.T) {
	body := []byte(`{
	  "version": 3,
	  "sources": [
	    "webpack:///src/api/order.js",
	    "webpack://app/./src/x.js",
	    "webpack:///./src/y.js",
	    "../src/local-only.ts",
	    "node_modules/react/index.js",
	    ""
	  ],
	  "mappings": "AAAA"
	}`)

	got := raws(FromSourceMap(body))
	want := []string{"/src/api/order.js", "/src/x.js", "/src/y.js"}

	if len(got) != len(want) {
		t.Fatalf("期望 %d 条，实际 %d 条：%v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 条期望 %s，实际 %s", i, want[i], got[i])
		}
	}
}

// TestLooksLikeSourceMap 锁定判定口径：后缀优先，内容兜底，不误伤普通 JSON。
func TestLooksLikeSourceMap(t *testing.T) {
	cases := []struct {
		path string
		body string
		want bool
	}{
		{"/app.js.map", "", true},
		{"/x", `{"version":3,"sources":["a"]}`, true},
		{"/api/orders", `{"version":"1.0","sources":[]}`, true},
		{"/api/orders", `{"items":[1,2],"total":3}`, false},
		{"/x", `{"sources":[]}`, false},
	}
	for _, c := range cases {
		if got := LooksLikeSourceMap(c.path, []byte(c.body)); got != c.want {
			t.Errorf("LooksLikeSourceMap(%q, %q) = %v, want %v", c.path, c.body, got, c.want)
		}
	}
}
