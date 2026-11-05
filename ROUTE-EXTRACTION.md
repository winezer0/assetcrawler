# 路由与 chunk 提取：路线评估

日期：2026-11-06
背景：SPA 的路由页面和懒加载 chunk 不挂在任何静态链接上，普通爬虫拿不到。
本文评估「esbuild 定位模块 → goja 挂 `__webpack_require__` → 执行导出 routes」
这条路线的可行性，以及 JSHunter 的 `-G/--chunk-graph` 有哪些可借鉴的。

相关实验代码：`testdata/routeprobe/`（执行路线）、`testdata/routeprobe_esm/`（失效边界）。

---

## 一、结论先说

| 环节 | 可行性 | 结论 |
|---|---|---|
| 1. esbuild 做 AST 定位模块 | ❌ 用不了 | `internal/js_parser` 外部不可导入，`pkg/api` 只暴露 Transform/Build，拿不到 AST |
| 2. goja 挂极简 `__webpack_require__` | ✅ 可行 | 40 行内跑通，能拿到路由 + chunk id |
| 3. 执行模块导出 routes | ⚠️ 有条件 | webpack/CJS 产物可用；**ESM/Vite 产物完全不可用** |

**推荐：静态优先，执行兜底。**
静态能覆盖绝大多数场景且零风险；只有「路由表由运行时 map/concat 生成」时才需要执行。

---

## 二、JSHunter 值得借鉴的部分

仓库：`github.com/cc1a2b/JShunter`，42 个 Go 文件，核心在
`internal/jshunter/chunkgraph.go`（627 行）。

### 1. 整个 chunk 图是**纯静态**从词法流里解析出来的

没有 JS 引擎、没有 goja、没有执行。文件头注释写得很清楚：

> Extraction is driven from the token stream rather than from regexes over raw
> text, so a manifest that happens to sit inside a comment or a template
> substitution is not mistaken for a live one.

它用自带的 `Lexer` 产出的 `Token` 流做结构判断，而不是正则扫原文。
这一点直接否定了「必须执行才能拿到 chunk」的假设。

### 2. webpack 的 chunk 文件名还原：按同一 key 对齐

`cgCaptureRHS` + `cgBuildChunkURLs` 处理 `__webpack_require__.u`：

```js
o.u = function (e) { return "chunk-" + e + "." + {101:"a1b2c3",102:"d4e5f6"}[e] + ".js" }
```

拆成 `literals[前缀, 中缀, 后缀]` + `maps[hash 表]`，
然后**按同一个 key 同时填 `<ID>` 和 `<HASH>`**，避免笛卡尔积产生
`chunk-101.d4e5f6.js` 这种非法组合。

我上一轮说过这里「需要符号执行」，**是错的**。实测只要把
`{...}[e]` 当作一个整体 token（而不是把下标里的 `e` 再替换一次），
15 行就能还原：

```
模板片段: ['chunk-', '<ID>', '.', '<HASH>', '.js']
查找表: {'101': 'a1b2c3', '102': 'd4e5f6', '103': '9f8e7d'}
→ /static/js/chunk-101.a1b2c3.js
```

### 3. 路由表提取是「括号栈扫描」，不是 AST

`cgRouteTables`（约 80 行）：逐 token 走，维护 `{`/`}` 的帧栈，
遇到 `:` 记下 key，遇到字符串记下 value；当一个 `}` 闭合的帧里
同时有 `path` 和至少一个路由兄弟键（`component`/`name`/`element`/`children`），
就产出一条路由。

不需要 AST，也不需要知道框架。

### 4. 多运行时覆盖清单（可直接照抄这个清单）

| 运行时 | 识别点 |
|---|---|
| webpack 4/5 | `__webpack_require__.u`、`miniCssF`、`jsonpScriptSrc` |
| Next.js | build manifest |
| Vite / Rollup | `__vite__mapDeps`、`__vitePreload` |
| 通用 | 动态 `import()` 的模块说明符 |
| 路由 | React Router / Vue Router / Angular 的路由表 |

### 5. 另一个值得注意的设计：结构化分流

它的「Structural Detection Engine」把每个字节分成
字符串 / 模板 / 注释 / 正则 / 代码五类，规则只在对应区域生效。
这套分类对 URL 提取同样有用（比如避免把注释里的例子当成真实路径），
但它的主要目标是敏感信息误报控制，与我们的定位不同，**不建议整体搬**。

---

## 三、你的三步路线实测

### 第 1 步：esbuild 做 AST —— 不可行

```
$ go build
main.go:6:2: use of internal package github.com/evanw/esbuild/internal/js_parser not allowed
```

`go list github.com/evanw/esbuild/...` 只对外暴露：

```
github.com/evanw/esbuild/cmd/esbuild
github.com/evanw/esbuild/pkg/api      # 只有 Transform / Build，返回 code + sourcemap
github.com/evanw/esbuild/pkg/cli
```

要用它的 parser 只能 vendored，代价是 **12 个 internal 包、约 2.2 MB 源码**，
且官方明确不保证内部 API 兼容。

而且我们已经有 **goja AST**（能解析 ES2020）和 **gotreesitter**（能解析 ESM），
再引一套 parser 收益很低。

### 第 2+3 步：goja 挂 require 并执行 —— 可行，实测通过

`testdata/routeprobe/main.go`，核心就是准备好全局环境然后把 chunk 跑起来：

```go
vm.RunString(prelude)   // 造 self/window，捕获 chunk 的 push 载荷
vm.RunString(chunk)     // (self.webpackChunkapp || []).push([["routes"], {500: fn, ...}])
vm.RunString(loader)    // 极简 __webpack_require__ + 读 routes + 触发懒加载
```

`loader` 里的 require 只有 8 行：

```js
function __webpack_require__(id) {
  if (__cache[id]) return __cache[id].exports;
  var m = __cache[id] = { id: id, exports: {} };
  __mods[id](m, m.exports, __webpack_require__);
  return m.exports;
}
__webpack_require__.p = "/static/js/";
__webpack_require__.u = function (e) { ... };
__webpack_require__.e = function (e) { __chunks.push(e); return Promise.resolve(); };
```

实测输出：

```
模块 500: 3 条路由            ← 静态字面量表
    /                      home
    /admin/users/:id       user
    /report                report
模块 501: 3 条路由            ← 运行时 map 生成，静态解析拿不到
    /billing               billing
    /audit                 audit
    /settings              settings
懒加载捕获的 chunk id: [101 102 103 200 201 202]
对应文件: /static/js/chunk-101.a1b2c3.js ...
```

**这就是执行路线的唯一价值：`routes = sections.map(...)` 这种动态生成。**
静态扫描只能拿到字面量里的 `/admin/users/:id`、`/report`，
拿不到 `/billing` `/audit` `/settings`。

### 但失效边界很硬

`testdata/routeprobe_esm/main.go` 实测：

| 写法 | 结果 |
|---|---|
| `import {createRouter} from "vue-router"; export default [...]` | ❌ `SyntaxError: Unexpected reserved word` |
| `export const routes = [...]` | ❌ 同上 |
| `import("./chunk-a.js")` | ❌ 同上 |
| `obj?.a ?? b` | ✅ |
| `class A { #p = 1 }` | ✅ |
| `document.createElement(...)` | ❌ `ReferenceError: document is not defined`（需打桩） |
| `while(true){}` | ⏳ **挂死**，必须靠 `vm.Interrupt` 兜底 |

关键点：**goja 连动态 `import()` 都不认**。
所以「剥离 import/export 再执行」这条路也走不通——
剥离等于删掉模块依赖，执行必然报错。
我们现有的 `StripModuleSyntax` 只适用于「解析提取」，不能用于执行。

这意味着：**Vite / ESM 产物（越来越多）在这条路线上是零覆盖。**

---

## 四、建议的实现方案

```
1. 静态层（默认，覆盖 90%）
   - 用现有 goja AST / gotreesitter 定位 `__webpack_require__.u`、
     `__vite__mapDeps`、动态 import() 说明符
   - 查表按同一 key 对齐，还原 chunk 文件名
   - 括号栈扫路由表：path + (component|name|element|children)
   - 对 ESM 同样有效（gotreesitter 能解析）

2. 执行层（兜底，默认关）
   - 仅当静态层发现了「像路由模块，但一条路由都没提取到」时才尝试
   - 必须在 goja 里配 vm.Interrupt(超时) + document/window 桩
   - 框架桩：createRouter 返回 {routes: opts.routes}，未知模块返回空对象代理
   - 只对 CJS/webpack 产物启用；ESM 直接跳过
```

### 优先级

| 项 | 价值 | 成本 | 建议 |
|---|---|---|---|
| chunk 文件名还原（webpack `u`/vite mapDeps） | 高（这是喂给爬虫的下一批 URL） | 低（~150 行） | **先做** |
| 字面量路由表提取 | 中（路由页面通常是需要登录的入口） | 低（~80 行） | 先做 |
| goja 执行兜底 | 低（只覆盖 map 生成的路由） | 高（桩 + 超时 + 安全风险） | 后置 |

---

## 五、仍然需要承认的限制

1. **路由不一定等于可访问 URL**。Vue Router 的 `/admin/users/:id` 是前端路由，
   服务器上可能是同一个 `index.html`。这类结果应标 `kind=route` 而不是混进 URL 列表。
2. **chunk 还原依赖 publicPath**。`__webpack_require__.p` 可能是运行时赋值，
   静态拿不到时要退回目录推断（用入口 js 的目录）。
3. **执行层有供应链风险**。跑目标站的代码等于在本地执行不可信 JS，
   必须限定时间/内存，且默认关闭。

---

## 六、实施结果（2026-11-06）

结论已落地：静态优先（默认开），执行兜底（编译期可选）。

### 代码位置

| 文件 | 作用 |
|---|---|
| `routegraph/lexer.go` | 最小 JS 词法分析器：区分代码 / 字符串 / 模板 / 注释 / 正则 |
| `routegraph/chunk.go` | webpack `u`（含 hash 表按 key 对齐）、vite mapDeps、动态 import |
| `routegraph/routes.go` | 括号栈扫路由表（path + component/name/element/children） |
| `routegraph/routegraph.go` | `Extract()` 入口 + `LooksLikeBundle()` 粗筛 |
| `routegraph/exec.go` | goja 执行兜底（`//go:build routeexec`） |
| `routegraph/exec_stub.go` | 未启用标签时的占位，返回明确错误 |

接入点：`crawler.extractAll` 的 JS 分支，**先跑 routegraph 再跑通用 JS 提取**——
两者可能产出同一 URL，frontier 先到先得，带类型标记的必须排前面，否则 `kind` 会丢。

### 实测

| 项 | 结果 |
|---|---|
| 默认二进制 | 15.22 MB（与改动前持平） |
| `-tags routeexec` | 23.70 MB（+8.48 MB，goja VM 的代价） |
| 动态靶站（`testdata/dynamic_fixture.py`） | 8 条 → **12 条**，多出的正是 `a.href=` / `f.src=` / `setTimeout` 里那些运行时才注入的 URL |
| vue 夹具 | 恢复出 `/static/js/chunk-101.a1b2c3.js`（200）与 `chunk-102`，路由表命中 |
| 探针夹具 / testasp 基线 | 无回退（11/19、5/5、0 噪音；testasp 差异仍是站点内容变化） |

### 过程中修掉的两个误报

静态层一上线就暴露了 `collectLiterals` 判定过宽：

1. `o.p = "/static/js/"`（webpack publicPath）被当成地址 → 多出一条 `/static/js/` 的 404。
   修法：成员赋值只认 URL 语义的属性名（`href`/`src`/`url`/`action`/`path`…）。
2. 路由表里的相对子路由 `path:"user/:id"` 被当成地址 → 解析成 `/static/js/user/:id`。
   修法：独立字面量必须以 `/`、`http(s)://`、`./`、`../` 开头才算。

### 仍然不做

- 响应体解析（`{"next":"/api/page/2"}` 这类）——PLAN 里的 P0-3，未做。
- Next.js build manifest 的 `_buildManifest.js` 结构化解析：目前靠动态 import 兜，
  没有专门解析它的 mapping 对象。
- 路由去参数化（`/user/:id` → 不生成候选实例），保持「只报告形状」。
