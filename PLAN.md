# 优化规划：召回 × 准确 × 参数 × 方法

目标不是「抓更多条」，而是**抓到更多「能直接拿去测」的接口**。
判定拆成四个可量化指标，避免用噪音充数。

## 四个指标

| 指标 | 定义 | 当前问题 |
|---|---|---|
| **召回** | 有效 URL 数（200 且非碎片） | JS 拼接被拆成多条 404 碎片 |
| **准确** | 不产生无效/占位 URL | 碎片全部 404；拼接产物无置信度标记 |
| **参数** | 带 query 的 URL 数与参数名种类 | 只能捡 URL 里写死的参数，表单/JS 里的全丢 |
| **方法** | 请求方法正确的接口数 | JS 调用的 method 全部退化成 GET |

## 现状实测（2026-11-06）

### 方法：form 对，JS 全错

| 来源 | 提取到的 URL | 当前 method | 应为 | 结论 |
|---|---|---|---|---|
| `form[method=get]` | `/search` | GET | GET | 正确 |
| `form[method=post]` | `/login` | POST | POST | 正确 |
| `$.get(...)` | `/api/list` | GET | GET | 正确（默认值碰巧对） |
| `axios.post(...)` | `/api/users` | **GET** | POST | 错 |
| `axios.delete(...)` | `/api/users/1` | **GET** | DELETE | 错 |
| `fetch(url,{method:"PUT"})` | `/api/items` | **GET** | PUT | 错 |

`method` 字段在链路里是通的（form 能正确传出），问题只在**没有从 JS 调用点解析**。

### 参数：只能捡写死的

同一批用例里，`q` / `page` / `sort` / `username` / `size` / `role` 全部丢失。
原因是没有从两个地方取：

- `<form>` 内 `<input name>` / `<select name>` / `<textarea name>`
- JS 调用的第二参数对象：`$.get("/api/list", {page:1,size:20})`、`axios.get(url,{params:{role:"admin"}})`

### 召回与准确：碎片

`fetch("/api/" + mode + "/config")` 被拆成 `/api/` 与 `/config` 两条独立结果，均 404。

### 真实站点基线（testasp）

44 条站内 URL，其中 36 条带参数（81%），但**只有 3 个参数名**（`RetURL`/`id`/`item`）。
比例高是因为站点自己的链接写了参数，不代表推导能力——换个 SPA 就会归零。

## 优化项

### P0-1 JS 调用点语义识别（一石三鸟）

**一次改造同时解决方法、参数、碎片三件事**，因为它们都需要同一份信息：
调用点的 callee 与实参。

要识别：

| 模式 | 产出 |
|---|---|
| `fetch(url, {method:"POST", body:...})` | method + bodyParams |
| `axios.post(url, data)` / `.delete(url)` | method |
| `axios.get(url, {params:{a,b}})` | queryParams |
| `$.get(url, {a,b})` / `$.post(url, {a,b})` | method + queryParams |
| `xhr.open("PUT", url)` | method |
| `navigator.sendBeacon(url, data)` | method=POST |
| 字符串拼接 `A + "/x" + B` | 部分求值，还原完整路径 |

**输出形态**：`{URL, Method, QueryParams, BodyParams, Confidence}`。

**实现**：`dop251/goja` parser + AST，原型见 `testdata/probe/jsext_prototype.go`
（已验证：零 CGO、能还原 `/api/debug/config`、处理了「常量传播结果不能丢」与
「嵌套 + 只取最外层」两个坑）。解析失败回退现有正则。

**成本**：中（+1 依赖）。这是唯一需要外部依赖的项，需你确认。

**收益**：方法正确率 0/3 → 3/3；同时消除 4 条碎片、产出参数名。

### P0-2 表单参数合成（成本最低，收益明确）

**问题**：`<form action="/search" method="get">` 现在只提取 `/search`，
里面的 `q`、`page` 全丢。表单是**参数名最集中的来源**，一个后台登录页就能给出
`username`/`password`/`captcha` 三个参数。

**改法**：`extractor.FromHTML` 在遇到 `form` 时，向上收集其可见控件：
`input[name]`、`select[name]`、`textarea[name]`（跳过 `type=submit|button|reset`）。
GET 表单 → query 参数；POST 表单 → body 参数。产出 `/search?q=&page=`。

**成本**：低（纯 HTML 遍历，约 40 行）。**验收**：`q`/`page`/`username` 出现在参数里。

### P0-3 JSON 响应解析

**问题**：`/api/data` 抓到了但响应体不解析，`next`、`links.self` 全丢。
SPA 的分页和资源入口基本都在 JSON 里。

**改法**：新增 `FromJSON()`，递归遍历字符串值，套用与 JS 相同的过滤规则
（排除静态资源、纯数字、超长串）。识别依据 `Content-Type: application/json`
或 body 首字符 `{`/`[`。

**成本**：低。**验收**：`/api/page/2`、`/api/items/1` 命中。

### P0-4 CSS 文件专用解析

**问题**：`.css` 现在被当 JS 处理，真正的 `url()` 因为落在静态资源过滤里全丢。

**改法**：独立 `looksLikeCSS` 分支，复用 `cssURLRe` 提取 `url(...)`，
对 CSS 引用的资源不做静态资源过滤（过滤等于全丢）。

**成本**：很低（约 20 行）。

### P1-1 置信度建模（服务「准确」）

**问题**：部分求值会产出 `/api/v1/users/EXPR` 这类含占位符的结果。
它**不是可访问的 URL**，现在会入队抓取（必然 404，浪费请求），且混在结果里拉低准确率。

**改法**：`Result` 增加 `Kind` 与 `Confidence`：

| Kind | 含义 | 是否入队抓取 |
|---|---|---|
| `page` | 可直接访问的页面 | 是 |
| `asset` | 静态资源 | 是 |
| `api` | 接口路径（完整） | 是 |
| `template` | 含 EXPR 占位符，需人工替换 | **否**，仅输出 |

**默认行为**：`template` 不入队，避免无谓的 404。这同时提升了「准确」与速度。

### P1-2 响应头扩展：CSP / Report-To

`Content-Security-Policy` 的 `connect-src`（后端 API 域名）、`report-uri`（上报端点）
是「页面上看不见」的资产，安全视角价值高。**成本**：低。

### P1-3 meta 泛化

`og:url`、`og:image`、`twitter:image`、`link[rel=alternate|preconnect|manifest]`
全丢（现在只认 `http-equiv=refresh`）。**成本**：低。

### P1-4 sourcemap 的 sources

`.map` 已抓到但内容不解析，`sources` 里的源码路径全部丢失。**成本**：低。

### P2-1 URL 归一化增强（服务「准确」）

`?a=1&b=2` 与 `?b=2&a=1` 目前算两条。多数框架下等价，可排序后去重。
**风险**：少数框架按顺序取值，需做成可关闭选项。

### P2-2 data-\* 泛化

`data-api-endpoint`、`data-url` 等，值必须通过「像 URL」判定才采纳。误报风险中等。

### P2-3 分页参数有限推进

对已发现的 `?page=1` 按上限（默认 5 页）递增，并设总请求预算。
**注意**：这不是目录 fuzz（已排除），只沿已存在的参数推进，必须可关闭。

## 明确不做

- 目录重组 fuzz / 猜路径
- 敏感信息提取
- 执行 JS 渲染 DOM

## 验收

每项改动都要过两道：

1. **夹具**：`testdata/probe/` 起服务后，`expected.json` 的 `implemented` 逐条转 true，
   `known_noise` 全部消失；新增「方法正确」「参数提取」两组断言。
2. **基线不回退**：`python tools/baseline.py diff` 无「消失」项。

只看新增数会把「用噪音换数字」误判成进步，所以第一道必须包含「噪音消失」的断言。

## 实施顺序

| 阶段 | 内容 | 依赖 | 风险 |
|---|---|---|---|
| 1 | P0-2 表单参数 + P0-4 CSS 解析 | 无 | 无，纯标准库 |
| 2 | P0-1 JS 调用点语义识别 | 需引入 goja | 单独提交，便于回滚 |
| 3 | P1-1 置信度建模 | 依赖 P0-1 产出模板 URL | 改 Result 结构，需同步基线脚本 |
| 4 | P0-3 JSON + P1-2/3/4 | 无 | 各自独立 |
| 5 | P2 视目标类型再定 | — | — |

P0 全部完成后，夹具覆盖应从 7/19 提升到 15/19，方法正确率 3/3，参数名可提取 7 个。
