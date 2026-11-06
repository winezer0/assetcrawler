# 实施现状审计

日期：2026-11-06（修复后更新）
范围：`assetcrawler` 全部已实现能力 + 遗留债务。
所有数字来自本地实测，非估算。

---

## 一、总量

| 项 | 数值 |
|---|---|
| Go 代码 | 6619 行（含测试 1393 行） |
| 包 | 8 个（crawler / fetcher / extractor / frontier / jsext / routegraph / cmd + 2 个引擎实现） |
| 测试用例 | 47 个 |
| 覆盖率 | crawler 71.3% · extractor 80.7% · frontier 77.2% · jsext 81.2% · routegraph 82.1% |
| 直接依赖 | 3 个（goja / gotreesitter / x/net） |
| 默认二进制 | 15.22 MB |
| `-tags routeexec` | 23.70 MB |
| 推送状态 | `5a54566` 已全部推送到 origin/main |

覆盖率还有两个 0%：`fetcher` 与 `cmd/assetcrawler` 没有测试文件。
`jsext/goja`、`jsext/treesitter` 显示 0% 是统计口径——用例写在 `jsext` 包做黑盒测试。

---

## 二、能力现状

| 能力 | 状态 | 证据 |
|---|---|---|
| HTML 20+ 标签 / 注释 / 响应头 / robots / sitemap | 完成 | 探针 13/19 |
| JS AST 提取（拼接、常量传播、方法、参数） | 完成 | 方法 5/5、参数 3/3 |
| 表单控件参数名 | 完成 | `/form/submit` 带 `params=q`；登录页可给出 username/password |
| JSON 响应体「下一跳」 | 完成 | 动态靶站 `/dyn/from-fetch`、vue 夹具 `/api/v3/products?page=2` |
| chunk 文件名恢复（webpack `u` / vite / 动态 import） | 完成 | vue 夹具 `chunk-101` 200 |
| 前端路由表（括号栈） | 完成 | 端到端断言 `kind=route`，带 `:param` 的只报告不抓取 |
| 执行兜底（`-tags routeexec`） | 完成但可选 | 4 个用例含超时保护 |
| CSP / og / twitter / sourcemap sources / data-* | 未做 | 探针剩余 6 项 ✗ |

真实站点（testasp）37 条，与基线差异仍是站点内容变化（`showthread?id=4,5` 已下线）。

---

## 三、本轮修复（原问题 → 现状）

### ✅ P1-1 超过 4MB 输入静默截断

原来 `lexAll` 触到 token 上限就停止，不返回任何信号，调用方只会看到「没找到清单」。

现在 `Result.Truncated` 会置位，并由爬虫层通过错误回调报出（`errBundleTruncated`）。

```
TestTruncationReported：6MB 输入 → Truncated=true；小文件 → false
```

### ✅ P1-2 内存放大 95 倍

三处根因，逐个处理：

| 处理 | 效果 |
|---|---|
| `lexAll` 预分配（`len(src)/2`，避免 append 倍增拷贝） | 消除 91% 的分配 |
| 路由扫描帧去掉 `map` 与 key 切片，改固定字段 | 分配次数 11.9 万 → 1.3 万 |
| `Token` 从 40 字节压到 12 字节（`Kind` uint8、偏移 int32、去掉 ValStart/ValEnd） | 常驻内存减半 |
| 热循环改用零分配比较（`is` / `identIs` / `identHasPrefix`） | 去掉每个 token 的字符串拷贝 |

```
BenchmarkExtract1MB（1MB 输入）：
  修复前  34.5 ms/op   94.5 MB/op   119,501 allocs/op
  修复后   7.9 ms/op    6.7 MB/op    13,278 allocs/op
```

### ✅ P2-1 `-route-exec` 静默失效

新增 `routegraph.ExecAvailable()`；CLI 启动时检查，未编入则打印构建命令并以**退出码 2** 退出（实测确认）。

### ✅ P2-2 带参数占位的前端路由照抓

`/user/:id`、`/files/*` 现在只输出形状（`kind=route`、`status=0`），不发请求——
SPA 上它们会重复抓首页，传统站点上是一串 404。

### ✅ P3 文档漂移

- `PLAN.md`：P0-1/P0-2/P0-3/P0-4 标为已完成，P1-1 标为部分完成，其余标未做。
- `testdata/probe/expected.json`：13/19 已覆盖，与实测一致（`check.py` 的「记录与实际不符」已归零）。
- 删除已被正式实现取代的三个原型目录（371 行）。

### ✅ 顺带完成的 P0-2 / P0-3

- `formParamNames`：表单控件名作为参数形状带出（跳过 submit/button/reset/image/file 与 disabled）。
- `extractor.FromJSON`：递归提取响应体里以 `/` 或 `http(s)://` 开头的字符串，上限 200 条。

---

## 四、仍然存在的债务

| 项 | 现状 | 影响 |
|---|---|---|
| `fetcher` 零单测 | 限速、重试、重定向链只靠端到端间接覆盖 | 改动不易发现回归 |
| 双 JS 引擎并存 | treesitter 使二进制约 8MB，用于对比与 ESM 解析 | ESM 包确实只有它能解析，不能简单删 |
| `go.mod` 里的 pprof / regexp2 | 仅 `-tags routeexec` 才链接（`go list -deps` 已验证） | 供应链审计时易被误判 |
| `maxTokens` 上限 | 2M token ≈ 4MB 源码；超限虽会报告，但仍拿不全 | 调大 `MaxBodySize` 时要留意 |
| 探针剩余 6 项 | CSP(2)、og/twitter meta(2)、data-*(1)、sourcemap sources(1) | 都是低成本的补全项 |

---

## 五、建议

**短期（能力补全，都是小改动）**

1. meta 泛化：`og:url` / `twitter:image` 等 `property`/`name` 前缀判定（约 20 行）。
2. CSP 响应头：`report-uri` / `connect-src` / `script-src` 的地址抽取（约 60 行）。
3. sourcemap `sources` 与 `data-*` 泛化。

**中期（质量）**

4. 给 `fetcher` 补单测：重试退避、重定向链、按 host 并发、body 截断。
5. 决定 treesitter 的去留——若只为对比，可以移到独立 build tag 或工具目录。
