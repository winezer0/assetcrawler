# esbuild 作为解析器的收益评估

日期：2026-11-06
问题：不计解析速度，esbuild 能不能带来**更好的分析效果**？可以的话再考虑接入。
结论：**不能，不建议接入。** 它的两项理论优势（作用域正确、import 溯源）在真实产物上要么不触发、
要么可以用本地约 100 行代码实现，不需要引入依赖。

---

## 一、评估口径

不看速度、不看体积，只回答一件事：**提取结果是否更准**——新增真实 URL、消除错误 URL。

样本分两类，避免只用自己构造的夹具自证：

- 真实产物 7 个：`vuejs.org` 5 个 Vite chunk、`webpack.js.org` 2 个 webpack 包（其中 vendor 549 KB）
- 合成夹具 3 个：多模块同名变量、业务形态拼接、import 别名

## 二、esbuild 的两种用法

| 用法 | 做法 | 是否需要 fork |
|---|---|---|
| A. 折叠预处理 | `Transform(MinifySyntax)` 产出等价源码，再喂现有 treesitter | 否，`pkg/api` 即可 |
| B. 直读 AST | 用 `internal/js_parser` 拿 AST 与符号表 | **是**，internal 包不对外 |

A 的成本低得多，先按 A 测；B 的增量单独评估（见第五节）。

折叠实测需要**跑两遍**才收敛：第一遍把局部常量内联（`base` → `"/api/v1"`），
第二遍才把相邻字面量合并（`"/api/v1" + "/users"` → `"/api/v1/users"`）。第三遍无变化。

## 三、真实产物：零增量

| 文件 | 大小 | 原始 treesitter | 折叠后 | 差异性质 |
|---|---:|---:|---:|---|
| `vuejs/app.js` | 8 KB | 11 | 11 | 完全相同 |
| `vuejs/client.js` | 17 KB | 7 | 7 | 完全相同 |
| `vuejs/runtime-dom.js` | 16 KB | 4 | 4 | 完全相同 |
| `vuejs/src.js` | 51 KB | 8 | 8 | 完全相同 |
| `vuejs/runtime-core.js` | 70 KB | 0 | 0 | 完全相同 |
| `webpack/index.js` | 347 KB | 65 | 63 | 差集全是噪音 |
| `webpack/vendor.js` | 549 KB | 63 | 44 | 丢 29 条，**全是噪音** |

vendor 丢失的样本：

```
-EXPRapplication/x-www-form-urlencoded
::view-transition-data-rh-managed(https://react.dev/errors/ABCDEFGHIJKLMNOPQRSTUVWXYZ...)
ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789https://react.dev/errors/...
```

新增的也一样：

```
::view-transition-group(http://localhost)
data-starting-stylehttp://localhost
http://localhostEXPREXPR
```

**结论：折叠在真实产物上只是把噪音换了个形态，有效 URL 一个没多。** 代价是重打印：
vendor 从 562 KB 涨到 817 KB（+45%），且注释被删（`//# sourceMappingURL=` 会丢）。

## 四、折叠真正有效的场景：未压缩的多作用域

现有 `treesitter` 的常量表是**全局 map**（`extractor.vars`），不区分作用域。
多个模块各自声明同名短变量时，后写的覆盖先写的：

```js
(function(){ const n="/api/v1"; http.get(n+"/users") })();
(function(){ const n="/static"; http.get(n+"/logo.png") })();
(function(){ const n="/admin";  http.get(n+"/panel") })();
```

| | 提取结果 |
|---|---|
| 原始 | `/admin/users`(错) `/admin/logo.png`(错) `/admin/panel`(对) —— **4 条错 2 条** |
| 折叠后 | `/api/v1/users` `/static/logo.png` `/admin/panel` —— **4 条全对** |

折叠把变量消掉了，作用域问题自然不存在。这是 esbuild 唯一有实证优势的场景。

但**真实压缩产物里这个场景被消解了**：terser 会内联单次使用的常量，
`const n="/api/v1"; get(n+"/users")` 在产物里往往已经是 `get("/api/v1/users")`。
只有未压缩部署（webpack development 模式、没开 minify）才会保留这种形态。
本次 7 个真实样本里，压缩产物 7/7 都没有触发。

## 五、第二项理论优势：import 溯源

```js
import { get as fetchIt } from "axios";
fetchIt("/api/alias-get");
```

| | 结果 |
|---|---|
| 现状 | URL 拿到，但 **method 为空**（`fetchIt` 不在动词表，也不是 HTTP 动词） |
| esbuild AST | 有 `EImportIdentifier.Ref` → Symbol → import record，能追到 axios 的 `get` |
| 折叠预处理 | 无帮助（`fetchIt` 折叠后还是 `fetchIt`） |

这是 esbuild 直读 AST 的真实增量，但只影响**方法判定**，不影响 URL 提取。
而它同样能本地做：解析 `import` 语句建一张 `本地名 → (模块, 原名)` 映射表，
判定 callee 时先查表即可，不需要符号表、不需要 fork。

## 六、成本对比

| 方案 | 改动量 | 新增依赖 | 副作用 |
|---|---|---|---|
| A 折叠预处理 | ~30 行 + 每次解析多一遍 Transform | esbuild（MB 级） | 丢注释（含 sourceMappingURL）、重打印 +45% 体积、截断时需回退原文 |
| B 直读 AST | 拷 10+ 个 internal 包，并跟随上游升级 | esbuild 全量 | 同 A，外加长期维护 |
| C 本地修复 | ~100 行 | 0 | 无 |

C 的两项：

1. **vars 作用域化**——`gotreesitter.Node.Parent()` 可用，按最近的
   `function_declaration / function_expression / arrow_function / method_definition / program`
   划分作用域，查找时逐层向上。修掉第四节那个「4 条错 2 条」。
2. **import 别名映射**——修掉第五节的方法缺失。

## 七、结论

- esbuild 的**折叠**在真实产物上零增量，在未压缩多模块场景有效，但该场景用 C-1 同样能修。
- esbuild 的**AST** 只多一层 import 溯源，用 C-2 三十行就能覆盖。
- 两者都要付出「丢注释、重打印、截断回退、依赖维护」的代价。

所以不接入。真正该做的是 C。

**唯一可能翻盘的情况**：将来要直接分析 **TS / JSX 源码**（sourcemap 反解出来的 `.ts`、
或部署了未编译源码的站点）。tree-sitter 的 JS 语法不认类型注解和装饰器，那时 esbuild
的语法覆盖才是不可替代的。目前采集对象是浏览器实际加载的 JS，不涉及。

## 八、未验证

- 真实「未压缩部署」站点样本量为 0（第四节的数据来自合成夹具，形态与 webpack 模块表一致，
  但没有真实站点点位）。
- 折叠在 >2 MB 文件上的行为未测：这类文件本来就会被 `MaxBodySize` 截断，
  而截断后 esbuild 必报错（`Unterminated string literal`），只能回退原文。
- B 方案没有落地验证，`internal` 包的拷贝成本是按依赖链估算的，未实测。
