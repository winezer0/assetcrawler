package jsext_test

import (
	"testing"

	"assetcrawler/jsext/treesitter"
)

// TestImportAliasMethod 锁定「import 别名 → HTTP 方法」的还原。
//
// 背景：`import { get as fetchIt } from "axios"` 之后调用 fetchIt("/api/x")，
// URL 能拿到（字面量就在参数里），但方法为空——fetchIt 既不在 httpClients 表里，
// 也不是 HTTP 动词。压缩产物里通常已退化成 `(0,n.get)` 靠动词判定能命中，
// 所以这个缺口只在未压缩 ESM 上暴露。
//
// 已知限制：`import { delete as del }` 这类**关键字做导入名**的写法，
// tree-sitter 的 JS 语法会整条 import 解析成 ERROR 节点（`delete` 被当语句关键字），
// 绑定与模块路径都拿不到。这是语法库的限制，不在本映射的责任范围内。
func TestImportAliasMethod(t *testing.T) {
	cases := []struct {
		name string
		src  string
		url  string
		want string
	}{
		{
			name: "具名别名 get",
			src:  `import { get as fetchIt } from "axios"; fetchIt("/api/alias-get");`,
			url:  "/api/alias-get",
			want: "GET",
		},
		{
			name: "具名别名 post",
			src:  `import { post as sendIt } from "axios"; sendIt("/api/alias-post");`,
			url:  "/api/alias-post",
			want: "POST",
		},
		{
			name: "具名别名 put",
			src:  `import { put as send } from "@/api/client"; send("/api/items/1");`,
			url:  "/api/items/1",
			want: "PUT",
		},
		{
			// 反例：名字不是动词时，即使来自 HTTP 模块也不该凭空造方法。
			name: "非动词名不猜",
			src:  `import { pick as p } from "lodash"; p("/api/not-http");`,
			url:  "/api/not-http",
			want: "",
		},
	}

	for _, c := range cases {
		reqs, err := treesitter.Extract([]byte(c.src))
		if err != nil {
			t.Errorf("%s: 解析失败: %v", c.name, err)
			continue
		}
		var got string
		found := false
		for _, r := range reqs {
			if r.URL == c.url {
				found = true
				got = r.Method
			}
		}
		if !found {
			var urls []string
			for _, r := range reqs {
				urls = append(urls, r.URL)
			}
			t.Errorf("%s: 未提取到 %s，实际 %v", c.name, c.url, urls)
			continue
		}
		if got != c.want {
			t.Errorf("%s: %s 的方法期望 %q，实际 %q", c.name, c.url, c.want, got)
		}
	}
}
