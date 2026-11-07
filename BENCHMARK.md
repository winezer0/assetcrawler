# URL 采集工具横向对比

> 本文是一次性的分析结论。**可复现的基线文本与对比脚本已入库 `tools/`**
> （`tools/baseline/testasp-2026-11-06/`、`tools/baseline.py`），回归对比请用
> `tools/baseline.py diff`，不要以本文的数字作为判据——目标站点内容会漂移。
> 5 个对比工具的二进制约 91 MB，只做本地离线存档、不入库，校验和见
> `tools/README.md`。

测试日期：2026-11-04
目标：`http://testasp.vulnweb.com`（Acunetix 公开测试靶站）
运行环境：Windows / Go 1.26.4

## 测试设置

统一参数：深度 3、并发 10、超时 10s。

| 工具 | 版本 | 命令 |
|---|---|---|
| hakrawler | latest | `echo http://testasp.vulnweb.com \| hakrawler -d 3 -t 10 -timeout 10 -u -s` |
| gospider | v1.1.6 | `gospider -s http://testasp.vulnweb.com -d 3 -c 10 -m 10` |
| cariddi | v1.4.6 | `echo http://testasp.vulnweb.com \| cariddi -md 3 -c 10 -t 10 -plain` |
| URLFinder | v2026.6.16 | `URLFinder -u http://testasp.vulnweb.com -m 2 -s all -t 10 -time 5` |
| assetcrawler | 本地 | `ac -u http://testasp.vulnweb.com -d 3 -c 10 -timeout 10 -jsonl` |

统计口径：剥离 ANSI 颜色码后正则提取 `http(s)://`，去 fragment；
分「站内 / 外链」两栏，避免被导航栏的外部链接冲高总数。

## 结果

| 工具 | 唯一 URL | 站内 | 外链 | 耗时 |
|---|---:|---:|---:|---:|
| **assetcrawler** | **44** | **44** | 0 | 23s |
| cariddi | 37 | 37 | 0 | 6s |
| hakrawler | 35 | 30 | 5 | 6s |
| URLFinder `-m 2` | 504 | 31 | 473 | 190s |
| URLFinder `-m 3` | — | 30 | — | 174s |
| URLFinder `-m 1` | 16 | 11 | 5 | 9s |
| gospider | — | — | — | 运行失败 |

再做一次宽松归一化（percent-decode + 折叠 `../` + 去尾斜杠），排除同一资源的不同写法：

| 工具 | 站内唯一资源 |
|---|---:|
| **assetcrawler** | **44** |
| cariddi | 37 |
| hakrawler | 30 |
| URLFinder `-m 2` | 28 |
| 合并去重 | 49 |

## 各工具的实际表现

**assetcrawler**：站内 44 条，全部命中，漏抓的真实资产为 0。
相对其他工具只少两类：`robots.txt` / `sitemap.xml` 文件本身（本实现把它们当基础设施，不作为结果输出），
以及 URLFinder 那 3 条 `text/css` 误报。同时会输出 1 条 404 误报（`/x`，来自页面里 `<img src="x">`）。

**cariddi**：37 条，速度最快（6s），没有误报、没有外链。
它的特点是会把 `robots.txt`、`sitemap.xml` 本身也列进结果。默认不做敏感信息提取。

**hakrawler**：30 条，完全被 assetcrawler 覆盖（0 条独占）。
只走 `a[href]` 为主，深度够了但不去碰 JS 和注释，结果干净但覆盖有限。

**URLFinder `-m 2`**：数字看着最大（504），但**外链占 473 条**，站内只有 31 条。
外链主要来自顺着第三方 JS 和导航跑到 acunetix.com（268 条）、trendemon.com（76 条）、
geojs.io（25 条）等域外站点。站内 31 条里还包含 3 条从 `Content-Type: text/css`
误提取的假路径，以及 3 条未归一化的 `Templates/../Default.asp` 重复项。
归一化后实际有效站内资源 28 条，低于 assetcrawler。耗时 190s。

**gospider**：在本环境完全不可用。用 depth=1/3、并发 1/5/10、`-q`、`--js=false`、
`-o` 目录输出共 5 种组合尝试，均零输出、零文件、无报错，直到超时被杀。
`--version` 正常，判断为 Windows 下的运行时问题，未计入对比。

### URLFinder 三个模式的差异

模式只影响「是否递归」，不改变提取规则：

```go
// crawler/find.go —— 递归开关同时依赖 num 与 M
if num <= config.UrlSteps && (cmd.M == 2 || cmd.M == 3) { go Spider(url, num+1) }
if num <= config.JsSteps  && (cmd.M == 2 || cmd.M == 3) { go Spider(js,  num+1) }

// config/config.go —— 默认层数
UrlSteps = 1
JsSteps  = 3
```

| 模式 | 行为 | 站内唯一 | 原始条数 | 耗时 |
|---|---|---:|---:|---:|
| `-m 1` normal | 只抓种子页，提取后不跟进 | 11 | 28 | 9s |
| `-m 2` thorough | URL 深入 1 层 + JS 深入 3 层 | 31 | 819 | 190s |
| `-m 3` security | 同 m2，额外跳过含危险词的 URL | 30 | 825 | 174s |

`-m 3` 的过滤在 `crawler/crawler.go` 的 `Spider()` 入口处，用
`strings.Contains(u, v)` 对**整条 URL 做子串匹配**，命中直接 return：

```go
Risks = []string{"remove", "delete", "insert", "update", "logout"}
```

这意味着被拦的 URL 连结果都不会出现，且只读接口（如 `/api/query?action=update`）也会被误杀。
本次实测 m3 比 m2 少 1 条（`showthread.asp?id=4`），但该 URL 并不含任何 Risks 关键词，
判断为并发时序波动，而非过滤所致——也就是说这个靶站上没有触发过滤。

另外两处实现细节值得注意：

- `Spider()` 入口执行 `url.QueryUnescape(u)`，输出的是**解码后**的 URL，
  所以结果是 `Login.asp?RetURL=/Default.asp?` 而不是原始的 `%2FDefault%2Easp%3F`。
  解码后可能改变原 URL 语义。
- `AppendEndUrl(u)` 在发请求**之前**就标记为已抓取，请求失败后不会重试，也不会出现在结果里。

`-m 1` 与 `-m 2` 的差距不只是数量：m2 的 819 条原始记录里只有 31 条站内，
递归会顺着首页的导航链接跑到 acunetix.com 等外部站点，这是它耗时从 9s 涨到 190s 的主因。

## 本次测试发现并修复的缺陷

对比过程中发现 assetcrawler 漏掉了 `http://testasp.vulnweb.com/Templates/MainTemplate.dwt.asp`（200，2488 字节）。
追查后确认该路径写在首页的 Dreamweaver 注释里：

```html
<html><!-- InstanceBegin template="/Templates/MainTemplate.dwt.asp" codeOutsideHTMLIsLocked="false" -->
```

原实现只遍历元素节点，不解析 HTML 注释，因此整类资产被漏掉。
已补 `extractor.fromComments()`，覆盖 SSI 的 `#include virtual|file`、Dreamweaver 模板声明、
以及注释中带扩展名的绝对路径，并加了单测。修复后站内结果从 38 条升到 44 条，
并顺带从该模板页又发现 2 条新的 404 路径。

## 结论

单纯比「站内 URL 覆盖」，assetcrawler 最高（44），且没有真实漏抓。
URLFinder 的总数最大但被外链稀释，实际站内覆盖最低。

但两件事要分开说：

- **URL 提取与范围控制**：assetcrawler 更好。scope 用 publicsuffix 取根域，不会跑飞；
  路径会归一化，不会把 `Templates/../Default.asp` 当成新资产。
- **功能完整度**：URLFinder 更全。它有敏感信息提取（身份证/手机/邮箱/API key 等可配置正则）、
  CSV/JSON/HTML 报告导出、YAML 驱动规则。这些 assetcrawler 都没有，且已明确不在范围内
  （见 README 的「不做什么」），不做追赶。
  （它的 404 目录重组 fuzz 同理不计入对标：本质是穷举目录段与文件名的笛卡尔积，
  请求数随目录层级立方增长，且产出依赖数据形态，不属于「URL 采集」的能力范畴。）

所以选型取决于用途：要做**可集成的 URL 采集组件**用 assetcrawler；
要做**人工排查敏感信息的一次性作业**，URLFinder 的现成规则和报告更省事。
两者也可以串联——用 assetcrawler 拿干净的 URL 全集，再喂给 URLFinder 做敏感信息扫描。

