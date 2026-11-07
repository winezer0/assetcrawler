# 实施情况

日期：2026-11-07
基线提交：`d17a6a4`（Next.js App Router 的 RSC 流解析）
本轮：批次 **E**（正确性、测量口径、文档）与批次 **F**（Nuxt / 文件路由框架召回）

本文只写**结论与判据**。行数、用例数、覆盖率、二进制体积这类每次提交都会变的
数字，一律由 `python tools/metrics.py [--cover]` 生成，不再写死（ISSUES P2-5）。
所有实测数字都注明参数与口径；没有实测的地方会明确标注。

---

## 一、当前状态

- 直接依赖 3 个：`dop251/goja`、`odvcencio/gotreesitter`、`golang.org/x/net`。
- 包结构与单向依赖见 README「包结构」；`extractor` 通过 `Options.JSExtract`
  注入引擎，自身不 import 任何引擎。
- `csp` 包解析响应头；**编排层会调用它输出线索**（`crawler.go` → `emitCSPHints`），
  线索只报告不入队。早期「纯函数库、不被采集流程调用」的说法已作废。
- 可复现基线、对比脚本与原始 JSONL 已入库 `tools/`；5 个第三方二进制仍只做
  本地离线存档（见 `tools/README.md`）。
- CI：`.github/workflows/ci.yml`，六步：gofmt / vet / race / `-s -w` 构建 /
  探针夹具 / 探针自身的身份校验回归。
- 本地验证：`go test -race ./...`、
  `python testdata/probe/check.py --bin assetcrawler.exe`、
  `python -m unittest discover -s testdata/probe -p 'test_*.py' -v`。

覆盖率口径提醒（ISSUES P2-4）：`go test -cover ./...` 里 `jsext/goja` 与
`jsext/treesitter` 显示 0% 只是「用例写在 jsext 包做黑盒测试」的统计假象，
真实覆盖要看 `-coverpkg`。

---

## 二、本轮实施内容（批次 E / F）

| 项 | 做法 | 验证 / 判据 |
|---|---|---|
| **E-1 探针身份校验** | `common.py` 共享标记；`server.py` 加 `/__probe_ping` 并支持端口参数；`check.py` 校验标记、默认端口占用时自动换空闲端口、支持 `--port` | 本机 18080 被别的桩服务占用时，旧版报「1/19 假回退」，新版自动换端口后 20/20；4 个负向用例（含 CLI 级）全绿 |
| **E-2 RSC 分派回退** | `fromInlineScript` 统一分派内联脚本；`FromNextFlight` 为空时回退 `FromJS`；`nextFlightPatterns` 表驱动支持 `(self.__next_f=...).push` | 新增 2 个用例：初始化式 payload 能解析、只含初始化器的脚本不丢普通接口路径 |
| **E-2 AST / 正则取舍** | 用 A/B 实测裁定 `FromJS` 是否叠加正则，并统一 `js.go` 与 `Options` 两处互相矛盾的注释 | 叠加后探针 4 条已知噪音（`/api/`、`/config`、`/v2`、`/orders`）全部反弹，goja 档还丢 1 条真实 URL → **保持不叠加** |
| **E-3 文档与基线** | 基线文本 / 脚本入库；新增 `tools/metrics.py`；重写 STATUS / README / ISSUES / ROADMAP / CI 注释里的过期结论 | 文档数字改为生成式；`tools/` 路径在 clone 后真实可用 |
| **F-1 内联 hydration JSON** | 新增 `extractor/inlinejson.go` + `inline.go`：表驱动识别 `__NUXT_DATA__` / `__NEXT_DATA__` / `__INITIAL_STATE__`，按 JSON 解码（自动处理 `\u002F`），保守形状判定、200 条上限、独立 `inline-json:*` source；**与原有 `FromJS` 结果合并**而非二选一 | 真实 nuxt.com 首页（201 KB payload）单次 `FromHTML` 产出 200 条站内路径；探针新增 1 个载体，20/20；端到端 A/B 零回归（见第四节） |
| **F-2 chunk 相对路径修复** | `normalizeChunkPath`：`./x.js` 保留相对形态交给 frontier 按当前 JS 的 URL 解析；`assets/x.js` 仍补 `/`；`LooksLikeBundle` 标记表补 Nuxt / SvelteKit 特征；`dynamicImports` 支持静态模板字符串 | 真实入口 JS 上 `Extract` 产出 211 个 `./x.js`，此前是 211 个无效的 `/./x.js`；新增 4 个单测 + 1 个 frontier 契约测试 |
| **F-3 调度分区** | 新增 `priority.go`：同层稳定分区，把 hydration 导航页排在静态资源之后 | 新增 4 个用例锁定 `extractor → toTasks → orderTasks` 接线 |

结构化约定照旧：一个文件一个职责、识别规则用表驱动、新能力带可区分的
`source` 与限流、先写测试再接线。

---

## 三、F-0 实测（2026-11-07，改造前的真实样本）

参数统一为 `-d 2 -max-urls 300 -proxy 127.0.0.1:10808`，三站同口径：

| 站点 | 观察 | 结论 |
|---|---|---|
| nuxt.com | 首页 `<script id="__NUXT_DATA__" type="application/json">` 约 201 KB，解码后有 **460 条站内绝对路径**，旧结果只覆盖 50 条 | hydration payload 是最大缺口，且斜杠写成 `\u002F`，正则/AST 都拿不到 |
| nuxt.com | 入口 JS 496 KB，`__vite__mapDeps` 有 211 个元素、56 个 `import()`，其中 **36 个 chunk 未被 HTML link 覆盖** | 不是缺规则，是 `./x.js` 被错误补成 `/./x.js` |
| svelte.dev | `/_app/immutable` 资产 77 条已由 HTML link 覆盖，`chunk:*` 0 条 | 该框架暂时没有额外增量 |
| nextjs.org | `next-flight` 32 条仍正常 | App Router 解析未回归 |

`FromHTML` 层面的收益是确定的（200 条干净路径）；端到端结果还受配额与并发
调度影响，见下节。

---

## 四、验收数据

**探针夹具**（`testdata/probe/check.py`）

```
[载体]   20/20   [方法]   5/5   [参数]   3/3   [噪音]   0 条
结论: 全部达标
```

**新增能力**也进了夹具：`__NUXT_DATA__` 的 `\u002F` 路径作为第 20 个载体，
payload 里的静态资源作为新的噪音守卫（不该被重复收）。
退出码口径仍是「已实现项不回归」，不是「满覆盖」。

**真实站点端到端对照**（nuxt.com，`-d 2 -max-urls 800`，旧 `d17a6a4` 二进制
vs 新二进制，同一时段串行跑）：

| 指标 | 旧 `d17a6a4` | 新（最终） |
|---|---:|---:|
| 结果条数 | 1152 | 1153 |
| `inline-json:nuxt-data` | 0 | 12 |
| `js-ast` | 107 | 95 |
| `sitemap` | 454 | 455 |
| `link` | 130 | 130 |
| 404 | 9 | 9 |
| 差集 | — | 新增 2 / 丢失 1 |

差集里 1 丢 2 增都是带一次性 token 的 `csp:report-to` 端点与 1 个 sitemap
页面，属于站点侧波动，**真实 URL 零丢失**。

**过程中排除过两个错误结论**，都值得留档：

1. 一开始以为 `sitemap` 454→534、`js-ast` 107→16 是 `runLevel` 并发调度波动。
   用同一二进制连跑两次验证：差集只有 2 / 1 条，调度其实是稳定的。
2. 定位到真因是**分派二选一**：新代码识别 `__NUXT_DATA__` 后只跑
   `FromInlineJSON`，丢掉了旧版从同一份 payload 经 AST 拿到的 75 条真实 URL
   （外链脚本、logo）——它们排在 200 条上限之后，或是被结构化解析过滤的
   静态资源。改为**合并** `FromInlineJSON` 与 `FromJS`（frontier 去重）后，
   `js-ast` 回到 95（差 12 条是 source 归属转移），差集收敛到 1 / 2。

合并后探针仍是 20/20、噪音 0；新增 `TestInlineScriptMergesASTAndJSON`
锁定「合并而非二选一」的契约。F-1 的静态资源过滤由单测覆盖，探针只守
全链路噪音。

结论：**提取层收益确定**（真实 HTML 上 200 条 `\u002F` 路径、真实 JS 上
36 个遗漏 chunk），**端到端零回归**。nuxt.com 自己的 sitemap 已覆盖大部分
文档页，所以新增载体在这个站点上只净增 12 条；对没有 sitemap 的 Nuxt 站
价值更大。判断是否回归必须看差集内容，不能只看 `rows` 有没有涨。

**现代站基线**（`-d 2 -max-urls 300`，历史值，仅作回归参照）：
vuejs.org 298（站内 297）、vite.dev 297；`tools/baseline/` 下有
`urls-stable.txt` 可比。传统站 `testasp-2026-11-06c`：67 条。

**历史基线漂移裁定**（2026-11-07，旧 `d17a6a4` 与最终新二进制在同时段串行复跑，
口径与 `tools/baseline.py` 的 `canonical` / `stable_of` 一致；`stable` 一套另算）：

| 站点 | 入库基线 → 新 | 旧 → 新（同时段） | 同二进制连跑噪声 | 裁定 |
|---|---|---|---|---|
| testasp | 67 → 40，消失 27 | 40 / 40，差集 **0 / 0** | — | 靶站删帖：消失的正好是 `id=5..13` 的 `showthread` / `Login` / `Register` 三件套，与 `testasp-2026-11-06c` MANIFEST 记录的 `id=14..26` 删帖模式完全同形。旧二进制同时段也只有 40 条，**代码零回退** |
| vuejs.org | 297 → 297，17 / 17 | 297 / 297，5 / 5 | 新 0 / 1，旧 2 / 3 | 300 条配额饱和下的**子域轮换**：差集整组落在 `play` / `v3-migration` / `fr` / `ru` / `v2.cn` 子域；同旧代码跨日差集反而更大（19 / 19）；无主域 URL 系统性丢失 |
| vite.dev | 293 → 293，2 / 2 | 293 / 293，12 / 12 | — | `v3.vite.dev/*` ↔ `v5.vite.dev/*` 整套版本切换（MANIFEST 已记录同类现象）；`registry.vite.dev/assets/plugins.md.B_88fOLZ.lean.js` 在旧 / 新同时段**完全一致**，证明 hash 变化来自站点部署 |

`stable` 口径没消掉 vite.dev 的 1 / 1，是 `stable_of` 的已知边界：只归一化
扩展名前最后一段，`plugins.md.<hash>.lean.js` 这类双扩展名不覆盖
（`tools/README.md` 已记录）。它成对出现在增删两侧，不构成回退。

**结论**：三份基线的差异全部来自目标站变化或配额内子域抽样，没有任何一条
可以归因到本轮代码改动。判定基线回退必须同时满足「同一时段 + 同一代理 +
旧二进制复跑 + 同二进制连跑噪声对照」；只看 `baseline.py diff` 的非 0 退出
会误报。复现步骤见 `tools/README.md` 的「基线漂移裁定 SOP」。

---

## 五、框架支持现状

| 层 | 有专属实现 | 靠通用规则兜住 |
|---|---|---|
| 构建产物 | webpack 4/5（`.u` / publicPath / 自注册 chunk）、Vite/Rollup（`__vite__mapDeps`、动态 `import()`、静态模板） | Turbopack、Angular CLI、esbuild、Parcel |
| 文件路由框架 | Next.js App Router RSC 流、Next.js Pages Router `__NEXT_DATA__`、Nuxt `__NUXT_DATA__` | SvelteKit（HTML link + sitemap，实测已覆盖主要资产） |
| 前端路由 | 显式 `{path, component/lazy/children}` 表（vue-router、react-router 配置式、Angular Router） | 纯文件路由框架仍靠 sitemap 与 HTML 链接 |
| URL 载体 | 20+ 标签、og/twitter、`data-*`、CSS、JSON、表单值、sourcemap、CSP 线索、内联 hydration JSON | — |

Next.js RSC 的三个既有坑（chunk 省略 `/_next/` 前缀、`\/` 转义、单段 payload
已完整）保持不变；本轮另修了「命中 `__next_f` 但无 payload 时整段归零」。

---

## 六、已定结论（不再讨论）

| 结论 | 依据 |
|---|---|
| CSP 线索接入采集流程，只报告不入队 | 早期「48 主机仅 3 同站」口径有误：非同站本来就会被 scope 挡掉（STATUS 历史第七节） |
| 不接入 esbuild | 真实产物零增量（`ESBUILD-ANALYSIS.md`） |
| 执行兜底默认编入、运行时由 `-route-exec` 控制 | 能力与二进制必须唯一对应 |
| `-route-expand` 保留但默认关 | SPA 的 history fallback 让展开路径与首页 md5 相同 |
| **AST 解析成功后不叠加正则** | 2026-11-07 A/B：4 条已知噪音全部反弹 |
| **hydration JSON 与 `FromJS` 合并，不是二选一** | 二选一实测丢 75 条真实 URL；合并后差集收敛到 1 丢 / 2 增 |
| **hydration 导航页在同层延后抓取** | 200 条页面会抢走 JS/CSS 配额，需稳定分区 |
| **`./x.js` 形态的 chunk 保留相对路径** | 补 `/` 会产出 `/./x.js`，整批落空 |
| 不做敏感信息提取、目录 fuzz、漏洞检测、指纹识别、JS 渲染 DOM | 已定边界 |

---

## 七、明确未做

| 项 | 原因 |
|---|---|
| 参数激进版（按候选值合成 URL） | hidden 常带 csrf/session，拼出来必然失败；需先有噪音率数据 |
| 分页推进（`?page=N`） | 容易压垮目标、缺「内容重复即停」判定 |
| TS / JSX 源码解析 | 采集对象是浏览器实际加载的 JS |
| treesitter 裁剪构建 | JS 语法表只有 40 KB，大头在运行时，收益待测 |
| SvelteKit 专属 chunk 规则 | F-0 实测资产已由 HTML link 覆盖，加规则零增量 |

`ISSUES.md` 按 P0/P1/P2 记录证据与修法成本，`ROADMAP.md` 记录批次与结论。

---

## 八、仓库状态

以 `git log origin/main` 与 `git status` 为准。本轮改动按一次提交推送，
CI 六步全绿后才算完成。
