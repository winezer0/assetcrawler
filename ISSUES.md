# 现存问题清单

整理日期：2026-11-06 10:12
代码状态：`a3377c6`，本地 ahead 3（未推送）
说明：所有结论均来自本地实测；每条给出证据、影响与修法，成本为代码量级估计。

---

## 一、正确性（会让结果「看起来对但实际错」）

### P0-1 参数被写进 URL，产生源码里不存在的地址

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

**修法**：抓取始终用原始 URL；参数名只放 `Params` 字段。
需要展示「带参形态」时另设字段（如 `URLTemplate`），不参与抓取。
成本：约 30 行，改动点集中在 `jsengine.go:withParams` 与调用处。

### P0-2 sitemap 挤占抓取配额，资源类 URL 拿不到

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

**修法**（需定策，三选一）：
- sitemap 条目降为 `depth 1`，与资源同层竞争；
- `MaxURLs` 只统计「非 sitemap 来源」的抓取；
- 增加 `-no-sitemap`，或让 `FollowSitemap` 默认在配额足够时才开。

成本：10~20 行，但要先确定语义。

### P0-3 `fetcher` 383 行零测试

**证据**：`go test -cover ./fetcher/` → 无测试文件；`cmd/assetcrawler` 同样为零。

`fetcher` 承担：重定向链、5xx 退避重试、QPS 限速、按 host 并发、body 截断、超时取消。
这些全部只靠端到端用例间接覆盖，改任何一处都没有回归保障。

**影响**：限速或并发写错不会让测试变红，只会在真实站点上表现为「把目标打挂」或「莫名其妙超时」。

**修法**：6 个用例（重定向计数、重试次数与退避、上限保护、限速精度、
同 host 并发上限、body 截断）。成本：约 2 小时。

---

## 二、召回与准确（能力缺口）

### P1-1 六类 URL 载体未覆盖

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
价值明显高于其余四项。合计成本约 120 行。

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

### P1-6 真实站点样本太少，没有可回归基线

目前只有 `testasp.vulnweb.com` 一个基线，且它是 2000 年代的 ASP 站。
现代站点只在 2026-11-06 对 `vuejs.org` 抽测过一次（16 个 JS 全 200），**结果没有固化**，
下次改动无法判断是变好还是变坏。

**修法**：把 `vuejs.org` 的抽测结果按 `tools/baseline/` 的格式存一份；
再补一个 Vite 站点（`vite.dev`）与一个传统站。成本：半天。

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

`-route-exec` 在默认构建下 exit 2 这条行为，只有手工验证过一次。
flag 组合（`-no-js` / `-no-chunk-graph` / `-js-engine` 与 Options 的映射）
没有任何自动化覆盖。

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
| 不执行 JS 渲染 DOM | 与零浏览器目标冲突；`routeexec` 只执行 webpack chunk 取路由 |
| 不做目录 fuzz / 猜路径 | 笛卡尔积穷举，请求量立方增长 |
| 不做敏感信息提取 | 内容分析，非 URL 采集 |
| `template` 结果不入队 | 含 `EXPR` 的地址请求必然 404 |
| 带 `:param` 路由不抓 | 见 P1-3，取舍本身合理，缺的是开关 |

---

## 建议处理顺序

1. **P0-1** 参数不要写进 URL（30 行，直接关系结果可信度）
2. **P0-2** sitemap 与配额（需先定语义，10~20 行）
3. **P0-3** fetcher 单测（2 小时，保护后续所有改动）
4. **P1-6** 固化一个现代站基线（否则无法验证 P1 系列改动）
5. **P1-1 CSP** 优先，其余打包
6. P1-2 / P1-3 / P1-4 视真实数据决定投入
7. P2 系列随手清理
