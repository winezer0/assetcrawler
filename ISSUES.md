# 现存问题清单

整理日期：2026-11-06 10:12；更新 12:50
代码状态：P0-1 / P0-2 / P0-3 已修复（见各条状态）
说明：所有结论均来自本地实测；每条给出证据、影响与修法，成本为代码量级估计。

---

## 一、正确性（会让结果「看起来对但实际错」）

### P0-1 参数被写进 URL，产生源码里不存在的地址（✅ 已修复）

**证据**：直接调用适配器实测

```
axios.get("/api/list",{params:{page:1,size:20}})   →  /api/list?page=&size=
$.get("/api/list2",{page:1})                       →  /api/list2?page=
axios.post("/api/create",{name:"x",age:2})         →  /api/create（未改写）
```

`withParams()` 把只有名字、没有值的参数补成 `?page=`，然后这个地址**会被真的抓取**。

**影响**：这是源码里从未出现的 URL。
- 严格校验的接口会返回 400；
- 分页接口可能把 `page=` 当成空值从而返回第一页（与真实调用结果不同）；
- 缓存/风控行为也可能不同。

与我们「挖掘准确的 URL」的目标直接冲突。

**已修**：`adaptEngine` 直接用 `r.URL`，删除 `withParams`。参数名只放 `Params`。
测试：`TestParamsDoNotRewriteURL`（断言 URL 不含 `?`，且 params 有两个名字）。

### P0-2 sitemap 挤占抓取配额，资源类 URL 拿不到（✅ 已修复）

**证据**：`vuejs.org` 的 sitemap 有 59 条，全部以 `depth 0` 入队
（`bootstrap.go:40` 固定 `depth: 0`），与种子页同层。

| 配额 | 结果 |
|---|---|
| `-max-urls 60` | 60 条全是 sitemap 页面，**JS 数 0** |
| `-max-urls 300` | 16 个 JS 全部 200（含 9 个 chunk） |

`reserveFetch()` 对每次抓取计数，sitemap 页面把额度用完后，第 0 层还没走完，
第 1 层（种子页里提取出的 JS/CSS）根本不会执行。

**影响**：sitemap 丰富的站点，在小配额下看不到任何静态资源与接口，
而这恰恰是资产收集最关心的部分。用户会误以为「这个站没有 JS」。

**已修**：采用第一种——sitemap 页面放 `depth 1`，且引导任务在第 0 层跑完后才并入，
排序上排在种子页提取出的资源之后（配额紧张时优先抓 JS/CSS）。

实测对比（`vuejs.org`，`-max-urls 60`）：

| | JS 文件 | 200 | sitemap 页面 |
|---|---:|---:|---:|
| 修复前 | **0** | 0 | 59 |
| 修复后 | **10** | 10 | 8 |

测试：`TestSitemapDoesNotStarveAssets`（40 条 sitemap + 配额 16，断言 `/app.js` 被抓到）。

### P0-3 `fetcher` 383 行零测试（✅ 已修复）

**证据**：`go test -cover ./fetcher/` → 无测试文件；`cmd/assetcrawler` 同样为零。

`fetcher` 承担：重定向链、5xx 退避重试、QPS 限速、按 host 并发、body 截断、超时取消。
这些全部只靠端到端用例间接覆盖，改任何一处都没有回归保障。

**影响**：限速或并发写错不会让测试变红，只会在真实站点上表现为「把目标打挂」或「莫名其妙超时」。

**已修**：7 个用例，覆盖率 0% → **74.3%**：

| 用例 | 断言 |
|---|---|
| `TestRedirectChain` | 两跳链、最终 URL、`Redirects` 两条都在 |
| `TestRedirectLimit` | 上限生效、不死循环 |
| `TestRetryOn5xx` | 5xx 重试到成功（请求 3 次）；404 不重试且不报错 |
| `TestRateLimit` | QPS=5 时 10 请求 ≥ 0.8s |
| `TestPerHostConcurrency` | 峰值并发不超过 `PerHostConcurrency` |
| `TestBodyTruncation` | 超限时 `len(body)==limit` 且不报错 |
| `TestContextCancel` | 取消能中断在途请求 |

顺带确认了一个既有语义：**404 不返回错误**（状态码本身是证据），
只有 5xx 重试耗尽才返回 `StatusError`。

---

## 二、召回与准确（能力缺口）

### P1-1 六类 URL 载体未覆盖（CSP 部分已实现为独立包）

探针 13/19，剩余 6 项：

| 载体 | 示例 |
|---|---|
| `meta[property=og:url]` | `/shared/og-page` |
| `meta[name=twitter:image]` | `/img/twitter-card.png` |
| `data-*` 自定义属性 | `/api/from-data-attr` |
| CSP `report-uri` | `/csp-report-endpoint` |
| CSP `connect-src` | `https://api.internal.example.com` |
| sourcemap `sources` | `/src/api/order.js` |

其中 **CSP 是唯一能发现新域名的载体**（`connect-src` 常直接列出 API 域名），
价值明显高于其余四项。

**CSP 已实现（2026-11-06）**：独立的 `csp` 包，纯函数，不被采集流程调用。
实测增量与噪音：

| 站点 | 来源主机 | 同站 | 上报端点 |
|---|---:|---:|---:|
| developer.mozilla.org | 48 | 3 | 0 |
| cloudflare.com | 60 | 1 | 1（NEL） |
| vuejs.org | 6 | 2 | 0 |
| vite.dev / github.com | 无 CSP | — | — |

结论：域名部分同站比例低、第三方噪音高，**只作为工具函数供调用方按范围策略使用**；
上报端点是稳定增量。因此本项不再计入「采集流程应自动覆盖」的缺口。

剩余未覆盖：og/twitter meta、`data-*`、sourcemap `sources`。

### P1-2 配置对象里的 base 地址变成 404

**证据**：vue 夹具实测输出里有

```
/api/v3     404
/static/    404
```

来源是 `window.__ENV__={VUE_APP_API:"/api/v3", STATIC:"/static/"}` —— 对象属性值
没有走「前缀 / 端点」的形状判定（该判定只用在裸变量赋值上）。

**影响**：真实站点每个配置块会贡献 1~3 条 404，轻微但可感知。
**修法**：把 `endpointShaped` 也用于对象属性值，但会误伤 `{plain:"/api/in-js-file"}`
这类两段路径——需要先定规则（例如只排除以 `/` 结尾的、或段数 ≤ 2 且无扩展名的）。
成本：约 20 行 + 规则讨论。

### P1-3 带参数的前端路由只报告不抓取

`/user/:id` 这类路由现在 `kind=route`、`status=0`，不发起请求（避免 SPA 重复抓首页）。
代价是**SPA 的深层页面完全拿不到**——而它们往往是 API 调用最集中的地方。

**现状是刻意取舍，但缺一个开关**。可选：`-route-expand` 用占位值（`/user/1`）试抓，
带上限与去重。成本：40 行，噪音需实测。

### P1-4 参数只有名字，没有值

`Params` 里只有 `page`、`size`，没有取值。但表单里有一批**静态可取**的值：
`<input type="hidden" value="12">` 是确定值，`<select>` / `radio` 的候选集合很小。
目前完全没用上。

**修法**：保守版只输出 `sort=price_asc|newest` 这类候选，不生成 URL。成本：60 行。

### P1-5 treesitter 的成本未优化

| 项 | 数值 |
|---|---|
| 二进制 | 15.21 MB（含两套引擎，treesitter 约占 7.7MB） |
| 1MB JS 端到端 | goja 1.7s / treesitter 3.1s |

ESM 必须靠它（`ESM-ANALYSIS.md`），但不能删。可优化方向是「只编 JS 语法的裁剪构建」，
未做评估。

### P1-6 真实站点样本太少，没有可回归基线（✅ 已修复）

目前只有 `testasp.vulnweb.com` 一个基线，且它是 2000 年代的 ASP 站。
现代站点只在 2026-11-06 对 `vuejs.org` 抽测过一次（16 个 JS 全 200），**结果没有固化**，
下次改动无法判断是变好还是变坏。

**已修**：在 `tools/baseline/` 下新增两份现代站基线
（`vuejs.org-2026-11-06`、`vite.dev-2026-11-06`），并在同一目录写了 MANIFEST。

现代站的文件名带内容哈希，每次部署都变，因此额外产出
`urls-stable.txt`（把 `.<hash>.` 归一成 `.<HASH>.`）用于回归对比；
`urls-all.txt` 只在同一次部署内可比。

**采集当天就抓出三类噪音，已修复**（见下）：

| 站点 | 修复前 404 | 修复后 |
|---|---:|---:|
| vuejs.org | 32 | **8** |
| vite.dev | 11 | **3** |

### P1-7 三类误报（✅ 已修复，2026-11-06）

由新基线发现，均为真实站点上的命中：

| 现象 | 根因 | 修法 |
|---|---|---|
| `/assets/%23SVGxxx` 大量 404 | data URI 内联 SVG 里的 `url(%23filter)` 是同文档引用，被当成资源路径 | CSS 引用过滤补 `#` 与 `%23` |
| `/assets/chunks/assets/chunks/x.js` | `__vite__mapDeps` 元素是相对站点根的 `assets/chunks/x.js`，被按模块文件解析 | 数组元素补前导斜杠 |
| `/framework.js`、`/assets.LcogJ1e5.js` | mapDeps 扫描了全文件字符串，把 `from"./framework.js"` 也锚定了 | 限定在 `__vite__mapDeps` 的数组字面量内 |
| `/assets/text/plain`、`/modulepreload`、`/assets/Running...`、`/*` | JS 里的 MIME、link rel、UI 文案、通配符被当成路径 | 新增 `nonURLLiteral` 过滤 |

三条都有单测：`TestCSSFragmentRefSkipped`、`TestViteMapDepsScopedToArray`、
`TestNonURLLiterals`。

修复后 JS 文件的 200 命中率：vuejs.org 17/17、vite.dev 38/38。

---

## 三、工程与文档

### P2-1 内存没有上限

`results` 与 `seen` 全常驻内存。1MB 输入在 routegraph 里约 6.7MB 分配（已优化过），
但默认 `Concurrency=10` × `MaxBodySize=2MB` × 语法树的峰值**从未测量**。
10 万 URL 规模下的实际占用是未知数。

### P2-2 基线原始输出已丢失

`tools/baseline/testasp-2026-11-06/raw/` 为空（我早期误删）。
`baseline.py diff` 仍可用（依赖归一化后的 `urls-*.txt`），但**换口径重新归一化**和
「原始证据复查」的能力没有了。MANIFEST 已注明，未重建。

### P2-3 CLI 零测试

flag 组合（`-no-js` / `-no-chunk-graph` / `-js-engine` / `-route-exec` 与 Options 的映射）
没有任何自动化覆盖，只有手工验证。

### P2-4 覆盖率数字容易被误读

`go test -cover ./...` 显示 `jsext/goja`、`jsext/treesitter` 都是 0%，
但这只是统计口径（用例写在 `jsext` 包做黑盒测试）。用 `-coverpkg` 实测是 **80.6%**。
新人或 CI 阈值判断会被误导。

### P2-5 文档里的数字会自然过期

`STATUS.md`、`ROADMAP.md` 里写死了行数、覆盖率、提交数、二进制体积。
每次改动都会让它们失真（这次审计时已经发现「未推送 12 个」与实际不符）。
建议把可变量改成生成脚本，或只保留「日期 + 结论」，不写具体数字。

### P2-6 三个提交未推送

`ahead 3`：ESM 调研与引擎串联、ROADMAP、STATUS 同步。

---

## 四、明确不算问题的设计取舍

以下是有意为之，不再讨论：

| 项 | 理由 |
|---|---|
| 不执行 JS 渲染 DOM | 与零浏览器目标冲突；执行兜底只跑 webpack chunk 取路由 |
| 不做目录 fuzz / 猜路径 | 笛卡尔积穷举，请求量立方增长 |
| 不做敏感信息提取 | 内容分析，非 URL 采集 |
| `template` 结果不入队 | 含 `EXPR` 的地址请求必然 404 |
| 带 `:param` 路由不抓 | 见 P1-3，取舍本身合理，缺的是开关 |

---

## 处理进度

| 顺序 | 项 | 状态 |
|---|---|---|
| 1 | P0-1 参数不要写进 URL | ✅ 2026-11-06 |
| 2 | P0-2 sitemap 与配额 | ✅ 2026-11-06 |
| 3 | P0-3 fetcher 单测 | ✅ 2026-11-06（覆盖率 74.3%） |
| 4 | P1-6 固化现代站基线 | ✅ 2026-11-06（vuejs.org / vite.dev） |
| 4.5 | P1-7 三类误报 | ✅ 2026-11-06（404：32→8 / 11→3） |
| 5 | P1-1 CSP，其余打包 | 待做 |
| 6 | P1-2 / P1-3 / P1-4 | 视真实数据决定 |
| 7 | P2 系列 | 随手清理 |
