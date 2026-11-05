# 优化规划：提高 URL 召回率

目标很明确：**在同样的目标上发现更多真实 URL**，而不是做更多功能。
判定标准用「有效 URL 数」（200 且非碎片），不是「抓了多少条」。

## 现状诊断（实测，非推测）

用 `testdata/probe/` 构造的夹具扫 19 种 URL 载体，当前覆盖 7 种，缺 12 种。
同一次探测共抓 14 条，其中 4 条是 JS 拼接碎片（`/api/`、`/config`、`/v2`、`/orders`），
全部 404 —— 这些不是「多抓」，是噪音。

在真实站点（testasp 基线 44 条）上表现尚可，但那是个传统 ASP 站：
页面结构简单、几乎没有 JS 拼装接口。**换成 SPA 或 API 驱动的站点，缺口会立刻放大。**

## 优化清单

按「预期新增有效 URL ÷ 实现成本」排序。

### P0-1 JS AST 部分求值（收益最大）

**问题**：现在是纯正则，`fetch("/api/" + mode + "/config")` 只能抽出 `/api/` 和 `/config`
两条碎片，真实路径 `/api/debug/config` 完全丢失；`var BASE="/v2"; fetch(BASE+"/orders")`
同理。既漏真资产，又制造 404 噪音。

**改法**：`extractor` 增加 AST 分支，用 `dop251/goja` 的 parser 解析，
对 `BinaryExpression(+)` 做部分求值，未知节点记为 `EXPR`；再做一遍 `var X = "literal"`
的常量传播。解析失败时回退现有正则。

**已验证可行**：原型在 `out/jsext_prototype.go`（工作区），零 CGO、只多 1 个依赖，
实测能把上面两个例子分别还原成 `/api/debug/config` 与 `/v2/orders`，
且**比 jsluice 更准**（jsluice 不做常量传播，只能给出 `/users/`）。成本：中。
代价：`extractor` 不再是零第三方依赖——这条需要你点头。

**验收**：夹具里 `/api/debug/config`、`/v2/orders` 命中；`known_noise` 里 4 条碎片消失。

### P0-2 JSON 响应解析（成本最低）

**问题**：`/api/data` 已经抓到了，但响应体是 JSON，当前不解析，
里面的 `next`、`links.self` 全部丢失。SPA 的分页和资源入口基本都在 JSON 里。

**改法**：`extractor` 增加 `FromJSON()`，递归遍历 JSON 值，对字符串值套用与 JS 相同的
过滤规则（排除静态资源、排除纯数字、长度限制）。识别依据是 `Content-Type: application/json`
或 body 首字符为 `{`/`[`。

**收益**：分页接口能滚出成倍的 URL，且几乎零误报。成本：低。

**验收**：夹具里 `/api/page/2`、`/api/items/1` 命中。

### P0-3 CSS 文件改为专用解析

**问题**：`.css` 文件现在被当成 JS 处理（`LookLikeJS` 对 `text/css` 返回 true），
走的是 JS 正则，只偶然捞到带引号的 `.json`。真正的 `url()` 和 `@font-face`
因为落在静态资源过滤里全部丢弃。

**改法**：`looksLikeCSS` 单独判断，复用已有的 `cssURLRe` 提取 `url(...)`，
不受静态资源过滤影响（CSS 引用的本来就是静态资源，过滤等于全丢）。

**收益**：图片/字体路径 + 少量提示性接口。成本：很低（约 20 行）。

**验收**：夹具里 `/css-file/bg.jpg`、`/fonts/inter.woff2` 命中。

### P1-1 响应头扩展：CSP 与 Report-To

**问题**：`FromHeaders` 只认 `Location` / `Content-Location` / `Link` / `Refresh`。
CSP 头里的 `connect-src`（后端 API 域名）、`report-uri`（上报端点）现在完全没看。

**改法**：解析 `Content-Security-Policy` / `Content-Security-Policy-Report-Only`
的各指令域名与路径，以及 `Report-To` / `NEL` 的 JSON 端点。

**收益**：内部域名和上报端点，安全视角价值高，且是「页面上看不见」的资产。成本：低。

**验收**：夹具里 `/csp-report-endpoint`、`https://api.internal.example.com` 命中。

### P1-2 meta 标签泛化

**问题**：`meta` 只处理 `http-equiv=refresh`。`og:url`、`og:image`、`twitter:image`
这些社交卡片字段全丢——它们在分享型站点上大量存在。

**改法**：识别 `property` / `name` 为 `og:*`、`twitter:*`、`msapplication-*` 的
`content`，以及 `link[rel=alternate|preconnect|dns-prefetch|manifest]`。

**成本**：低（扩一张属性表）。**验收**：夹具里 `/shared/og-page`、`/img/twitter-card.png` 命中。

### P1-3 sourcemap 解析

**问题**：`.map` 文件已经抓到（`sourceMappingURL` 提取生效），但内容是 JSON 未解析，
`sources` 里的源码路径全部丢失。

**改法**：识别 `application/json` + 路径以 `.map` 结尾或含 `"version":3`，
解析 `sources` / `sourceRoot`，还原成可访问路径。

**收益**：暴露前端组件与路由结构，有时能直接看到 `/src/views/admin/` 这类目录。成本：低。

**验收**：夹具里 `/src/api/order.js` 命中。

### P2-1 data-* 属性泛化

**问题**：只处理 `data-src` / `data-original`。Vue/React 项目常把接口写在
`data-api-endpoint`、`data-url`、`data-href` 这类自定义属性上。

**改法**：匹配所有 `data-*`，但值必须通过「像 URL」的判定（以 `/` 或 `http` 开头，
或含 `.` 扩展名）才采纳，避免把开关标记当 URL。**误报风险中等，需实测调阈值。**

### P2-2 robots.txt 的 Disallow 可选抓取

**现状**：Disallow 路径只作为线索输出（`status=0`），不实际访问。这是有意的合规选择。
**改法**：加 `--respect-robots=false` 开关，由使用者在授权场景下显式开启。

### P2-3 分页模式有限扩展

**问题**：`?page=1` 只抓到第一页，后续页要靠 JSON 的 `next` 才有（依赖 P0-2）。
**改法**：对已发现的 URL，若 query 含 `page`/`p`/`offset` 等常见分页参数，
按上限（默认 5 页）做有限递增，并设总请求预算。
**注意**：这不是目录 fuzz（已排除），只沿已存在的参数做分页推进，且必须可关闭。

## 明确不做

- 目录重组 fuzz / 猜路径（已排除）
- 敏感信息提取（已排除）
- 执行 JS 渲染 DOM（需要浏览器，与零依赖冲突）

## 验收方式

任何改动都要同时过两道：

1. **夹具**：`testdata/probe/server.py` 起服务，扫 19 个载体，`expected.json` 里
   `implemented` 逐条转 true，`known_noise` 全部消失。
2. **基线不回退**：`tools/baseline/testasp-2026-11-06` 的
   `python tools/baseline.py diff` 不能出现「消失」。

两道都过才算完成。只看新增数会把「用噪音换数字」的改动误判为进步。

## 建议实施顺序

1. P0-2 JSON 解析 + P0-3 CSS 专用解析（成本最低，先把低垂果实摘掉）
2. P0-1 JS AST（需要引入依赖，单独一个提交，便于回滚）
3. P1-1 / P1-2 / P1-3（都是新增解析分支，互不冲突，可并行）
4. P2 视实际目标站点类型再定——如果目标以传统站为主，P2 收益有限

P0 三项做完，夹具覆盖应从 7/19 提到 12/19。
