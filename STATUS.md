# 实施情况

日期：2026-11-06 18:55
代码：`9a2810f` 系列，全部改动**未推送**到 origin
所有数字均为本地实测，未验证的会明确标注。

---

## 一、总量

| 项 | 数值 |
|---|---|
| Go 代码 | 8301 行（其中测试 2426 行） |
| 包 | 8 个（含 2 个 JS 引擎实现、1 个独立工具包） |
| 测试用例 | 77 个 |
| 直接依赖 | 3 个（`dop251/goja`、`odvcencio/gotreesitter`、`golang.org/x/net`） |
| 二进制（默认，`-s -w`） | 23.70 MB |
| 二进制（含调试符号） | 32.90 MB |

## 二、覆盖率

| 包 | 覆盖率 | 说明 |
|---|---:|---|
| crawler（顶层编排） | 75.7% | 端到端 + 配额 + 引擎链 |
| `extractor` | 83.8% | HTML / 头 / CSS / JSON / 表单参数 |
| `routegraph` | 82.6% | 词法、chunk、路由表、截断、执行兜底 |
| `jsext` | 81.2%（`-coverpkg` 82.1%） | 两个引擎的黑盒测试 |
| `frontier` | 77.2% | 归一化、scope、去重 |
| `fetcher` | 74.3% | 本轮从 0% 补到 74.3% |
| `csp` | 86.3% | 新增独立包 |
| `cmd/assetcrawler` | 0% | 无测试文件 |

口径提醒：`jsext/goja` 与 `jsext/treesitter` 在 `go test -cover ./...` 下显示 0%，
那只是「用例写在 jsext 包做黑盒测试」的统计假象，真实覆盖见 `-coverpkg` 的 82.1%。

## 三、包结构与职责

| 包 | 职责 | 依赖 |
|---|---|---|
| `assetcrawler` | 编排：种子、BFS、结果组装、引擎选择 | 下面全部 |
| `fetcher` | HTTP：连接池、限速、按 host 并发、重试、重定向链、body 截断 | 仅标准库 |
| `extractor` | 纯函数提取：HTML / 响应头 / CSS / JSON / 表单 | 仅 `x/net` |
| `frontier` | 归一化、scope 判定、去重 | 仅标准库 |
| `routegraph` | 打包产物：chunk 文件名、前端路由表、执行兜底 | 标准库 + goja（仅 `exec.go`） |
| `jsext/{goja,treesitter}` | JS AST：拼接求值、方法、参数 | goja / gotreesitter |
| `csp` | CSP / Report-To 响应头解析 | 仅标准库 |

`extractor` 通过 `Options.JSExtract` 注入引擎，自身不 import 任何引擎；
`csp` 完全独立，不被采集流程调用。

## 四、本轮实施内容

| 提交 | 内容 | 验证 |
|---|---|---|
| `5a54566` | 截断可感知、routegraph 内存 94.5MB→6.7MB、表单参数、JSON 响应体 | 探针 11→13/19，参数 1/3→3/3 |
| `a3377c6` | 默认引擎改 `auto`（goja→treesitter 串联）、goja 显式拒绝 ESM、`export ... from`、前缀/端点判定、CSS 选择器过滤 | 真实 Vite 入口从「100% 解析失败」到可用 |
| `ca03237` | 参数不改写 URL、sitemap 降为 depth 1、fetcher 补 7 个用例 | 60 配额下 JS 0→10，fetcher 0→74.3% |
| `c3493b8` | 修 data URI 片段引用、vite mapDeps 前缀、非 URL 字面量；固化两份现代站基线 | 404：vuejs 32→8，vite 11→3 |
| `fe81f73` | 新增独立 `csp` 包（纯函数，不接入采集流程） | 覆盖率 86.3%，真实头夹具 |
| 本轮 | 去掉 `routeexec` 构建标签：执行兜底默认编入，删 `exec_stub.go` 与 `ExecAvailable` | 二进制 15.22→23.70 MB，`go test -race` 全绿，routegraph 82.6% |

## 五、验收数据

**探针夹具**（`testdata/probe/check.py`，默认参数）

```
[载体]   13/19   [方法]   5/5   [参数]   3/3   [噪音]   0 条
```

未覆盖的 6 项：og/twitter meta、`data-*`、CSP×2、sourcemap `sources`。

**现代站基线**（`-d 2 -max-urls 300`）

| 站点 | 总数 | JS 文件 | JS 200 | 404 |
|---|---:|---:|---:|---:|
| vuejs.org | 298 | 17 | 17 | 8 |
| vite.dev | 296 | 38 | 38 | 3 |

**传统站基线**（`testasp-2026-11-06b`，`-d 3`）

| 总数 | 200 | 404 | 深度分布 |
|---:|---:|---:|---|
| 106 | 104 | 2 | 0:1 / 1:11 / 2:39 / 3:55 |

**ESM 引擎对比**（同一份真实产物）

| 文件 | goja | treesitter |
|---|---|---|
| `vite.dev/assets/app.js` | 解析失败 | 2 个 chunk |
| `vuejs.org/assets/app.js` | 解析失败 | 12 条（9 个 chunk） |

这是默认改成串联的直接原因；详见 `ESM-ANALYSIS.md`。

## 六、基线状态

| 目录 | 状态 |
|---|---|
| `testasp-2026-11-06b` | **现行**，106 条 |
| `testasp-2026-11-06` | 已失效（站点内容变化 + sitemap 调度调整），MANIFEST 已标注 |
| `vuejs.org-2026-11-06` | 现行，含 `urls-stable.txt` |
| `vite.dev-2026-11-06` | 现行，含 `urls-stable.txt` |

现代站文件名带内容哈希，回归必须比 `urls-stable.txt`（hash 归一化），
否则每次部署都会被判成「几十条消失 + 几十条新增」。

## 七、明确未做

| 项 | 原因 |
|---|---|
| CSP 接入采集流程 | 同站比例低、第三方噪音高（MDN 48 主机仅 3 个同站），只作为工具函数 |
| og/twitter meta、`data-*`、sourcemap `sources` | 剩余 6 项载体，合计约 90 行，未做 |
| 带 `:param` 路由的实例化抓取 | 现只报告形状，缺开关 |
| 参数取值的枚举（select / radio / hidden） | 只记录参数名 |
| `cmd` 的 CLI 测试 | 0% 覆盖，flag 组合靠手工验证 |
| 大站点内存上限 | `seen` / `results` 常驻，10 万 URL 规模未测 |
| 接入 esbuild | 评估完成：真实产物零增量，优势可本地覆盖（`ESBUILD-ANALYSIS.md`） |

以上都在 `ISSUES.md` 里按 P0/P1/P2 分级，并标了证据与修法成本。

## 八、仓库状态

- 相对 `origin/main` 有未推送提交，以 `git status` 为准
- 工作区干净，`go vet` / `gofmt` 无输出，`go test -race ./...` 全绿
