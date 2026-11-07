package extractor

import "testing"

// TestFromNextFlight 覆盖 Next.js App Router 的 RSC 流解析。
//
// 这份 payload 是文件路由框架里唯一结构化暴露导航结构的地方，
// 两个坑都在真实站点上踩过：
//   - chunk 引用有一半省略了 `/_next/` 前缀（`static/chunks/x.js`）
//   - URL 前缀常写成 `\/`，用 Go 的 strconv 反转义会整段失败
func TestFromNextFlight(t *testing.T) {
	const script = `self.__next_f.push([1,"1:\"$Sreact.fragment\"\n"]);
self.__next_f.push([1,"2:\"/_next/static/chunks/a.js\"\n3:\"static/chunks/b.js\"\n4:\"/docs\"\n5:\"\/_next\/static\/chunks\/c.js\"\n6:\"/$L7f3\"\n7:\"not a path\"\n"])`

	got := FromNextFlight(script)
	byURL := map[string]string{}
	for _, r := range got {
		byURL[r.Raw] = r.Kind
	}

	want := map[string]string{
		"/_next/static/chunks/a.js": "chunk",
		"/_next/static/chunks/b.js": "chunk", // 补回 /_next/ 前缀
		"/_next/static/chunks/c.js": "chunk", // `/` 转义还原
		"/docs":                     "",      // 页面路径，普通 URL
	}
	for u, kind := range want {
		gotKind, ok := byURL[u]
		if !ok {
			t.Errorf("缺少 %s，实际 %v", u, byURL)
			continue
		}
		if gotKind != kind {
			t.Errorf("%s 的 kind 期望 %q，实际 %q", u, kind, gotKind)
		}
	}

	// RSC 标记与文案都不能当路径
	for _, bad := range []string{"/$L7f3", "not a path"} {
		if _, ok := byURL[bad]; ok {
			t.Errorf("不该提取 %q", bad)
		}
	}
}

// TestLooksLikeNextFlight 确认只对 RSC 脚本启用专用解析。
func TestLooksLikeNextFlight(t *testing.T) {
	if !LooksLikeNextFlight(`self.__next_f=self.__next_f||[]`) {
		t.Error("初始化脚本应命中")
	}
	if LooksLikeNextFlight(`var api="/api/x"`) {
		t.Error("普通内联脚本不该命中")
	}
}

// TestUnescapeJSString 覆盖 JS 特有的转义。
func TestUnescapeJSString(t *testing.T) {
	cases := map[string]string{
		`a\/b`:       "a/b",
		`a\"b`:       `a"b`,
		`a\\b`:       `a\b`,
		`a\u0041b`:   "aAb",
		`plain-path`: "plain-path",
	}
	for in, want := range cases {
		got, ok := unescapeJSString(in)
		if !ok || got != want {
			t.Errorf("unescapeJSString(%q) = (%q, %v)，期望 %q", in, got, ok, want)
		}
	}
}

// TestFromNextFlightInitStyle 覆盖初始化式接收者。
func TestFromNextFlightInitStyle(t *testing.T) {
	const script = `(self.__next_f=self.__next_f||[]).push([1,"1:\"/docs/init\"\n2:\"static/chunks/x.js\"\n"])`
	got := raws(FromNextFlight(script))
	for _, want := range []string{"/docs/init", "/_next/static/chunks/x.js"} {
		if !contains(got, want) {
			t.Errorf("缺少 %s，实际 %v", want, got)
		}
	}
}

// TestInlineScriptFallsBackWithoutPayload 守住「命中 __next_f 但无 payload」的归零。
//
// 初始化脚本里除了 __next_f 还写着普通接口路径；只跑 FromNextFlight 会把
// 整段脚本丢掉，必须回退到通用 JS 提取。
func TestInlineScriptFallsBackWithoutPayload(t *testing.T) {
	doc := parseHTML(t, `<html><body><script>self.__next_f=self.__next_f||[];fetch("/api/from-init-script")</script></body></html>`)
	got := FromHTML(doc, Options{ExtractJS: true})
	if !contains(raws(got), "/api/from-init-script") {
		t.Fatalf("初始化脚本里的普通接口路径被丢弃，实际 %v", raws(got))
	}
}
