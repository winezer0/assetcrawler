package extractor

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// TestFromInlineJSONNuxtData 覆盖 Nuxt 3 的 hydration payload。
// 斜杠被写成 \u002F，按普通 JS 或正则扫字面量都拿不到，必须按 JSON 解码。
func TestFromInlineJSONNuxtData(t *testing.T) {
	const payload = `[["ShallowReactive",1],{"path":2},"\u002Fdocs\u002F4.x","\u002Fapi\u002Fitems","Docs","/img/logo.png","/x/EXPR","https://cdn.example.com/a.js"]`

	got := FromInlineJSON(payload, "nuxt-data")
	urls := raws(got)
	for _, want := range []string{"/docs/4.x", "/api/items", "https://cdn.example.com/a.js"} {
		if !contains(urls, want) {
			t.Errorf("缺少 %s，实际 %v", want, urls)
		}
	}
	for _, bad := range []string{"ShallowReactive", "Docs", "/img/logo.png", "/x/EXPR"} {
		if contains(urls, bad) {
			t.Errorf("不该提取 %q，实际 %v", bad, urls)
		}
	}
	for _, r := range got {
		if r.Source != "inline-json:nuxt-data" {
			t.Errorf("source 期望 inline-json:nuxt-data，实际 %q", r.Source)
		}
	}
}

// TestFromInlineJSONNextData 覆盖 Next.js Pages Router 的对象形态。
func TestFromInlineJSONNextData(t *testing.T) {
	const payload = `{"props":{"pageProps":{"href":"/docs/getting-started"}},"page":"/","buildId":"abc"}`

	got := FromInlineJSON(payload, "next-data")
	if len(got) != 1 || got[0].Raw != "/docs/getting-started" {
		t.Fatalf("期望只取出 /docs/getting-started，实际 %v", raws(got))
	}
	if got[0].Source != "inline-json:next-data" {
		t.Errorf("source = %q", got[0].Source)
	}
}

// TestFromInlineJSONFallback 覆盖带赋值前缀、不是合法 JSON 的初始化脚本。
func TestFromInlineJSONFallback(t *testing.T) {
	const payload = `window.__INITIAL_STATE__ = {"links":["\/account\/profile","\/api\/v2\/orders","some text"]};`

	got := FromInlineJSON(payload, "initial-state")
	urls := raws(got)
	for _, want := range []string{"/account/profile", "/api/v2/orders"} {
		if !contains(urls, want) {
			t.Errorf("缺少 %s，实际 %v", want, urls)
		}
	}
}

// TestFromInlineJSONLimit 确认大 payload 不会灌满队列。
func TestFromInlineJSONLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < 400; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"/page/`)
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + i/26)))
		b.WriteString(`"`)
	}
	b.WriteString("]")

	if got := len(FromInlineJSON(b.String(), "nuxt-data")); got > maxInlineJSONURLs {
		t.Errorf("产出 %d 条，超过上限 %d", got, maxInlineJSONURLs)
	}
}

// TestInlineJSONKind 确认只认 hydration 载体，不认 ld+json。
func TestInlineJSONKind(t *testing.T) {
	cases := []struct {
		name string
		html string
		want string
	}{
		{"nuxt by id", `<script id="__NUXT_DATA__" type="application/json">[]</script>`, "nuxt-data"},
		{"nuxt by marker", `<script type="application/json" data-nuxt-data="nuxt-app">[]</script>`, "nuxt-data"},
		{"next pages", `<script id="__NEXT_DATA__" type="application/json">{}</script>`, "next-data"},
		{"ld+json", `<script type="application/ld+json">{}</script>`, ""},
		{"plain script", `<script>var a=1</script>`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := inlineJSONKind(firstScriptNode(t, c.html))
			if c.want == "" && ok {
				t.Errorf("不该识别，实际 %q", got)
			}
			if c.want != "" && got != c.want {
				t.Errorf("期望 %q，实际 %q", c.want, got)
			}
		})
	}
}

func firstScriptNode(t *testing.T, s string) *html.Node {
	t.Helper()
	doc := parseHTML(t, s)
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && strings.EqualFold(n.Data, "script") {
			found = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if found == nil {
		t.Fatal("HTML 里没有 script 节点")
	}
	return found
}

// TestInlineScriptMergesASTAndJSON 锁定「合并而非二选一」。
//
// 只跑结构化解析会丢掉 payload 里的绝对外链：它们可能排在 200 条上限之后，
// 或属于被我们过滤的静态资源，但旧版从同一份 payload 里是能拿到的。
func TestInlineScriptMergesASTAndJSON(t *testing.T) {
	const payload = `["/docs/4.x","https://scripts.example.com/a.js","/img/logo.svg"]`
	doc := parseHTML(t, `<html><body><script id="__NUXT_DATA__" type="application/json">`+payload+`</script></body></html>`)
	urls := raws(FromHTML(doc, Options{ExtractJS: true}))

	for _, want := range []string{"/docs/4.x", "https://scripts.example.com/a.js"} {
		if !contains(urls, want) {
			t.Errorf("缺少 %s，实际 %v", want, urls)
		}
	}
	var structured bool
	for _, r := range FromInlineJSON(payload, "nuxt-data") {
		if r.Raw == "/docs/4.x" && r.Source == "inline-json:nuxt-data" {
			structured = true
		}
	}
	if !structured {
		t.Error("结构化解析的独立 source 丢失")
	}
}

// TestFromHTMLInlineJSON 是端到端分派：内联 hydration JSON 走专用解析。
func TestFromHTMLInlineJSON(t *testing.T) {
	doc := parseHTML(t, `<html><body><script id="__NUXT_DATA__" type="application/json">[["\u002Fdocs\u002F4.x","/api/orders"]]</script></body></html>`)
	got := FromHTML(doc, Options{ExtractJS: true})

	var found bool
	for _, r := range got {
		if r.Raw == "/docs/4.x" {
			found = true
			if r.Source != "inline-json:nuxt-data" {
				t.Errorf("source = %q", r.Source)
			}
		}
	}
	if !found {
		t.Fatalf("未从内联 hydration JSON 取出路径，实际 %v", raws(got))
	}
}
