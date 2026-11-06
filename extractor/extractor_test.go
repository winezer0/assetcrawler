package extractor

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func parseHTML(t *testing.T, s string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	return doc
}

func raws(reqs []Request) []string {
	out := make([]string, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, r.Raw)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestParseSrcset(t *testing.T) {
	got := ParseSrcset("/a.png 1x, /b.png 2x,/c.png 640w")
	want := []string{"/a.png", "/b.png", "/c.png"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFromHTML(t *testing.T) {
	// 覆盖各类容易漏掉的载体：老式 applet/object、表单、htmx、meta refresh、内联样式
	const body = `<!DOCTYPE html>
<html manifest="/app.manifest">
<head>
  <base href="https://example.com/app/">
  <link rel="stylesheet" href="css/main.css">
  <meta http-equiv="refresh" content="5; url=/login">
  <style>.bg{background:url('/img/bg.png')}</style>
</head>
<body background="/img/body.jpg">
  <a href="/page1">1</a>
  <area href="/page2">
  <script src="/js/app.js"></script>
  <script>var api="/api/v1/user/list";</script>
  <img src="img/logo.png" srcset="img/logo2x.png 2x">
  <iframe src="//cdn.example.com/frame.html"></iframe>
  <form action="/submit" method="POST"><input formaction="/alt" src="/btn.png"></form>
  <object data="/doc/x.swf"></object>
  <embed src="/media/player.swf">
  <video src="/v.mp4" poster="/poster.jpg"></video>
  <button hx-post="/api/click">go</button>
  <div style="background-image:url(/img/div.png)"></div>
  <blockquote cite="/quote"></blockquote>
</body></html>`

	doc := parseHTML(t, body)
	got := FromHTML(doc, DefaultOptions())
	list := raws(got)

	// 注意：extractor 只输出原始字符串，不做相对路径解析。
	// 带前导斜杠的保持原样，不带斜杠的相对路径也保持原样。
	want := []string{
		"css/main.css",
		"/login",
		"/img/bg.png",
		"/img/body.jpg",
		"/page1",
		"/page2",
		"/js/app.js",
		"img/logo.png",
		"img/logo2x.png",
		"//cdn.example.com/frame.html",
		"/submit",
		"/alt",
		"/btn.png",
		"/doc/x.swf",
		"/media/player.swf",
		"/v.mp4",
		"/poster.jpg",
		"/api/click",
		"/img/div.png",
		"/quote",
		"/app.manifest",
	}
	for _, w := range want {
		if !contains(list, w) {
			t.Errorf("missing expected raw URL: %s", w)
		}
	}

	// 表单必须区分出 POST
	for _, r := range got {
		if r.Raw == "/submit" && r.Method != http.MethodPost {
			t.Errorf("form method = %q, want POST", r.Method)
		}
		if r.Raw == "/api/click" && r.Method != http.MethodPost {
			t.Errorf("hx-post method = %q, want POST", r.Method)
		}
	}
}

func TestFromHTMLInlineScript(t *testing.T) {
	const body = `<html><body><script>
		fetch("/api/v2/orders");
		var u = "/admin/panel";
	</script></body></html>`
	doc := parseHTML(t, body)
	list := raws(FromHTML(doc, DefaultOptions()))

	for _, want := range []string{"/api/v2/orders", "/admin/panel"} {
		if !contains(list, want) {
			t.Errorf("inline script missing %s, got %v", want, list)
		}
	}
}

func TestFromHTMLNoJS(t *testing.T) {
	const body = `<html><body><script>var x="/api/secret";</script></body></html>`
	doc := parseHTML(t, body)
	list := raws(FromHTML(doc, Options{ExtractJS: false}))
	if contains(list, "/api/secret") {
		t.Error("ExtractJS=false should not extract from inline script")
	}
}

func TestBaseHref(t *testing.T) {
	doc := parseHTML(t, `<html><head><base href="https://example.com/app/"></head></html>`)
	if got := BaseHref(doc); got != "https://example.com/app/" {
		t.Errorf("BaseHref = %q", got)
	}

	doc = parseHTML(t, `<html><head></head></html>`)
	if got := BaseHref(doc); got != "" {
		t.Errorf("BaseHref on doc without base = %q, want empty", got)
	}
}

func TestFromHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Location", "/next")
	h.Set("Content-Location", "/canonical")
	h.Set("Link", `</api/spec>; rel="describedby", <https://cdn.example.com/x.js>; rel=preload`)
	h.Set("Refresh", "0; url=/refreshed")

	list := raws(FromHeaders(h))
	for _, want := range []string{"/next", "/canonical", "/api/spec", "https://cdn.example.com/x.js", "/refreshed"} {
		if !contains(list, want) {
			t.Errorf("missing header-derived URL %s, got %v", want, list)
		}
	}
}

func TestFromJS(t *testing.T) {
	js := `
		var a = "/api/user/info";
		var b = './v2/orders';
		var c = "https://api.example.com/v3/pay";
		var d = "api/v1/internal/report";
		var e = "/static/img/logo.png";     // 静态资源，应被过滤
		fetch(` + "`/api/tpl/${x}`" + `);   // 模板变量，应被过滤
		//# sourceMappingURL=app.js.map
	`
	list := raws(FromJS(js, DefaultOptions()))

	for _, want := range []string{
		"/api/user/info",
		"./v2/orders",
		"https://api.example.com/v3/pay",
		"api/v1/internal/report",
		"app.js.map",
	} {
		if !contains(list, want) {
			t.Errorf("missing JS-derived URL %s; got %v", want, list)
		}
	}
	if contains(list, "/static/img/logo.png") {
		t.Error("static asset should have been filtered out")
	}
	if contains(list, "/api/tpl/${x}") {
		t.Error("template placeholder should have been filtered out")
	}
}

func TestParseRobots(t *testing.T) {
	body := `
User-agent: *
Disallow: /admin
Disallow: /backup
Allow: /public
Sitemap: https://example.com/sitemap.xml

User-agent: Googlebot
Disallow: /nogoogle
`
	info := ParseRobots([]byte(body))
	if len(info.Sitemaps) != 1 || info.Sitemaps[0] != "https://example.com/sitemap.xml" {
		t.Errorf("sitemaps = %v", info.Sitemaps)
	}
	if len(info.Disallow) != 3 {
		t.Errorf("disallow = %v, want 3 entries", info.Disallow)
	}
}

func TestParseSitemap(t *testing.T) {
	body := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc></url>
  <url><loc>https://example.com/b</loc></url>
</urlset>`
	urls, sitemaps := ParseSitemap([]byte(body))
	if len(urls) != 2 || len(sitemaps) != 0 {
		t.Errorf("urls=%v sitemaps=%v", urls, sitemaps)
	}

	index := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/s1.xml</loc></sitemap>
</sitemapindex>`
	urls2, sitemaps2 := ParseSitemap([]byte(index))
	if len(urls2) != 0 || len(sitemaps2) != 1 {
		t.Errorf("index: urls=%v sitemaps=%v", urls2, sitemaps2)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	if !LooksLikeHTML("text/html; charset=utf-8", nil) {
		t.Error("text/html should be HTML")
	}
	if LooksLikeHTML("application/javascript", nil) {
		t.Error("javascript should not be HTML")
	}
	if !LooksLikeHTML("", []byte("<!DOCTYPE html><html>")) {
		t.Error("sniffing should detect HTML")
	}
	if !LooksLikeHTML("", []byte("<html><body>")) {
		t.Error("sniffing should detect <html>")
	}
}

func TestLooksLikeJS(t *testing.T) {
	if !LooksLikeJS("application/javascript", "/x") {
		t.Error("javascript content-type should match")
	}
	if !LooksLikeJS("", "/static/app.js") {
		t.Error(".js path should match")
	}
	if LooksLikeJS("", "/static/a.css") {
		t.Error(".css should be handled by LooksLikeCSS, not LooksLikeJS")
	}
	if !LooksLikeCSS("text/css", "/x") {
		t.Error("text/css content-type should be CSS")
	}
	if !LooksLikeCSS("", "/static/a.css") {
		t.Error(".css path should be CSS")
	}
	if LooksLikeCSS("text/html", "/page") {
		t.Error("html page should not be CSS")
	}
	if LooksLikeJS("text/html", "/page") {
		t.Error("html page should not match")
	}
}

func TestIsStaticAsset(t *testing.T) {
	for _, s := range []string{"/a.png", "/b.JPG", "/c.woff2", "/d.svg?v=2"} {
		if !IsStaticAsset(s) {
			t.Errorf("%s should be static asset", s)
		}
	}
	for _, s := range []string{"/api/user", "/login", "/a.js"} {
		if IsStaticAsset(s) {
			t.Errorf("%s should not be static asset", s)
		}
	}
}

// TestFromComments 覆盖注释里的路径提取。
// 这类路径在浏览器里不可见，但恰恰常指向模板与 include 文件，
// 是老 ASP/JSP 站点上很容易整体漏掉的一层资产。
func TestFromComments(t *testing.T) {
	const body = `<html>
<!-- InstanceBegin template="/Templates/MainTemplate.dwt.asp" codeOutsideHTMLIsLocked="false" -->
<body>
<!--#include virtual="/inc/header.asp" -->
<!--#include file="footer.inc" -->
<!-- 普通说明，不应提取 -->
<!-- 参考配置: "/conf/settings.xml" -->
</body></html>`

	doc := parseHTML(t, body)
	list := raws(FromHTML(doc, DefaultOptions()))

	for _, want := range []string{
		"/Templates/MainTemplate.dwt.asp",
		"/inc/header.asp",
		"footer.inc",
		"/conf/settings.xml",
	} {
		if !contains(list, want) {
			t.Errorf("missing comment-derived path %q, got %v", want, list)
		}
	}
}

// TestFormParams 验证表单控件名被当作「参数形状」收集。
//
// 在这之前只拿到一个光秃秃的 action：一个后台登录页能给出
// username / password / captcha 三个高价值参数名，全丢了。
func TestFormParams(t *testing.T) {
	const body = `<html><body>
<form action="/search" method="get">
  <input name="q"><input name="page"><select name="sort"></select>
  <input type="submit" name="go" value="1">
  <input name="off" disabled>
  <button name="btn" type="button"></button>
  <input type="hidden" name="token">
</form>
<form action="/login" method="post"><input name="username"><input name="password"></form>
</body></html>`

	doc := parseHTML(t, body)
	var search, login *Request
	for i, r := range FromHTML(doc, DefaultOptions()) {
		switch r.Raw {
		case "/search":
			search = &FromHTML(doc, DefaultOptions())[i]
		case "/login":
			login = &FromHTML(doc, DefaultOptions())[i]
		}
	}
	if search == nil || login == nil {
		t.Fatalf("未找到表单 action: search=%v login=%v", search, login)
	}

	wantSearch := []string{"q", "page", "sort", "token"}
	for _, w := range wantSearch {
		if !contains(search.Params, w) {
			t.Errorf("/search 缺少参数 %q，实际 %v", w, search.Params)
		}
	}
	// 按钮类控件不产生参数
	for _, bad := range []string{"go", "off", "btn"} {
		if contains(search.Params, bad) {
			t.Errorf("/search 不应包含 %q，实际 %v", bad, search.Params)
		}
	}
	if !contains(login.Params, "username") || !contains(login.Params, "password") {
		t.Errorf("/login 参数 = %v，期望含 username/password", login.Params)
	}
}

// TestCSSFragmentRefSkipped 验证 url(#id) 这类同文档引用不被当成资产。
//
// 实测 vuejs.org 上，CSS 里的 `url(#SVGxxx)` 被解析成
// /assets/%23SVGxxx，一个站点就贡献 32 条 404。
func TestCSSFragmentRefSkipped(t *testing.T) {
	const css = `.a{fill:url(#SVGfilter)} .b{background:url(/static/bg.png)}
.c{background:url(#grad1)} .d{background:url(./rel.svg)}
.e{background:url(data:image/png;base64,AAA)}`
	for _, r := range FromCSS(css) {
		switch {
		case strings.Contains(r.Raw, "#"), strings.HasPrefix(r.Raw, "data:"):
			t.Errorf("不应收集 %q", r.Raw)
		}
	}
	got := map[string]bool{}
	for _, r := range FromCSS(css) {
		got[r.Raw] = true
	}
	for _, want := range []string{"/static/bg.png", "./rel.svg"} {
		if !got[want] {
			t.Errorf("缺少 %q，实际: %v", want, got)
		}
	}
}

// TestStyleAttrFragmentSkipped 覆盖内联 style 属性里的同类引用。
func TestStyleAttrFragmentSkipped(t *testing.T) {
	const body = `<html><body>
<div style="fill:url(#f1);background:url(/img/a.png)"></div>
<svg><style>.x{clip-path:url(#clip)}</style></svg>
</body></html>`
	doc := parseHTML(t, body)
	for _, r := range FromHTML(doc, DefaultOptions()) {
		if strings.Contains(r.Raw, "#") {
			t.Errorf("style 里的片段引用被收集了: %q (source=%s)", r.Raw, r.Source)
		}
	}
}
