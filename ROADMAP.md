# 下一步任务规划

日期：2026-11-06
基线：`5a54566`，已推送，工作区仅 `STATUS.md` 未提交

## 现状摘要

| 项 | 值 |
|---|---|
| 覆盖率 | crawler 71.3% / extractor 80.7% / frontier 77.2% / jsext 80.6% / routegraph 82.1% |
| 零覆盖 | `fetcher`（383 行）、`cmd/assetcrawler` |
| 二进制 | 默认 15.22 MB；`-tags routeexec` 23.70 MB |
| 探针 | 13/19 载体、方法 5/5、参数 3/3、噪音 0（**仅限 `-js-engine goja`**） |
| 未覆盖载体 | og/twitter meta、data-*、CSP×2、sourcemap sources |

---

## P0：三件事，先做

### P0-1 默认引擎改为 auto（✅ 已完成 2026-11-06）

原问题是默认值是 `regex`，AST 的全部收益在默认使用下拿不到（探针 11/19）。
调研 ESM 后没有简单地「改成 goja」，而是做成 **goja → treesitter 串联**：

| 配置 | 载体 | 方法 | 参数 | 噪音 |
|---|---:|---:|---:|---:|
| 原默认（regex） | 11/19 | 2/5 | 1/3 | 4 |
| 现默认（auto） | **13/19** | **5/5** | **3/3** | **0** |

原因见 `ESM-ANALYSIS.md`：Vite 站点入口 chunk 是 ESM，goja 100% 解析失败，
而 treesitter 全能但慢。串联后两者都不牺牲。

### P0-2 真实站点验证（✅ 部分完成 2026-11-06）

**问题**：所有 SPA 验证都基于 `testdata/spa_fixture.py` / `vue_fixture.py` /
`dynamic_fixture.py`，而这些夹具是**照着实现写的**。存在确认偏误：
真实 webpack 产物里 `__webpack_require__.u` 有 webpack 4/5 两种形态、
chunk 可能分散在多个 runtime、Vite 产物完全另一套（`__vitePreload` + `mapDeps`）。

我们至今没在**真实的现代前端站点**上验证过 chunk/route 提取到底能不能命中。

**动作**：
1. 选三类目标各 2 个（公开、可礼貌爬取）：
   - 现代 SPA 生产站（Vue/React + webpack5、Vite 各一）
   - 传统服务端渲染站（ASP/PHP，已知 `testasp` 为基线）
   - 静态文档站（`go.dev` 之类）
2. 每个目标记录：chunk 还原命中率（构造出的文件名有多少返回 200）、
   route 条数、参数条数、方法分布、耗时、噪音率（非 200 占比）
3. 把真实形态与夹具的差异写成「夹具缺失清单」，回填到 `testdata/`

**验收**：产出 `VALIDATION.md`，含每站点的命中率表格与差异清单；
若 chunk 命中率低于 50%，优先修还原逻辑而不是继续加新功能。

**成本**：半天（主要是挑选目标与限速爬取）。

**已完成部分（2026-11-06）**：对 `vuejs.org` 实跑深度 2、300 配额，16 个 JS 全部 200，
其中 9 个 `./chunks/*.js` 由 ESM 入口的 `export ... from` 还原。
发现一个调度问题（见 P0-4）。

### P0-3 `fetcher` 补单测（383 行零覆盖）

**问题**：`fetcher` 承担重定向链、5xx 退避重试、QPS 限速、按 host 并发、
body 截断、超时取消——全部靠端到端间接覆盖。改任何一处都没有回归保障。

**要覆盖的六条**：
1. 重定向链记录完整（含跨 host 时不带上 Authorization）
2. 5xx 重试次数与退避间隔（用 httptest 计数验证实际请求次数）
3. `MaxRedirects` 超限返回错误而非死循环
4. 限速：`RateLimit=4` 时 8 个请求耗时落在合理区间
5. 按 host 并发上限：慢响应下同 host 并发数不超过配置
6. body 截断：超过 `MaxBodySize` 时只读上限字节且不报错

**验收**：`fetcher` 覆盖率 ≥ 70%；`go test -race ./fetcher/` 通过。

**成本**：2 小时。

### P0-4 sitemap 挤占配额（新发现）

`vuejs.org` 该站 sitemap 有 59 条，全部以 depth 0 入队。`-max-urls 60` 时
**整份配额被 sitemap 吃光**，种子页 `<script src>` 提取出的 JS 一条都没轮到，
结果里 JS 数为 0。

同样是 60 配额，未加 body 提取前能拿到 60 条 sitemap 页面；加了之后什么都拿不到——
配额语义从「页面数」变成了「含 sitemap 的全部 URL 数」。

**可选修法**（需定策）：
- sitemap 条目降级为 depth 1，与种子页提取出的资源同层竞争；
- 或 `MaxURLs` 只统计「实际抓取」的数量，sitemap 线索不占额度；
- 或加 `-no-sitemap` 开关，默认在大配额时才启用。

---

## P1：召回补齐（合计约 120 行，探针从 13/19 → 19/19）

| 项 | 价值 | 成本 | 说明 |
|---|---|---|---|
| **CSP / Report-To 响应头** | **高** | 30 行 | `connect-src` 常直接列出 API 域名（可能不同子域），`report-uri` 是现成端点。这是少数能发现**新域名**的载体 |
| og / twitter meta | 中 | 25 行 | 现有 `meta` 只认 refresh。`og:url` 常指向分享页变体 |
| `data-*` 泛化 | 中 | 20 行 | 现在只认 `data-src`。取所有 `data-*` 值里像路径的，需限流避免误报 |
| sourcemap `sources` | 低 | 45 行 | 已会抓 `.map` 但不解析。`webpack://` 伪路径不可访问，只有相对路径有用，命中率预计偏低 |

顺序：CSP 先做（唯一能扩域名的），其余打包做。

**验收**：`check.py` 19/19、`expected.json` 全部 `implemented: true`；基线无回退。

---

## P2：参数深化（对「有参数」目标的直接投入）

当前 `params` 只有名字没有值——真实值要么客户端生成、要么需交互。
但**表单里其实有一批静态可取的值**：

### P2-1 表单参数值枚举

```html
<select name="sort"><option value="price_asc">...</option><option value="newest">...</option></select>
<input type="radio" name="type" value="sale">
<input type="hidden" name="cat" value="12">
```

`hidden` 的 `value` 是确定的默认值，`select`/`radio` 的候选值集合很小。
可以产出「带候选值的参数形状」，或按开关生成具体 URL（如 `/list?sort=newest&cat=12`）。

**必须先定策略**，两种取向：
- **保守（推荐）**：只输出 `params` 里带候选值（`sort=price_asc|newest`），不生成 URL。
  零额外请求、零误报，人工判读时直接可用。
- **激进**：对 `hidden` 的确定值直接合成 URL 入队。多抓一批真实页面，但可能撞上
  需要会话的路径，产生大量 302。

建议先做保守版，把激进版做成 `-param-expand` 开关单独验证。

**成本**：保守版 60 行；激进版再加 50 行。

### P2-2 分页有限推进

JSON 的 `next` 已能拿到（P0-3 完成）。剩下的是 `?page=N` 的有限推进：
需开关 + 上限（例如最多 5 页），且只在 `page`/`p`/`offset` 参数存在时启用。
**风险**：容易把目标压垮，也可能陷入无限翻页。必须带页数上限与「内容重复即停」判定。

**建议**：放最后，先看 P0-2 的真实站点数据里分页到底占多少召回。

---

## P3：工程收敛

### ~~P3-1 双引擎去留~~（❌ 撤回）

原判断「treesitter 只剩原生 ESM 解析，可移除省 8MB」是错的，实测推翻了它：

- goja 在 12 个 ESM 用例中 6 个解析失败；两个真实 Vite 站点入口 chunk **100% 失败**。
- treesitter 还提供**截断容错**：2MB body 上限下大 bundle 必被切断，
  goja 全有或全无，treesitter 能提取已下载部分（截断 49% 时仍拿到 102 条）。

所以 treesitter 不能移除。要减体积的正确方向是「只编 JS 语法的裁剪构建」，
不是删掉它。详见 `ESM-ANALYSIS.md`。

### P3-2 CI

GitHub Actions：`gofmt -l`、`go vet`、`go test -race`、`-tags routeexec` 构建、
探针夹具（起 Python 桩站跑 `check.py`）。目前所有验证都是手工跑的，容易漏。

### P3-3 大站点内存

`seen` 与 `results` 常驻内存。10 万 URL 时 `seen` 约几十 MB，
`results` 若带 body 摘要会更大。目前没炸，但缺一个上限与溢出策略（可选落盘）。

---

## 明确不做（已定边界，不再讨论）

- 敏感信息提取、目录 fuzz、漏洞检测、指纹识别
- 执行 JS 渲染 DOM（`routeexec` 只执行 webpack chunk 取路由，不渲染）
- CSV/HTML 报告导出（返回 `Result`，序列化交给调用方）

---

## 建议执行顺序

```
P0-1 默认引擎（5 分钟）→ 立刻让默认体验追上已实现的能力
P0-2 真实站点验证（半天）→ 用真实数据校正后续优先级
P0-3 fetcher 单测（2 小时）→ 为后面的改动兜底
P1   CSP 等四项（半天）→ 探针补满
P2-1 参数值枚举·保守版（2 小时）
P3-1 移除 treesitter（前置：P0-2 验证 ESM）→ 体积减半
P2-2 分页 / P3-2 CI / P3-3 内存 → 视真实数据再定
```
