# assetcrawler

零浏览器依赖的 Go Web URL 资产采集引擎。

面向安全资产收集场景：从一个入口 URL 出发，把目标站点上所有能静态发现的 URL
尽可能挖干净——包括 HTML 标签属性、响应头、JS 文件里的接口路径、robots.txt、
sitemap.xml。不启动无头浏览器，不执行 JavaScript。

## 为什么不用现成的

调研过一圈，结论是生态里没有对口的包：

| 方案 | 问题 |
|---|---|
| `colly` | 通用爬虫框架，解决的是队列/去重/限速。资产提取要自己从零写，且不支持 JS 内容挖 endpoint |
| `katana`（作为库引入） | 实测只 import `pkg/engine/parser` 就拉进 **56 个依赖**、`-s -w` 后二进制 **20.2MB**（colly 对照：17 依赖 / 13.4MB）；依赖链 `parser → pkg/output → dsl → go-openai/jarm-go` 把 nuclei 的 DSL 引擎和 LLM 客户端都带进来了；且 `parser_generic.go` 带 `//go:build !(386 \|\| windows)`，**Windows 上 jsluice 的 JS 提取能力整体缺失** |
| `hakrawler` / `gospider` / `cariddi` / `URLFinder` | 都是 CLI，核心逻辑在 `main` 包里，无法作为库复用 |
| `projectdiscovery/urlfinder` | 纯被动（只查存档），完全不访问目标，不是爬虫 |

本包只依赖 `golang.org/x/net`（Go 官方扩展库），`-s -w` 后二进制约 7.1MB。

## 包结构

```
assetcrawler/      编排层：种子、BFS 调度、结果组装
├── fetcher/     HTTP 抓取：连接池、限速、按 host 并发、重试、重定向链、body 截断
├── extractor/   URL 提取：纯函数，输入 HTML/响应头/脚本文本，输出原始字符串
├── frontier/    归一化 + 范围判定 + 去重
├── routegraph/  打包产物恢复：chunk 文件名 + 前端路由表（自带的 JS 词法分析器）
├── jsext/       JS AST 提取引擎：统一契约 + goja / gotreesitter 两套实现
└── csp/         CSP / Report-To 响应头解析：纯函数工具包，不被采集流程依赖
```

依赖单向，子包之间没有横向依赖（jsext 的两个实现只依赖 jsext 契约，
extractor 通过 Options 注入 jsext 的能力，因此自身仍是零第三方依赖）：

```
assetcrawler ──┬──> fetcher
             ├──> extractor ──(注入)──> jsext/{goja,treesitter}
             ├──> frontier
             └──> routegraph
```

分工边界是刻意的：**extractor 不认识 URL 语义，frontier 不认识 HTML，fetcher 不认识「爬虫」，
routegraph 不认识网络**。所以想单独要「一张页面里所有 URL」的人，可以只 import `extractor`；
想单独从一份 bundle 里要 chunk 清单的人，可以只 import `routegraph`。

## 能力

**HTML 提取**（覆盖 20+ 标签 / 30+ 属性）
`a[href]` `area` `base` `link` `script[src]` `img[src|srcset|data-src]` `source`
`video[src|poster]` `audio` `iframe` `frame` `embed` `object[data|codebase]`
`applet[code|codebase|archive]` `form[action]` `input[src|formaction]`
`button[formaction]` `blockquote[q|del|ins][cite]` `body[background]`
`table/tr/td/th[background]` `html[manifest]` `svg use[href]`
`meta[http-equiv=refresh]`、htmx 的 `hx-get/post/put/delete/patch`、
内联 `style` 与 `<style>` 块里的 `url()`、以及 `<script>` 内联代码。

**HTML 注释里的路径**
SSI 的 `<!--#include virtual|file="x"-->`、Dreamweaver 的
`<!-- InstanceBegin template="/Templates/x.dwt.asp" -->`、以及注释中带扩展名的绝对路径。
这类路径浏览器不渲染，但在老 ASP/JSP 站点上常直接指向模板与 include 配置文件——
实测里正是靠这一条，才没漏掉 `testasp.vulnweb.com` 的模板页。

**JSON 响应体**
递归提取响应里以 `/` 或 `http(s)://` 开头的字符串。现代接口常用
`{"next":"/api/orders?page=2"}` 这种「下一跳」，它既不在 HTML 也不在 JS 源码里，
只出现在响应体中，静态提取看不到。单份响应体上限 200 条，避免大列表接口灌满队列。

**参数形状**
表单控件名（`input`/`select`/`textarea`，跳过 submit/button/reset/image/file 与 disabled）
与 JS 调用里的参数对象都作为 `params` 字段带出。只有名字没有取值——
真实取值要么在客户端生成，要么需要交互，编造一个反而污染结果。

**响应头提取**
`Location`、`Content-Location`、`Link`、`Refresh`，外加整条重定向链。

**JS / CSS 内容提取**
从脚本源码里捞绝对 URL、引号包裹的绝对路径、相对路径、`sourceMappingURL`。
自动过滤静态图片/字体路径与模板占位符（`${}`、`%s`）。

**打包产物里的 chunk 与前端路由（`routegraph` 包）**

懒加载 chunk 和 SPA 路由都不挂在任何静态链接上，普通爬虫看不到。从 bundle 的
运行时清单里静态恢复两类目标：

- **chunk 文件名**：webpack 4/5 的 `__webpack_require__.u`（含 hash 查找表）、
  `__vite__mapDeps`、动态 `import()` 说明符。
  `o.u = e => "chunk-" + e + "." + {101:"a1b2c3"}[e] + ".js"` 这类写法里，
  文件名里的 `e` 与查表下标的 `e` 是同一个变量，必须按**同一个 key** 对齐，
  否则会拼出 `chunk-101.d4e5f6.js` 这种不存在的组合。
- **前端路由表**：括号栈扫描 `{path:"/x", component|name|element|children}` 形状的对象，
  不依赖 AST，对压缩与 ESM 产物一致有效。

结果带 `kind` 字段：`chunk` 是真实文件（会抓取），`route` 是前端路由
（也抓取，但调用方可据此过滤——SPA 的 `/admin/users` 在服务端往往只是同一个 index.html）。

```bash
assetcrawler -u https://target/ -jsonl                # 默认开启
assetcrawler -u https://target/ -no-chunk-graph       # 关掉
```

**执行兜底（默认编入）**

路由表如果由运行时 `map` 生成（`sections.map(s => ({path:"/"+s}))`），静态解析拿不到，
可以用 goja 把 chunk 真跑一遍。这条能力**默认编在二进制里**，不需要构建标签：

```bash
assetcrawler -u https://target/ -route-exec
```

代价是链接 goja 的完整 VM：二进制从 15.22 MB 涨到 23.70 MB（+8.48 MB）。
没有做成可选编译，因为「同一个程序有两种能力」会让基线和结论失去可比性。

运行时开关默认关闭（`Options.RouteExec = false`）——它会执行目标站的代码，
属于需要主动同意的动作，而不是功能阉割。

限制很明确：只对自注册的 webpack chunk（`self.webpackChunkX.push(...)`）有效；
ESM/Vite 产物完全不支持（goja 不认 `import`/`export`，连动态 `import()` 都报语法错误）；
必须带超时（默认 2 秒），因为它在运行目标站的代码。

**CSP 响应头（独立工具包，按需调用）**

`csp` 包解析 `Content-Security-Policy` / `CSP-Report-Only` / `Report-To` /
`Reporting-Endpoints`，给出「页面 HTML 与 JS 里未必出现」的基础设施线索：

```go
res := csp.ParseHeader(resp.Header)

res.SourceHosts()        // 策略允许的来源主机（通配符已归并到裸域）
res.SameSite("a.com")    // 同站 / 子域的来源
res.Reports()            // report-uri / report-to / reporting-endpoints 的上报端点
res.URLs()               // 可抓取的 URL（跳过通配与 scheme-only）
```

它是**纯函数库，不被采集流程自动调用**，也不会改动 `extractor` / `crawler` 的行为。
原因见下：

- 命中率一般：实测 `vuejs.org` 只有 `frame-ancestors`、`vite.dev` 与 `github.com` 没有 CSP；
- 噪音占比高：`developer.mozilla.org` 提取 48 个来源主机，其中同站仅 3 个；
  `cloudflare.com` 60 个里 59 个是第三方广告/分析域名；
- 所以「抓哪些」必须由调用方按自己的范围策略决定，而不是默认塞进抓取队列。

真正稳定的增量是**上报端点**（`report-uri` / `report-to`），
例如 Cloudflare 的 `https://a.nel.cloudflare.com/report/v4?...`——
这类接口在 HTML 与 JS 里都看不到。

**JS 引擎策略（`-js-engine`，默认 `auto`）**

| 取值 | 行为 |
|---|---|
| `auto`（默认） | goja 快路径优先，解析失败自动落到 treesitter |
| `goja` | 只用 goja；遇到 ESM 直接失败并回退正则 |
| `treesitter` | 只用 treesitter，最全但慢约 1.8 倍 |
| `regex` | 零依赖正则，最快，拿不到方法与参数 |

为什么默认是 `auto` 而不是单引擎：

- **goja 解析不了 ESM**（`import`/`export`/`import.meta`/顶层 `await`），
  而 Vite 构建的站点入口 chunk 就是 ESM——实测 `vuejs.org` 与 `vite.dev` 的入口文件
  让 goja 在第一个 token 就报错，整份文件会退化成正则。
- **treesitter 全能但慢**：1MB 输入约 3.1s（goja 1.7s），且语法表让二进制多约 7.7MB。
- 串联之后：常规脚本走快路径，ESM 走完整解析，两者都不牺牲。

goja 遇到 ESM 时**显式报错**而不是「剥掉 import/export 再试」——后者会让
`export default {a:"/x"}` 变成代码块解析成功，既不报错也提取不到内容，
自动兜底链根本不会触发。详见 `ESM-ANALYSIS.md`。

**入口补充**
`robots.txt`（Sitemap 声明）、`sitemap.xml`（递归处理 sitemap index、支持 gzip）、
自定义 `KnownFiles` 固定路径探测。

**调度**
分层 BFS（深度边界清晰）、URL 归一化去重、scope 三档
（同 host / 同根域 / 不限）、全局并发 + 单 host 并发 + QPS 限速、
手动跟随重定向、body 截断、gzip/deflate 透明解压、5xx 重试退避。

## 用法

```go
package main

import (
	"context"
	"fmt"

	"assetcrawlerer"
)

func main() {
	opts := assetcrawler.DefaultOptions()
	opts.MaxDepth = 3
	opts.Concurrency = 10
	opts.Scope = assetcrawler.ScopeSubdomain
	opts.OnResult = func(r assetcrawler.Result) {
		// 库内部已串行化回调，直接写文件不需要额外加锁
		fmt.Printf("%-60s %s %d\n", r.URL, r.Source, r.StatusCode)
	}

	c, _ := assetcrawler.New(opts)
	defer c.Close()

	results, _ := c.Crawl(context.Background(), "https://example.com")
	fmt.Println(len(results))
}
```

只想要提取器（不牵扯网络）：

```go
doc, _ := html.Parse(strings.NewReader(pageHTML))
for _, r := range extractor.FromHTML(doc, extractor.DefaultOptions()) {
    fmt.Println(r.Raw, r.Source, r.Method) // Raw 可能是相对路径，由你决定怎么解析
}
```

CLI（`cmd/assetcrawler`）：
```bash
# 只要 URL 列表
assetcrawler -u https://example.com -d 3 -o urls.txt

# JSONL，带来源与状态码
assetcrawler -u https://example.com -jsonl -o result.jsonl

# 收敛范围、加认证头、走代理
assetcrawler -u https://example.com -scope same-host -H "Cookie: sid=xxx" -proxy http://127.0.0.1:8080

# 补充固定路径探测
assetcrawler -u https://example.com -known-file /.git/config -known-file /actuator/env
```

常用参数：`-d` 深度、`-c` 并发、`-phc` 单 host 并发、`-rl` QPS、`-max-urls` 页面上限、
`-max-body` body 截断、`-scope`、`-no-js`、`-no-robots`、`-no-sitemap`、`-k` 跳过 TLS 校验、
`-include/-exclude` 正则过滤、`-v` 打印错误。

## 结果模型

```go
type Result struct {
	URL         string
	Method      string // 发现时推断的方法（form/hx-post 会是 POST）
	Source      string // seed | a | img | script | js | redirect | header:link | robots | sitemap | known-file
	Attribute   string // 该 URL 来自哪个属性
	Tag         string
	Depth       int
	StatusCode  int    // 0 表示未实际访问（如 robots 线索、含 EXPR 的模板）
	ContentType string
	Referer     string
	Confidence  string // 空 | template（含 EXPR，只输出不抓取）
	Kind        string // 空 | chunk（打包分片）| route（前端路由）
}
```

`Source` 值得保留：排查误报、评估某类提取是否有效，全靠它。

## 几个刻意的设计取舍

**robots.txt 的 Disallow 只输出、不抓取。**
`Status` 为 0、`Source` 为 `robots` 的条目就是这类。安全测试里 `/admin`、`/backup`
往往最值钱，但把它作为线索呈现、由使用者基于授权决定是否访问，比工具自作主张去请求要稳妥。

**JS 提取不做 AST 解析。**
只捞字面量，不还原字符串拼接（`"/api/" + id` 只能拿到 `"/api/"`）。做 AST 就要引
treesitter 这类重依赖，收益不抵成本。这也是本包能保持 1 个依赖的原因。

**URL 归一化偏保守。**
只做 scheme/host 小写、去默认端口、去 fragment、折叠 `../`。不排序 query、
不折叠路径大小写、不去尾斜杠——这些在真实站点上可能代表不同资源，过度归一化会漏资产。

**回调串行化。**
抓取是并发的，但 `OnResult` / `OnError` 在库内串行派发，调用方不用自己加锁。

## 不做什么（范围边界）

本包只解决「把一个站点上能静态发现的 URL 尽量收全」这一件事。以下明确不做，
需要时请配合专用工具，不要往里加：

| 不做 | 理由 / 替代 |
|---|---|
| 敏感信息提取（身份证、手机、邮箱、密钥、API key） | 属内容分析而非 URL 采集，规则演进快，交给 URLFinder 这类工具 |
| 目录重组 fuzz、猜路径 | 本质是穷举碰撞，请求量随目录层级立方增长，产出依赖数据形态 |
| 漏洞检测、指纹识别 | 不同的问题域 |
| 执行 JavaScript / 渲染 DOM | 需要无头浏览器，与本包的零依赖目标冲突 |
| 自动提交表单 | 只提取 `action` 与 `method`，是否发请求由使用者决定 |
| CSV / HTML 报告导出 | 结果以 `Result` 结构返回，序列化成什么由调用方决定 |

## 已知局限

- 不执行 JavaScript。纯前端渲染（内容完全靠 XHR 拉取、DOM 里没有链接）的页面挖不到，
  这类目标需要配合浏览器方案或在业务层直接打接口。
- JS 提取基于正则，会有误报（把非路径字符串当路径），也会有漏报（动态拼接的路径）。
- 不处理 WebSocket / GraphQL introspection / 前端路由表（如 `vue-router` 的
  `path: '/user/:id'` 里带参数的模板路由会被当成字面量过滤掉）。
- 不自动提交表单，只提取 `action` 与 `method`。
- 分层 BFS 每层之间有屏障，同一层的慢请求会拖住整层。追求极限吞吐时应改用连续 worker pool。

## 合规

仅用于**已获得书面授权**的目标。默认已开启限速与单 host 并发限制，
请勿把 `-rl 0`、`-max-urls 0` 同时用在别人的生产环境上。

## 测试

```bash
go test ./...              # 三个子包各自单测 + 一个 httptest 端到端用例
go test -race ./...        # 已验证无竞态
```

测试按层分布：`frontier` 测归一化与范围、`extractor` 测各类载体的提取、顶层测完整链路与配额。
