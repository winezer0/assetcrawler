package extractor

import "testing"

// TestMetaShareCarriers 覆盖 og:* / twitter:* 分享元数据。
//
// 这类位置在文档站、内容站很常见（og:url 常指向分享页变体，twitter:image 是卡片图），
// 早期只处理 meta refresh，等于把它们整体漏掉。
func TestMetaShareCarriers(t *testing.T) {
	const body = `<html><head>
  <meta property="og:url" content="/shared/og-page">
  <meta property="og:image" content="https://cdn.example.com/card.png">
  <meta name="twitter:image" content="/img/twitter-card.png">
  <meta property="og:site_name" content="My Site">
  <meta name="description" content="/not/a/path">
  <meta http-equiv="refresh" content="5; url=/login">
</head><body></body></html>`

	got := raws(FromHTML(parseHTML(t, body), DefaultOptions()))

	for _, want := range []string{"/shared/og-page", "https://cdn.example.com/card.png", "/img/twitter-card.png", "/login"} {
		if !contains(got, want) {
			t.Errorf("缺少 %s，实际 %v", want, got)
		}
	}
	// og:site_name 是文案、description 不是地址键，都不该被当成路径。
	for _, bad := range []string{"My Site", "/not/a/path"} {
		if contains(got, bad) {
			t.Errorf("误收非地址值 %q：%v", bad, got)
		}
	}
}

// TestDataAttrCarriers 覆盖 data-* 自定义属性里的端点。
//
// 规则是「键以 data- 开头 + 值有形似地址的起点」，并排除 data-id 这类标识符键。
func TestDataAttrCarriers(t *testing.T) {
	const body = `<html><body>
  <div data-api-endpoint="/api/from-data-attr"></div>
  <div data-href="/api/from-data-href"></div>
  <div data-id="123"></div>
  <div data-index="/2"></div>
  <div data-config='{"a":1}'></div>
  <button data-url="javascript:void(0)"></button>
  <span data-label="Delete row"></span>
</body></html>`

	got := raws(FromHTML(parseHTML(t, body), DefaultOptions()))

	for _, want := range []string{"/api/from-data-attr", "/api/from-data-href"} {
		if !contains(got, want) {
			t.Errorf("缺少 %s，实际 %v", want, got)
		}
	}
	for _, bad := range []string{"123", "/2", "javascript:void(0)", "Delete row"} {
		if contains(got, bad) {
			t.Errorf("误收非地址值 %q：%v", bad, got)
		}
	}
}
