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

### P0-1 JS 调用点语义识别（一石三鸟） （✅ 已完成 2026-11-06：jsext 双引擎）

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

**依赖评估（实测 2026-11-06）**：

| 项 | 结果 |
|---|---|
| 二进制体积 | 6.79 MB → **7.13 MB**（+0.34MB，约 5%） |
| 依赖增量 | +1 直接（`dop251/goja`）、+2 间接（`go-sourcemap/sourcemap`、`x/text`） |
| CGO | 不需要，`CGO_ENABLED=0` 可编译 |
| 解析性能 | 47KB 约 1.65ms；1MB bundle 约 35ms（相对网络耗时可忽略） |
| 语法覆盖 | 模板字符串 / 可选链 / `??` / async-await / 类私有字段 / 解构 / 展开 全部 OK |
| **不支持** | **ESM 的 `import` / `export` 语句、动态 `import()`** |

ESM 那条是硬限制：解析前要把 `import`/`export` 语句剥掉（约 10 行预处理），
或者直接用解析失败回退正则兜底。打包后的 UMD/IIFE bundle 不受影响，
只有 `<script type="module">` 与现代 `.mjs` 会踩到。

**注意**：只从 `goja` 取 `parser`/`ast`/`token` 三个子包，**不引入它的 VM**。
goja 本体是一个能执行 JS 的引擎，但本方案只用它做语法解析，不执行任何代码。

**候选方案对比（均为 `CGO_ENABLED=0` 实测，2026-11-06）**：

| 方案 | 二进制增量 | 50KB 解析 | 500KB 解析 | 1MB 解析 | ESM 语法 | 集成成本 |
|---|---|---|---|---|---|---|
| **`dop251/goja`** | **+0.34MB** | **1.3ms** | **10.5ms** | **19.5ms** | ❌ 不支持 | `go get`，+1 依赖 |
| `odvcencio/gotreesitter` | +3.6MB | 112ms | **6.0s** | — | ✅ 支持 | `go get`，+1 依赖 |
| `evanw/esbuild`（vendored） | 未测 | 未测 | 未测 | — | ✅ 支持 | 拷 12 个 internal 包、约 2.2MB 源码 |

- **gotreesitter 淘汰**：纯 Go 的 tree-sitter 运行时，语法覆盖完美（含 ESM、动态 import 全过），
  但解析耗时随文件增大**超线性退化**：1KB/1.2ms → 10KB/9ms → 50KB/112ms → 100KB/370ms → 500KB/6.0s，
  吞吐仅 0.1–1.3 MB/s，比 goja 慢 **最多 570 倍**。前端 bundle 动辄几百 KB，这个开销不可接受。
- **esbuild 不必要**：它的内部 parser 确实是最快的，但要 vendored `js_parser`/`js_ast`/`js_lexer`/
  `compat`/`config`/`logger`/`ast`/`helpers`/`sourcemap`/`runtime`/`cache`/`xxhash` 共 12 个包、
  约 2.2MB 源码，且 esbuild 明确声明 `internal/` 无兼容保证，升级需重新拷贝。
  而 goja 已是 50 MB/s——解析 1MB 只要 20ms，而下载 1MB 通常 200ms 以上，
  解析只占端到端耗时的 10% 以内，**再快也没有意义**。
- 结论：选 goja，ESM 缺口用预处理剥掉 `import`/`export` 语句解决（约 10 行），
  代价远小于引入第二套解析器。

**收益**：方法正确率 0/3 → 3/3；同时消除 4 条碎片、产出参数名。

### P0-2 表单参数合成（成本最低，收益明确） （✅ 已完成 2026-11-06：formParamNames）

**问题**：`<form action="/search" method="get">` 现在只提取 `/search`，
里面的 `q`、`page` 全丢。表单是**参数名最集中的来源**，一个后台登录页就能给出
`username`/`password`/`captcha` 三个参数。

**改法**：`extractor.FromHTML` 在遇到 `form` 时，向上收集其可见控件：
`input[name]`、`select[name]`、`textarea[name]`（跳过 `type=submit|button|reset`）。
GET 表单 → query 参数；POST 表单 → body 参数。产出 `/search?q=&page=`。

**成本**：低（纯 HTML 遍历，约 40 行）。**验收**：`q`/`page`/`username` 出现在参数里。

### P0-3 JSON 响应解析 （✅ 已完成 2026-11-06：extractor.FromJSON）

**问题**：`/api/data` 抓到了但响应体不解析，`next`、`links.self` 全丢。
SPA 的分页和资源入口基本都在 JSON 里。

**改法**：新增 `FromJSON()`，递归遍历字符串值，套用与 JS 相同的过滤规则
（排除静态资源、纯数字、超长串）。识别依据 `Content-Type: application/json`
或 body 首字符 `{`/`[`。

**成本**：低。**验收**：`/api/page/2`、`/api/items/1` 命中。

### P0-4 CSS 文件专用解析 （✅ 已完成 2026-11-06：extractor.FromCSS）

**问题**：`.css` 现在被当 JS 处理，真正的 `url()` 因为落在静态资源过滤里全丢。

**改法**：独立 `looksLikeCSS` 分支，复用 `cssURLRe` 提取 `url(...)`，
对 CSS 引用的资源不做静态资源过滤（过滤等于全丢）。

**成本**：很低（约 20 行）。

### P1-1 置信度建模（服务「准确」） （⚠️ 部分完成：已有 Confidence=template 与 Kind，未做 page/api/template 三分类）

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

### P1-2 响应头扩展：CSP / Report-To （❌ 未做）

`Content-Security-Policy` 的 `connect-src`（后端 API 域名）、`report-uri`（上报端点）
是「页面上看不见」的资产，安全视角价值高。**成本**：低。

### P1-3 meta 泛化 （❌ 未做）

`og:url`、`og:image`、`twitter:image`、`link[rel=alternate|preconnect|manifest]`
全丢（现在只认 `http-equiv=refresh`）。**成本**：低。

### P1-4 sourcemap 的 sources （❌ 未做）

`.map` 已抓到但内容不解析，`sources` 里的源码路径全部丢失。**成本**：低。

### P2-1 URL 归一化增强（服务「准确」） （❌ 未做）

`?a=1&b=2` 与 `?b=2&a=1` 目前算两条。多数框架下等价，可排序后去重。
**风险**：少数框架按顺序取值，需做成可关闭选项。

### P2-2 data-\* 泛化 （❌ 未做）

`data-api-endpoint`、`data-url` 等，值必须通过「像 URL」判定才采纳。误报风险中等。

### P2-3 分页参数有限推进 （❌ 未做）

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

---

## 附：双引擎实现与实测对比（2026-11-06）

`jsext/` 下实现了两个可切换的 JS 提取引擎，共用 `jsext.Request` 契约，
通过 `-js-engine regex|goja|treesitter` 选择。

### 一致性

`jsext/compare_test.go` 的 10 个用例（拼接、常量传播、axios/jQuery/fetch/xhr 方法、
参数对象、ESM、动态 import、纯字面量）两个引擎**输出完全一致**。

### 实测数据

| 指标 | regex | goja | treesitter |
|---|---|---|---|
| 二进制（各自单独启用） | 6.79 MB | **7.17 MB**（+0.38） | 14.85 MB（+8.06） |
| 夹具载体命中 | 9/19 | **11/19** | **11/19** |
| 方法正确 | 2/5 | **5/5** | **5/5** |
| 噪音残留 | 4 条 | **0** | **0** |
| 1MB JS 端到端（深 2） | 586 ms | **1715 ms** | 3075 ms |
| ESM 语法 | 不敏感 | 需预处理剥离 | 原生支持 |

双引擎同时编进二进制时体积 15.15 MB。

### 关键发现

1. **goja 的 ESM 缺口可以完全绕过**：解析失败时用 `StripModuleSyntax` 剥掉
   `import x from "y"` / `export ...`、把 `import(...)` 改名为 `__import(...)` 后重试，
   提取结果与 treesitter 原生解析一致。这个预处理只有约 30 行。
2. **treesitter 的体积代价是 8 MB**，几乎全花在 JS 语法表上，对命令行工具尚可接受，
   对要嵌入其他服务的库偏重。
3. **1MB 文件上 goja 快 1.8 倍**（1.7s vs 3.1s）。注意这与早期「50MB/s」的测法不同：
   那次只测 `ParseFile`，完整提取（遍历 + 12 万节点的类型断言）才是真实开销。
4. 两个 AST 引擎都把 `/api/v1` + `/users/` 两个碎片合并成了 `/api/v1/users/EXPR`，
   regex 则保留了碎片——这是「准确」维度的直接收益。

### 建议

默认用 `goja`：功能与 treesitter 等价、体积小 7.7MB、快 1.8 倍。
`treesitter` 保留作为对照实现，等确认无需对比后可从 `jsengine.go` 的
`engineFunc` 中移除（保留 `jsext/treesitter` 目录不影响默认构建，
但**必须同时删除 import**，否则二进制仍会带上 8MB 语法表）。

---

## 附：爬虫实战测试（2026-11-06）

用 `testdata/spa_fixture.py` 搭了一个 SPA 风格桩站（Vue/React 项目常见形态）：
`var BASE="/api/v2"` 拼接、`axios.get(url,{params:{...}})`、`axios.post`、
`$.ajax({url,type,data})`、`XMLHttpRequest.open`、动态 `import()`、静态 `import`、
sourceMappingURL 注释、外链 CSS。

三个引擎各跑一遍（深度 3）：

| 引擎 | 总数 | 有效 | 碎片 | 接口 | 带参 | 方法分布 |
|---|---:|---:|---:|---:|---:|---|
| regex | 19 | 12 | **7** | 3 | 0 | 全 GET |
| goja | 17 | **17** | 0 | **8** | 1 | GET×14, POST×2, DELETE×1 |
| treesitter | 17 | **17** | 0 | **8** | 1 | 同上 |

**regex 的 19 条里 7 条是表达式碎片**（`/api/v2`、`/orders`、`/users`、`/session`、
`/legacy/list`…），有效 URL 只有 12 条；AST 引擎 17 条全部有效。

AST 独有的 5 条真实接口，regex 完全拿不到：

```
/api/v2/auth/session          ← fetch(R + "/session")，R 是常量
/api/v2/legacy/list   POST    ← $.ajax({url: BASE+"/legacy/list", type:"POST"})
/api/v2/orders        POST    ← axios.post(url, {sku,qty})
/api/v2/orders/EXPR           ← fetch(BASE+"/orders/"+id)
/api/v2/users?page=&limit=    ← axios.get(url,{params:{page,limit}})
```

方法识别：`XHR.open("DELETE", ...)` → DELETE、`$.ajax(type:"POST")` → POST、
`axios.post` → POST，全部正确；regex 则一律 GET。

### 本轮修掉的两个缺陷

1. **静态 import 的模块路径丢失**。goja 走 ESM 预处理时会删掉 `import x from "y"` 整行，
   而 `collectStaticImports` 拿到的又是删改后的源码（`src = fixed` 覆盖了原变量），
   导致 `./dep.mjs` 丢失。改为保留 `orig` 再提取。
2. **`$.ajax({url, type, data})` 的 URL 在对象字段里**，原先只取第一个实参，
   所以这条要么拿不到、要么拿成 GET。补了配置对象分支，同时提取 `type` 与 `data`。

---

## 附：Vue / webpack 产物实测（2026-11-06）

夹具：`testdata/vue_fixture.py`，按 `vue-cli-service build` 之后的真实形态还原
（terser 压缩、单字母变量、模板字符串、派生常量、chunk hash 映射表）。

### 修复前：只覆盖一半

| 写法 | AST 引擎表现 |
|---|---|
| `n.get(`${c}/products/${id}`)` | **完全丢失**（eval 不认 TemplateLiteral） |
| `n.put(`${c}/orders/${id}`)` | **完全丢失** |
| `fetch(`/api/v3/search?q=${q}`)` | **完全丢失** |
| `n.post(u, {...})`，`u = c + "/orders"` | 拿到 URL，但**方法丢失**（`u` 是派生常量，没进常量表） |
| `axios.get("/api/v3/list", {params})` | 正常，参数 `page` `size` 都拿到 |

三个缺陷的共性：**都能静态确定，只是实现没覆盖到**。

### 修复后

1. **模板字符串求值**（两个引擎）。`Elements` 与 `Expressions` 交替拼接，
   常量代入、未知部分记 `EXPR`，与 `+` 拼接完全一致的处理方式。
2. **推导式常量收集**。常量表原来只收纯字面量，改为收「部分求值后完全确定」的表达式，
   `var u = c + "/orders"` 因此可用（webpack 压缩产物里这种派生很常见）。
3. **压缩后方法推断**。`axios` 被压成 `n` 后对象名不可信，退化为
   「属性名是 HTTP 动词 **且** 首参确实像地址」才判定，
   避免把 `store.get("token")` 误判成请求（有单测锁住）。
4. **模板不再入队抓取**。`Result` 新增 `confidence` 字段，
   含 `EXPR` 的地址只作为「接口形状」输出，不发请求——之前这类必然 404，
   白占配额。无法锚定到根路径的相对模板（webpack chunk 拼接）直接丢弃。

### 实测对比

| 指标 | regex | goja / treesitter |
|---|---:|---:|
| 输出条数 | 14 | 11 |
| 其中 404 噪音 | **10** | 5（均为夹具未提供的真实静态文件） |
| 识别出的接口 | 2 | **3 + 2 个模板** |
| 方法正确 | 0/2 | **3/3**（GET / POST / POST） |
| 模板字符串产出 | 0 | **2** |

regex 的 10 条 404 里包括 `/api/v3`、`/orders`、`/orders/`、`/static/`、`/static/js/`
这类拼接残留——数字更多，但全是无效地址。

两个 AST 引擎在本夹具上输出**完全一致**（`TestVueWebpack` 同时断言两者）。

### 仍然拿不到的一类：webpack chunk 文件

```
o.p + "chunk-" + e + "." + {101:"a1b2c3",102:"d4e5f6"}[e] + ".js"
```

hash 取自查找表，形参 `e` 同时出现在文件名和查表下标里，**两者必须取同一个键**。
简单笛卡尔积会产生 `chunk-101.d4e5f6.js` 这种非法组合；
要正确还原得做「按 key 枚举 + 同名变量一致绑定」的符号执行。

chunk 文件往往是 SPA 里 API 调用最密集的地方（路由级组件、懒加载模块），
这是目前 Vue/React 站点召回率的主要缺口。要不要实现，需要单独评估成本与噪音。
