package csp_test

import (
	"fmt"
	"net/http"

	"assetcrawler/csp"
)

// Example 展示最常见的两种用法：取允许的来源主机、取上报端点。
//
// 这里只做解析，不发请求——抓不抓、抓哪些，由调用方按自己的范围策略决定。
func Example() {
	h := http.Header{}
	h.Set("Content-Security-Policy",
		"default-src 'self'; connect-src 'self' https://api.example.com; frame-ancestors https://partner.example.org")
	h.Set("Reporting-Endpoints", `csp="https://report.example.net/csp"`)

	res := csp.ParseHeader(h)

	fmt.Println("来源主机:", res.SourceHosts())
	fmt.Println("同站来源:", len(res.SameSite("example.com")))
	for _, it := range res.Reports() {
		fmt.Printf("上报端点: %s (%s)\n", it.Value, it.Directive)
	}

	// Output:
	// 来源主机: [api.example.com partner.example.org]
	// 同站来源: 1
	// 上报端点: https://report.example.net/csp (reporting-endpoints)
}
