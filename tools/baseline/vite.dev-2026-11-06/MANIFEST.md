# 基线：vite.dev-2026-11-06

采集日期：2026-11-06 21:02（**第二次更新**）
目标：`https://vite.dev/`（Vite 构建的现代站点，入口为 ESM）
命令：

```bash
./assetcrawler.exe -u https://vite.dev/ -d 2 -max-urls 300 -rl 5 -c 5 \
    -timeout 15 -jsonl -o raw/assetcrawler.jsonl -silent
```

代码：`0f95456` 之后（A/B/C/D 四批 + CSP 线索接入）

## 结果

| 指标 | 值 |
|---|---|
| 抓取总数 | 297（站内 293 + 外链 4） |
| stable 归一条数 | 293 |
| 外链 | 含 `a.nel.cloudflare.com/report/...`（CSP report-to） |

## 本次更新的原因

同 vuejs.org：提取器改了五处，旧基线不能用来判断回归。

**同口径对比结论：stable 零差异**（293 / 293）。

采集过程中观察到一次站点侧版本切换：同一时段用旧二进制抓到的是
`v4.vite.dev/*` 整套（13 条），新二进制抓到 `v5.vite.dev/*` 整套（14 条，
多一条 `blog.rss`）。两边结构完全对称，说明是站点自己换了导航目标，
不是采集回退。这类漂移无法用基线拦住，只能靠「差集是否成对」判断。

## 文件说明

| 文件 | 内容 |
|---|---|
| `raw/assetcrawler.jsonl` | 原始输出 |
| `urls-assetcrawler.txt` | 站内 URL（canonical） |
| `urls-stable.txt` | 站内 + hash 归一化，**回归对比用这一份** |
| `stats.json` | 计数与时间戳 |

## 对比方式

```bash
python baseline.py stable <新结果目录> -o /tmp/check --host vite.dev
diff baseline/vite.dev-2026-11-06/urls-stable.txt /tmp/check/urls-stable.txt
```

## 已知未归一化的形态

`*.md.<hash>.lean.js`（双扩展名）不在 `stable` 规则覆盖范围内，
文件名里的 hash 会原样保留。这类文件每次部署都会变，对比时会出现在增删两侧，
按成对出现即可判定为部署漂移。

## 失效条件

- 站点部署（chunk 哈希全变）→ 用 `urls-stable.txt`
- 站点结构改版（如 v4 → v5 子站切换）→ 差集会成对出现，需人工确认
- 采集参数变化 → 不可直接比较
