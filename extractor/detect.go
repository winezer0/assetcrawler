package extractor

import (
	"regexp"
	"strings"
)

// 内容类型识别。放在 extractor 里，是因为「这段响应该按什么解析」
// 属于提取层知识，编排层只负责把响应交给正确的提取函数。

// LooksLikeHTML 判断响应是否值得按 HTML 解析。
func LooksLikeHTML(contentType string, body []byte) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml") {
		return true
	}
	if ct != "" {
		return false
	}
	// 没有 Content-Type 时靠嗅探
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	s := strings.ToLower(string(head))
	return strings.Contains(s, "<html") || strings.Contains(s, "<!doctype html")
}

// LooksLikeCSS 判断响应是否为样式表。CSS 需要专用解析，不能走 JS 分支。
func LooksLikeCSS(contentType, path string) bool {
	if strings.Contains(strings.ToLower(contentType), "text/css") {
		return true
	}
	return strings.HasSuffix(strings.ToLower(path), ".css")
}

// LooksLikeJS 判断响应是否应按脚本/样式内容做 endpoint 提取。
func LooksLikeJS(contentType, path string) bool {
	ct := strings.ToLower(contentType)
	if strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript") {
		return true
	}
	p := strings.ToLower(path)
	return strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".mjs")
}

// staticAssetRe 识别「大概率不是接口」的静态资源。
// 资产收集场景下这些 URL 本身仍有价值（可能泄露信息），
// 这里只用于避免 JS 提取把大量图片路径灌进结果。
var staticAssetRe = regexp.MustCompile(`(?i)\.(png|jpe?g|gif|webp|svg|ico|bmp|tiff|woff2?|ttf|otf|eot|mp4|webm|avi|mov|mp3|wav|ogg|zip|rar|7z|gz|tar|pdf|docx?|xlsx?|pptx?|exe|dll|apk|ipa)$`)

// IsStaticAsset 判断原始地址是否指向静态资源（忽略 query）。
func IsStaticAsset(raw string) bool {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return staticAssetRe.MatchString(raw)
}
