# ESM 解析与 treesitter 去留评估

日期：2026-11-06
问题：ESM 解析是否有必要？保留 treesitter 是否能带来更好的分析效果？
结论：**两者都是必要的，且不该二选一——默认改成 goja → treesitter 的串联链。**

---

## 一、ESM 用例覆盖（14 项）

同一段源码分别交给两个引擎，只统计「能否解析」与「URL 是否拿到」。

| 用例 | goja | treesitter |
|---|---|---|
| `export const routes=[...]` | ok | ok |
| `export default {base:"/api/default"}` | **PARSE-ERR** | ok 2/2 |
| `export default function(){...}` | **PARSE-ERR** | ok 1/1 |
| `import a from "./a.js"` + 具名 | ok | ok |
| `export {x} from "./reexport.js"` | **PARSE-ERR** | ok |
| `export * from "./star.js"` | **PARSE-ERR** | ok |
| `import.meta.url` | **PARSE-ERR** | ok |
| `import.meta.env.VITE_API` | **PARSE-ERR** | ok |
| 顶层 `await` | **PARSE-ERR** | ok |
| 动态 `import("./chunk-"+id+".js")` | ok | ok |
| ESM + 模板字符串 | ok | ok |
| 导出后紧接 IIFE | ok | ok |

**goja 在 12 项里有 6 项完全失败**（`export default`、`export ... from`、
`import.meta`、顶层 `await`）。这些不是边角语法：`export default` 是模块最常见的导出形式，
`import.meta` 是 Vite/worker/wasm 加载资源的标准写法。

## 二、真实生产产物（决定性证据）

从两个 Vite 站点下载真实入口 chunk：

| 文件 | goja | treesitter |
|---|---|---|
| `vite.dev/assets/app.js`（1.3KB） | **PARSE-ERR**（Line 1:1 Unexpected reserved word） | 2 条 chunk 路径 |
| `vuejs.org/assets/app.js`（8.0KB） | **PARSE-ERR** | 12 条（含 9 个 `./chunks/*.js`） |
| `vite.dev/assets/chunks/metadata.js`（10KB） | ok 3 条 | ok 3 条 |

**Vite 站点的入口文件对 goja 是 100% 不可解析的。** 这正是 API 调用最集中的文件。

## 三、但「能不能发现 chunk」不等于「ESM 支持的价值」

在爬虫层面对 `vuejs.org` 实跑（深度 2、300 配额），三个引擎的差距没有想象中大：

| 引擎 | 总数 | JS 文件 | JS 200 | chunk |
|---|---:|---:|---:|---:|
| regex | 296 | 17 | 17 | 8 |
| goja | 298 | 17 | 17 | 8 |
| auto | 297 | 16 | 16 | 8 |

原因：Vite 的 chunk 路径是**独立字符串字面量**（`from"./chunks/x.js"`），
正则也捡得到。ESM 支持带来的真实收益在别处：

1. **方法与参数**：ESM 文件交给正则 → 全部退化成 GET、参数丢失。
   AST 才能识别 `axios.post` / `xhr.open("PUT", …)` / `{params:{…}}`。
2. **拼接还原**：`fetch(B+"/x/"+id)` 在正则里是碎片，AST 才能合并成 `/x/EXPR`。
3. **截断容错**：见下节。

## 四、截断容错（treesitter 的第二个价值）

爬虫默认 `MaxBodySize=2MB`，大 bundle 必然被切在函数中间。实测在一个
4709 字节的 IIFE 里切在 24% / 49% / 74%：

| 截断位置 | goja | treesitter |
|---|---|---|
| 24% | PARSE-ERR | 51 条 |
| 49% | PARSE-ERR | 102 条 |
| 74% | PARSE-ERR | 151 条 |

goja 是**全有或全无**（语法必须完整），tree-sitter 是增量容错（解析到哪算哪）。
对 2MB 上限的爬虫，这是结构性差异，不只是 ESM 问题。

## 五、代码改动

### 1. 默认引擎改为 `auto`（串联）

```go
func engineChain(name JSEngine) []jsext.Engine {
	switch name {
	case JSEngineGoja:       return []jsext.Engine{goja.Extract}
	case JSEngineTreesitter: return []jsext.Engine{treesitter.Extract}
	case JSEngineRegex:      return nil
	default:                 return []jsext.Engine{goja.Extract, treesitter.Extract}
	}
}
```

链上第一个成功的引擎结果即被采用；全部失败才回退正则。
`goja` 遇到 ESM 在第一个 token 就报错，串行成本可忽略。

### 2. goja 改为「显式拒绝 ESM」

旧实现是「parse 失败 → 剥掉 `import`/`export` → 再 parse 一次」。这条路径**会静默出错**：

```js
export default {a: "/x"};
//  剥掉 export default 后 →  {a: "/x"};   ← 合法的「代码块 + 标签语句」
```

解析成功、不报错、什么也提取不到，**串联兜底不会触发**。同样的形状在真实代码里不少见，
属于「宁可失败也不要错」的典型。因此：

- 新增 `looksLikeESM()`（识别 `import`/`export`/`import.meta`/顶层 `await`）；
- 解析失败且看起来是 ESM → 返回 `ErrESMUnsupported`，交给上层兜底；
- 删除 `StripModuleSyntax`（约 30 行正则手术）与随之失效的 `collectStaticImports`。

## 六、顺带修掉的两个真实缺陷

| 问题 | 现象 | 修法 |
|---|---|---|
| `export {x} from "./a.js"` 的模块路径丢失 | 再导出携带的 chunk 路径拿不到 | treesitter 增加 `export_statement`；goja 的静态 import 正则扩到 `export … from` |
| `var api="/api/hidden/endpoint"` 丢失 | AST 成功即独占结果，裸变量赋值没人收 | 新增「前缀变量」判定 + `endpointShaped` 形状判定 |
| `.blocks` 被当成 URL | `e.querySelector(\`.blocks\`)` 的 CSS 选择器进了结果 | `urlLike` 拒绝以 `.` 开头的字符串 |

「前缀 / 端点」的区分规则（两引擎一致）：

1. 变量出现在 `+` 的操作数位置，或模板字符串的 `${}` 里 → 视为**前缀**，不输出；
2. 裸 `var`/`let`/`const` 赋值另外要求**形状像端点**：绝对地址、带 `?#`、
   路径段 ≥ 3，或末段含扩展名。`/api/v3` 因此不会被当成接口。

## 七、结论与后续

| 决策 | 结论 |
|---|---|
| ESM 解析是否有必要 | **有必要**。Vite 是 Vue 3 / 大量现代站点的默认构建，入口即 ESM |
| 是否保留 treesitter | **保留**。它承担两件事：ESM 全量解析、截断容错 |
| 是否二选一 | **不**。默认串联，快路径与覆盖率都要 |
| ROADMAP 中「移除 treesitter 省 8MB」 | **撤回**。那会直接砍掉 ESM 与容错能力 |

代价记录：二进制 15.21MB（含两套引擎）；ESM 文件的解析耗时按 treesitter 计（1MB ≈ 3.1s）。
若将来要减体积，正确方向是给 treesitter 做**只编 JS 语法**的裁剪构建，而不是删掉它。

遗留：`treesitter` 的 1.8x 耗时在大站点上仍可感知。可考虑「先看文件是否含 ESM 标记，
含则直接走 treesitter」，省掉 goja 的一次失败解析（收益很小，暂不做）。
