# 实施情况

日期：2026-11-06 20:15
代码：`fdee640` 系列，全部改动**未推送**到 origin
所有数字均为本地实测，未验证的会明确标注。

---

## 一、总量

| 项 | 数值 |
|---|---|
| Go 代码 | 9980 行（其中测试 3185 行） |
| 包 | 8 个（含 2 个 JS 引擎实现、1 个独立工具包） |
| 测试用例 | 97 个 |
| 直接依赖 | 3 个（`dop251/goja`、`odvcencio/gotreesitter`、`golang.org/x/net`） |
| 二进制（默认，`-s -w`） | 23.74 MB |
| 二进制（含调试符号） | 32.94 MB |

## 二、覆盖率

| 包 | 覆盖率 | 说明 |
|---|---:|---|
| `csp` | 86.3% | CSP / Report-To 响应头解析，纯函数 |
| `extractor` | 84.8% | HTML / 头 / CSS / JSON / 表单 / sourcemap |
| `routegraph` | 82.6% | 词法、chunk、路由表、截断、执行兜底 |
| `jsext` | 81.2%（`-coverpkg` 82.1%） | 两个引擎的黑盒测试 |
| `crawler`（顶层编排） | 78.9% | 端到端 + 配额 + 引擎链 + 路由展开 |
| `frontier` | 78.3% | 归一化、scope、去重、上限 |
| `fetcher` | 74.3% | 连接池、限速、重试、截断 |
| `cmd/assetcrawler` | 53.0% | flag → Options 映射表驱动；main 的 IO 分支未覆盖 |

口径提醒：`jsext/goja` 与 `jsext/treesitter` 在 `go test -cover ./...` 下显示 0%，
那只是「用例写在 jsext 包做黑盒测试」的统计假象，真实覆盖见 `-coverpkg` 的 82.1%。

## 三、包结构与职责

| 包 | 职责 | 依赖 |
|---|---|---|
| `assetcrawler` | 编排：种子、BFS、结果组装、引擎选择、路由展开 | 下面全部 |
| `fetcher` | HTTP：连接池、限速、按 host 并发、重试、重定向链、body 截断 | 仅标准库 |
| `extractor` | 纯函数提取：HTML / 响应头 / CSS / JSON / 表单 / sourcemap | 仅 `x/net` |
| `frontier` | 归一化、scope 判定、去重、已见集合上限 | 仅标准库 |
| `routegraph` | 打包产物：chunk 文件名、前端路由表、执行兜底 | 标准库 + goja（仅 `exec.go`） |
| `jsext/{goja,treesitter}` | JS AST：拼接求值、方法、参数 | goja / gotreesitter |
| `csp` | CSP / Report-To 响应头解析 | 仅标准库 |

`extractor` 通过 `Options.JSExtract` 注入引擎，自身不 import 任何引擎；
`csp` 完全独立，不被采集流程调用。

## 四、本轮实施内容

| 提交 | 内容 | 验证 |
|---|---|---|
| `31613ea` | 去掉 `routeexec` 构建标签，执行兜底默认编入 | 二进制 15.22→23.70 MB，探针无回退 |
| `a569199` | 评估 esbuild：真实产物零增量，结论不接入 | 7 个真实产物 + 3 个夹具对照 |
| `264d5e9` | **A-2** import 别名映射到 HTTP 方法 | `fetchIt`/`sendIt` 判出 GET/POST |
| `effed17` | **A-1** 常量表按作用域隔离 | 三 IIFE 夹具 4/4；wp_vendor 噪音 -21 |
| `163fc07` | **B** og/twitter、`data-*`、sourcemap sources | 探针 13→17/19 |
| `12aa8ee` | **C** 配置对象形状判定、表单取值、路由实例化 | 新增 4 组用例，含开关两态 |
| `fdee640` | **D** CLI 表驱动测试、CI、内存上限 | cmd 覆盖率 0→53% |

## 五、验收数据

**探针夹具**（`testdata/probe/check.py`，默认参数）

```
[载体]   17/19   [方法]   5/5   [参数]   3/3   [噪音]   0 条
结论: 无回归（覆盖 17/19，未实现项按设计保留）
```

未覆盖的 2 项都是 CSP（`report-uri`、`connect-src`），按既定结论只做工具包、
不接入采集流程，因此 19/19 不可达——`check.py` 的退出码口径已改为
「已实现项不回归」而不是「全部达标」，否则 CI 会长期红着。

**现代站基线**（`-d 2 -max-urls 300`）

| 站点 | 总数 | JS 文件 | JS 200 | 404 |
|---|---:|---:|---:|---:|
| vuejs.org | 298 | 17 | 17 | 8 |
| vite.dev | 296 | 38 | 38 | 3 |

**传统站基线**（`testasp-2026-11-06b`，`-d 3`）：106 条（200: 104 / 404: 2）。

## 六、新增能力（本轮）

| 能力 | 开关 | 说明 |
|---|---|---|
| 表单参数候选值 | 默认开启 | `ParamValues` 输出 hidden 默认值、select/radio/checkbox 候选集合 |
| 路由实例化 | `-route-expand` | `:id` → `1`、`*rest` → `index`，`-route-expand-max` 限流（默认 50） |
| 已见集合上限 | `-max-seen`（默认 200000） | 超出后拒绝入队并在结束时上报，不静默截断 |
| 执行兜底 | `-route-exec` | 默认关闭：会执行目标站代码 |

## 七、明确未做

| 项 | 原因 |
|---|---|
| CSP 接入采集流程 | 同站比例低、第三方噪音高（MDN 48 主机仅 3 个同站） |
| 参数激进版（按候选值合成 URL） | hidden 常带 csrf/session，拼出来必然失败；需要先有噪音率数据 |
| 分页推进（`?page=N`） | 容易把目标压垮，也容易无限翻页，缺「内容重复即停」判定 |
| TS / JSX 源码解析 | 采集对象是浏览器实际加载的 JS；若将来要分析 `.ts` 才需重新评估 esbuild |
| treesitter 裁剪构建 | `grammar_subset` 机制存在，但 JS 语法表只有 40 KB，大头在运行时，收益待测 |

以上都在 `ISSUES.md` 里按 P0/P1/P2 分级，并标了证据与修法成本；
下一步延伸见 `ROADMAP.md`。

## 八、仓库状态

- 相对 `origin/main` 有未推送提交，以 `git status` 为准
- 工作区干净，`gofmt` / `go vet` 无输出，`go test -race ./...` 全绿
- CI：`.github/workflows/ci.yml`（gofmt / vet / race / 构建 / 探针）
