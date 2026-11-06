# 实施情况

日期：2026-11-06 20:40
代码：`CSP 线索接入` 系列，全部改动**未推送**到 origin
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
| 待提交 | **CSP 线索接入采集流程**（只报告不入队） | 探针 17→19/19 |

## 五、验收数据

**探针夹具**（`testdata/probe/check.py`，默认参数）

```
[载体]   19/19   [方法]   5/5   [参数]   3/3   [噪音]   0 条
结论: 全部达标
```

`check.py` 的退出码口径是「已实现项不回归」而不是「19/19」：当前虽然全满，
但后续如果再出现「按设计不接入」的载体，门槛不该跟着变红。

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
| CSP 线索 | `-no-csp` 可关 | 输出 `csp-source` / `csp-report`，**只报告不入队** |

## 七、CSP 接入的口径（复盘）

早期结论是「不接入」，依据是 MDN 48 个主机只有 3 个同站。**那个口径是错的**：
非同站主机在默认 `scope=subdomain` 下本来就会被 frontier 挡掉，不会入队。
真正该问的是「接入后结果里多出什么」，实测（8 个站点，150 条目）：

| 分类 | 数量 | 处置 |
|---|---:|---|
| 同 host | 3 | 跳过（等于当前页面） |
| 同根域子域 | 16 | 收，`csp-source` |
| 上报端点 | 7 | 收，`csp-report` |
| 外部域名 | 124 | 收（`scope=any` 时才有意义），默认场景下也是资产线索 |

处置方式是**只报告不入队**：
- `csp-source` 多为域名根，抓取实测 5 个样本只有 1 个 200，抓它只是拿配额换 404；
- `csp-report` 常带一次性 token，抓取等于向目标注入伪造上报。

实测收益（`-d 1/2`）：vuejs.org +1、stripe.com +23（其中 `c.stripe.dev`、
`c.stripe.global`、`c.stripe.partners` 是 Stripe 的其他 TLD，属于资产面增量）。

被排除的三类：`frame-ancestors`（入站语义，列的是外部父页面，vuejs 一条就带进
6 个第三方站）、通配主机（无法定位）、同 host 来源（只是重复当前页面）。

## 八、-route-expand 的实测结论（保持默认关闭）

| 站点 | 恢复路由 | 带参路由 | 展开效果 |
|---|---:|---:|---|
| vuejs.org | 0 | 0 | 无（VitePress 无显式 routes 数组） |
| antdv.com | 7 | 1 | `/1` 返回 200，但内容与首页 md5 相同 |

SPA 的 history fallback 让所有路径返回同一份 `index.html`；不发请求执 JS，
就看不到路由渲染后的内容。带正则约束的参数（`:lang(.*)`）已改为不展开。

结论：**这条能力保留但不默认开**，它的价值要到「服务端按路径渲染不同内容、
且路径带 :param」的站点上才成立，这一类目前没有样本。

## 九、明确未做

| 项 | 原因 |
|---|---|
| 参数激进版（按候选值合成 URL） | hidden 常带 csrf/session，拼出来必然失败；需要先有噪音率数据 |
| 分页推进（`?page=N`） | 容易把目标压垮，也容易无限翻页，缺「内容重复即停」判定 |
| TS / JSX 源码解析 | 采集对象是浏览器实际加载的 JS；若将来要分析 `.ts` 才需重新评估 esbuild |
| treesitter 裁剪构建 | `grammar_subset` 机制存在，但 JS 语法表只有 40 KB，大头在运行时，收益待测 |

以上都在 `ISSUES.md` 里按 P0/P1/P2 分级，并标了证据与修法成本；
下一步延伸见 `ROADMAP.md`。

## 十、仓库状态

- 相对 `origin/main` 有未推送提交，以 `git status` 为准
- 工作区干净，`gofmt` / `go vet` 无输出，`go test -race ./...` 全绿
- CI：`.github/workflows/ci.yml`（gofmt / vet / race / 构建 / 探针）
