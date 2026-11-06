# 增强路线（下一阶段）

日期：2026-11-06 20:20
基线：`fdee640`，二进制 23.74 MB，探针 17/19，97 个用例
状态：**A / B / C / D 四批已全部实施**（见下），本文档保留为记录与后续延伸。
已定结论放最后一节，避免反复讨论。

---

## 一、缺口总览（本批已全部关闭，保留以说明当时判断依据）

| 维度 | 现状 | 缺口 | 批次 |
|---|---|---|---|
| 准确性 | 常量表是全局 map | 跨模块同名变量会产出**错误 URL**（实测 4 条错 2 条） | A |
| 方法 | 探针 5/5，压缩产物可判 | `import {get as x}` 别名后方法全空 | A |
| URL 召回 | 探针 13/19 | og/twitter meta、`data-*`、sourcemap `sources` | B |
| 参数 | 只有名字，3/3 | 表单里静态可取的值完全没用 | C |
| 深层页面 | 带 `:param` 路由只报告不抓 | SPA 内页拿不到，而那里 API 调用最集中 | C |
| 工程 | CLI 0% 覆盖、无 CI | 每次改动靠手工验证 | D |

判断依据：批次 A 是**正确性**问题（错 URL 比漏 URL 更贵，会吃掉抓取配额）；
B 是可验证的召回补齐；C 收益不确定、噪音风险高，必须带开关；D 是让后续改动不再靠人肉。

---

## 二、批次 A：正确性（✅ 已完成 2026-11-06）

### A-1 常量表按作用域隔离

- **问题**：`jsext/treesitter` 的 `extractor.vars` 是全局 `map[string]string`，
  `collectConsts` 后写覆盖先写。多个模块各自 `const n=...` 时会互相污染，
  产出源码里不存在的地址（ISSUES P0-4，证据见 `ESBUILD-ANALYSIS.md` 第四节）。
- **做法**：`vars` 改为 `map[scopeID]map[string]string`，`scopeID` 取最近的
  `function_declaration / function_expression / arrow_function / method_definition / program`
  的起始字节；`eval` 时从调用点所在作用域逐层向上查找（`gotreesitter.Node.Parent()` 可用）。
- **验收**：
  - 新增夹具（三个 IIFE 各自声明 `n`）：4/4 全部正确（现状 2/4）
  - 现有 77 个用例全绿，`testasp-2026-11-06b` 基线 106 条不变
  - 549 KB 的 webpack 包解析耗时不出现明显退化（27 KB 文件改动前 16.6 ms，作参考）
- **风险**：`Parent()` 逐层向上可能带来开销；先用真实大文件实测，再决定是否缓存 scope 链。

### A-2 import 别名映射到 HTTP 方法

- **问题**：`import { get as fetchIt } from "axios"; fetchIt("/api/x")` —— URL 拿得到，
  `method` 为空（ISSUES P1-8）。
- **做法**：`collectImports` 时顺带建 `本地名 → (模块, 原名)` 映射，
  `calleeOf` 判定方法前先查表；`import request from "@/utils/request"` 这类默认导入
  只能确定「是包装过的请求库」，方法仍留空，不做猜测。
- **验收**：别名夹具中 `fetchIt` / `sendIt` 分别判出 GET / POST（现状为空）。
- **说明**：这条只影响未压缩 ESM；压缩产物已退化成 `(0,n.get)`，靠动词判定本来就能命中。

---

## 三、批次 B：召回补齐（✅ 已完成 2026-11-06，探针 13 → 17）

| 项 | 做法 | 成本 | 备注 |
|---|---|---|---|
| B-1 og / twitter meta | 现有 `meta` 只认 `refresh`，补 `property=og:url`、`name=twitter:image` | 25 行 | 分享页变体，命中率中等 |
| B-2 `data-*` 泛化 | 现在只认 `data-src`，改为取所有 `data-*` 值中形状像路径的 | 20 行 | 需限流 + 形状判定，否则误报 |
| B-3 sourcemap `sources` | 已会抓 `.map` 但不解析；只取相对路径（`webpack://` 伪路径不可访问） | 45 行 | 命中率预计偏低，放最后 |

- **验收**：`check.py` **17/19**；CSP 两项当时按「不接入」处理，2026-11-06 20:40 已改为
  线索接入，现为 19/19（见 STATUS 第七节复盘）。
  `expected.json` 的 `implemented` 字段同步更新；三份基线无回退。
- **注意**：`implemented` 与实际不一致时 `check.py` 会打印「记录与实际不符」，
  改完必须同步，否则下次跑会误判。

---

## 四、批次 C：参数与深层页面（✅ 已完成 2026-11-06，全部带开关）

### C-1 表单参数值枚举（保守版）

```html
<select name="sort"><option value="price_asc">…</option><option value="newest">…</option></select>
<input type="hidden" name="cat" value="12">
```

输出 `sort=price_asc|newest`、`cat=12` 这样的候选值，**不生成 URL**。
零额外请求、零误报，人工判读时直接可用。激进版（按值合成 URL 入队）单独做开关，先不实现。

### C-2 路由实例化 `-route-expand`

`/user/:id` 现在 `kind=route`、`status=0`，不发起请求。加开关后用占位值（`/user/1`）试抓，
必须带条数上限与去重；默认关闭。

### C-3 配置对象里的 base 地址

`window.__ENV__={VUE_APP_API:"/api/v3", STATIC:"/static/"}` 的属性值没有走
「前缀 / 端点」形状判定，会贡献 1~3 条 404（ISSUES P1-2）。
修法是把 `endpointShaped` 也用于对象属性值，但要先定规则避免误伤
`{plain:"/api/in-js-file"}`——建议只排除以 `/` 结尾的、以及段数 ≤ 2 且无扩展名的。

- **验收**：`vue_fixture` 输出的 404 从 2 条降到 0；探针噪音仍为 0。
- **前置**：C 批次三项都缺真实样本的量级判断（噪音率未知），
  建议先用 3~5 个真实站点各跑一次，再决定开关默认值。

---

## 五、批次 D：工程（✅ 已完成 2026-11-06）

| 项 | 做法 | 说明 |
|---|---|---|
| D-1 CLI 测试 | 把 `flag → Options` 的映射做成表驱动用例 | 现在 0% 覆盖，新加 flag 没有任何保护 |
| D-2 CI | `gofmt -l`、`go vet`、`go test -race`、`-s -w` 构建、`check.py` | 目前全靠手工 |
| D-3 大站内存上限 | `seen` / `results` 加上限与溢出策略 | 10 万 URL 规模未测，不急 |

D-1 建议排在 C 之前：C 会新增 flag，没有测试兜底等于裸奔。

---

## 六、待评估（不是承诺）

- **treesitter 裁剪构建**：只编 JS 语法。`gotreesitter` 有 `grammar_subset` 机制，
  但实测 JS 语法 blob 只有 40 KB、整个包符号 4.09 MB，**大头在运行时不在语法表**，
  收益可能远小于预期。要先测再决定，别再重蹈「删 treesitter 省 8MB」的误判。
- **参数激进版**、**分页推进**：都会把目标压得更狠，需要先有噪音率数据。
- **TS / JSX 源码解析**：现在采集对象是浏览器实际加载的 JS，不涉及。
  若将来要分析 sourcemap 反解出的 `.ts`，才需要重新考虑 esbuild（见 `ESBUILD-ANALYSIS.md`）。

---

## 七、已定结论（不再讨论）

| 结论 | 依据 |
|---|---|
| treesitter 必须保留 | ESM 解析 + 截断容错；删了会让 Vite 站点入口 100% 失败（`ESM-ANALYSIS.md`） |
| 不接入 esbuild | 真实产物零增量，优势可本地覆盖（`ESBUILD-ANALYSIS.md`） |
| ~~CSP 只做独立工具包~~（已改） | 原判据「48 主机仅 3 同站」口径有误：非同站会被 scope 挡掉，不该算进噪音。现改为线索接入、只报告不入队 |
| 执行兜底不做成可选编译 | 能力与二进制必须唯一对应；已默认编入，运行时由 `-route-exec` 控制 |
| 不做敏感信息提取、目录 fuzz、漏洞检测、指纹识别、JS 渲染 DOM | 已定边界 |

---

## 八、执行顺序

```
A-2 import 别名（30 行，独立、低风险，先磨刀）
A-1 作用域化（60 行，修正确性，动核心数据结构，需完整回归）
B-1 + B-2（45 行，探针 13 → 15，一起做省一次回归）
B-3 sourcemap（45 行，独做，命中率待验证）
D-1 CLI 测试（在加新 flag 之前把网兜上）
C-3 配置对象 base（20 行，先把已知的 404 修掉）
C-1 参数值保守版（60 行）
C-2 路由实例化（40 行，风险最高，放最后）
D-2 CI → D-3 内存上限
```

每批完成后的固定动作：`gofmt -l` / `go vet` / `go test -race` / `check.py` /
三份基线对比（现代站比 `urls-stable.txt`，否则每次部署都会被判成几十条增删）。
