> **已失效**：站点内容变化 + sitemap 调度调整后，本基线为 44 条，
> 现行基线为 `testasp-2026-11-06b`（106 条），请用后者做回归。

# 基线：testasp-2026-11-06（已被 v2 取代）

采集日期：2026-11-06
目标：`http://testasp.vulnweb.com`（Acunetix 公开测试靶站）
环境：Windows / amd64 / Go 1.26.4
口径：深度 3、并发 10、超时 10s

## 结果

| 工具 | 站内唯一 | 外链 | 原始条数 | 说明 |
|---|---:|---:|---:|---|
| hakrawler | 30 | 5 | 48 |  |
| gospider | 0 | 0 | 0 | 环境不兼容，见 tools/README.md |
| cariddi | 37 | 0 | 37 |  |
| URLFinder-m1 | 11 | 5 | 28 |  |
| URLFinder-m2 | 28 | 462 | 819 | 外链 462 条，站内占比仅 6% |
| assetcrawler | 44 | 0 | 44 |  |
| **合并去重** | **49** | — | — | 所有工具站内 URL 的并集 |

`assetcrawler` 覆盖 44/49，未覆盖 5 条，
且这些未覆盖项都有明确原因（见「已知差异」）。

## 原始输出（已清理）

`raw/` 目录下的各工具原始输出在 2026-11-06 的清理中被误删，**不影响对比功能**：
`baseline.py diff` 只依赖本目录下的 `urls-<tool>.txt`（已按统一规则归一化），
原始输出仅在需要「换口径重新归一化」时才用到。

如需重建原始输出：

```bash
cd tools
# 按 tools/README.md 的复现命令重跑各工具，输出到临时目录
mkdir -p /tmp/raw && cd /tmp/raw
# …… 见 tools/README.md …
# 然后重新快照（会覆盖本基线的 urls-*.txt）
python ../baseline.py snapshot . -o ../baseline/testasp-2026-11-07
```

注意：`testasp.vulnweb.com` 的首页帖子列表是动态的，重跑得到的 URL 数量会有小幅波动，
因此**重建后应作为新基线（换个日期目录），不要覆盖本基线**。

## 已知差异（比对时不要误判为回退）

1. **`cariddi` 独有的 `robots.txt` / `sitemap.xml`**
   它把这两个文件本身当结果输出；`assetcrawler` 把它们当采集基础设施，
   会读取但不作为结果记录。这是设计选择，不是漏抓。

2. **`URLFinder` 独有的 `text/css` 系列**
   形如 `http://testasp.vulnweb.com/text/css`、`/Templates/text/css`，
   是从 `Content-Type: text/css` 响应头误提取的假路径，属误报。

3. **`URLFinder` 未归一化的 `Templates/../Default.asp`**
   指向的其实就是 `/Default.asp`。基线里已通过 canonical 归一化折叠掉，
   所以不计入站内唯一数。

4. **`gospider` 为 0**
   本机运行环境不兼容（详见 `tools/README.md`），不代表工具能力。

## 基线 URL 全集（49 条）

```
testasp.vulnweb.com/
testasp.vulnweb.com/Default.asp
testasp.vulnweb.com/Images/logo.gif
testasp.vulnweb.com/Login.asp?RetURL=/Default.asp?
testasp.vulnweb.com/Login.asp?RetURL=/Search.asp?
testasp.vulnweb.com/Login.asp?RetURL=/Templatize.asp?item=html/about.html
testasp.vulnweb.com/Login.asp?RetURL=/showforum.asp?id=0
testasp.vulnweb.com/Login.asp?RetURL=/showforum.asp?id=1
testasp.vulnweb.com/Login.asp?RetURL=/showforum.asp?id=2
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=0
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=1
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=2
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=3
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=4
testasp.vulnweb.com/Login.asp?RetURL=/showthread.asp?id=5
testasp.vulnweb.com/Register.asp?RetURL=/Default.asp?
testasp.vulnweb.com/Register.asp?RetURL=/Search.asp?
testasp.vulnweb.com/Register.asp?RetURL=/Templatize.asp?item=html/about.html
testasp.vulnweb.com/Register.asp?RetURL=/showforum.asp?id=0
testasp.vulnweb.com/Register.asp?RetURL=/showforum.asp?id=1
testasp.vulnweb.com/Register.asp?RetURL=/showforum.asp?id=2
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=0
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=1
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=2
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=3
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=4
testasp.vulnweb.com/Register.asp?RetURL=/showthread.asp?id=5
testasp.vulnweb.com/Search.asp
testasp.vulnweb.com/Templates/Login.asp?RetURL=/Templates/MainTemplate.dwt.asp?
testasp.vulnweb.com/Templates/MainTemplate.dwt.asp
testasp.vulnweb.com/Templates/Register.asp?RetURL=/Templates/MainTemplate.dwt.asp?
testasp.vulnweb.com/Templates/text/css
testasp.vulnweb.com/Templatize.asp?item=html/about.html
testasp.vulnweb.com/avatars/noavatar.gif
testasp.vulnweb.com/robots.txt
testasp.vulnweb.com/showforum.asp?id=0
testasp.vulnweb.com/showforum.asp?id=1
testasp.vulnweb.com/showforum.asp?id=2
testasp.vulnweb.com/showthread.asp?id=0
testasp.vulnweb.com/showthread.asp?id=1
testasp.vulnweb.com/showthread.asp?id=2
testasp.vulnweb.com/showthread.asp?id=3
testasp.vulnweb.com/showthread.asp?id=4
testasp.vulnweb.com/showthread.asp?id=5
testasp.vulnweb.com/sitemap.xml
testasp.vulnweb.com/styles.css
testasp.vulnweb.com/text/css
testasp.vulnweb.com/text/text/css
testasp.vulnweb.com/x
```

## 基线失效条件

出现以下情况时应重建基线，而不是把差异当成回退：

- 目标站点内容变更（`testasp.vulnweb.com` 是公开靶站，页面可能被调整）
- 任一工具升级版本
- 测试口径变更（深度、并发、超时、是否带 `-s`）
- `assetcrawler` 的提取规则发生有意变更（此时应先看 `diff` 明细，确认是改好了还是改坏了）

目标站点内容不稳定这一点需要特别注意：该站首页会随机展示论坛帖子，
`showthread.asp?id=N` 的 N 范围可能变化，导致 URL 数量小幅波动。
判断回退时应关注「原有 URL 是否消失」，而不是总数是否相等。
