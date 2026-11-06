# 实施现状审计

日期：2026-11-06
范围：`assetcrawler` 全部已实现能力 + 遗留债务。
所有数字来自本地实测，非估算。

---

## 一、总量

| 项 | 数值 |
|---|---|
| Go 代码 | 6559 行（含测试 1260 行、testdata 原型 371 行） |
| 包 | 8 个（crawler / fetcher / extractor / frontier / jsext / routegraph / cmd + 2 个引擎实现） |
| 测试用例 | 43 个 |
| 覆盖率 | crawler 72.9% · extractor 80.3% · frontier 77.2% · jsext 81.2% · routegraph 81.6% |
| 直接依赖 | 3 个（goja / gotreesitter / x/net） |
| 默认二进制 | 15.22 MB |
| `-tags routeexec` | 23.70 MB |
| 未推送提交 | 11 个 |

覆盖率有两个 0%：`fetcher`（无测试文件）与 `cmd/assetcrawler`（无测试文件）。
`jsext/goja`、`jsext/treesitter` 显示 0% 是统计口径——它们的用例写在 `jsext` 包做黑盒测试。

---

## 二、能力现状

| 能力 | 状态 | 证据 |
|---|---|---|
| HTML 20+ 标签 / 注释 / 响应头 / robots / sitemap | 完成 | 探针夹具 11/19 |
| JS AST 提取（拼接、常量传播、方法、参数） | 完成 | 方法 5/5 |
| chunk 文件名恢复（webpack `u` / vite / 动态 import） | 完成 | vue 夹具拿到 `chunk-101` 200 |
| 前端路由表（括号栈） | 完成 | 端到端用例断言 `kind=route` |
| 执行兜底（`-tags routeexec`） | 完成但可选 | 4 个用例含超时保护 |
| 表单参数合成 | **未做** | 参数 1/3 |
| JSON 响应体解析 | **未做** | `/dyn/from-fetch` 仍拿不到 |
| CSP / og / twitter / sourcemap sources / data-* | **未做** | 探针 8 项 ✗ |

真实站点（testasp）37 条，与基线差异仍是站点内容变化（`showthread?id=4,5` 已下线）。

---

## 三、发现的问题（按严重度）

### P1-1 超过 4MB 的输入会静默截断

`lexAll` 把 token 全部存进切片，上限 `maxTokens = 2<<20`，超限后**静默停止**，没有返回值或日志提示。

实测：

```
 1MB 源码 → chunks=1     （清单在尾部，能拿到）
 4MB 源码 → chunks=0     （清单在尾部，拿不到）
 8MB 源码 → chunks=0
```

当前爬虫链路不会踩到，因为 `MaxBodySize` 默认 2MB 会先截断。
但两个场景会踩：把 `routegraph.Extract` 当库用直接喂大文件；或者调大 `MaxBodySize`。
**这是正确性问题，不是性能问题**——结果是「看起来没找到」，而不是「报错」。

### P1-2 内存放大 95 倍

```
BenchmarkExtract1MB: 34.5 ms/op, 94.5 MB/op, 119,501 allocs/op
```

输入 1MB，分配 94.5MB。主因是 `Token.Text()` / `Value()` 每次调用都做
`string(src[a:b])`，而这两个方法在扫描循环里被高频调用；其次是 token 切片本身
（`Token` 40 字节 × 数十万 token）。

默认并发 10、body 上限 2MB，峰值分配可达 GB 级。现在没炸是因为 GC 跟得上，
但这是最容易在真实大站上出问题的一处。

修法不复杂：加一组不分配的字节比较辅助（`tl.is(i, "{"`)，
只在最终产出结果时做 `string()` 转换。

### P2-1 `-route-exec` 在默认构建里静默失效

`extract_stub.go` 返回 `ErrExecUnavailable`，但 CLI 的 `OnError` 只在 `-v` 时挂载，
所以不带 `-tags routeexec` 却传了 `-route-exec` 时**什么提示都没有**，用户会以为功能没生效。

### P2-2 路由带参数也照样抓

`/admin/users/:id` 会真的发请求。SPA 通常返回同一个 `index.html`（200），
等于把首页重复抓 N 次；传统站点则是一串 404。
`Kind=route` 目前只是标记，没有对应的抓取策略。

### P3 文档与实际漂移

- `PLAN.md`：0 项标记完成，但 P0-1 / P0-4 其实已经做完。
- `testdata/probe/expected.json`：记录 12 项未实现，实际只剩 8 项
  （JS 拼接、常量传播、CSS url、多资源已覆盖），且 `check.py` 不校验这个字段。
- `README.md` 的能力清单已更新，但「已知局限」章节没提 4MB 截断与内存放大。

---

## 四、结构性债务

| 项 | 现状 | 影响 |
|---|---|---|
| 双 JS 引擎并存 | treesitter 使二进制约 8MB，仅用于对比与 ESM 解析 | 交付体积；但 ESM 包确实只有它能解析 |
| `fetcher` 零单测 | 限速、重试、重定向链只靠端到端间接覆盖 | 改了不易发现回归 |
| `go.mod` 里的 pprof / regexp2 | 仅 `-tags routeexec` 才真正链接（已用 `go list -deps` 验证） | 供应链审计时容易被误判 |
| `testdata/` 三个原型 | `routeprobe`(162) `routeprobe_esm`(34) `probe/jsext_prototype.go`(175) 均已被正式实现取代 | 371 行冗余，读者会混淆哪份是活的 |
| `LooksLikeBundle` 做全量 `string(body)` | 每个 JS 响应多一次完整拷贝 | 2MB body → 2MB 额外分配 |

---

## 五、建议顺序

**立刻（正确性）**

1. `lexAll` 截断要么返回 `truncated bool` 并让上层记录，要么改成分块扫描。
   最低成本：把上限提到 4M 并在截断时置标志，`Extract` 返回值带 `Truncated`。
2. `-route-exec` 在未编入时启动即报错退出，而不是逐文件静默失败。

**短期（性能）**

3. 去掉 `Text()/Value()` 的高频字符串分配，目标把 1MB 的 94MB 压到 10MB 以内。
4. `LooksLikeBundle` 改用 `bytes.Contains`。

**中期（能力）**

5. P0-2 表单参数合成（探针里 `q`/`page` 两个参数就差这一步，约 40 行）。
6. P0-3 JSON 响应体解析（一条 `{"next":"/api/page/2"}` 现在完全丢）。

**清理**

7. 删掉三个原型目录，`PLAN.md` 补完成标记，`expected.json` 与实际对齐并让 `check.py` 校验。
