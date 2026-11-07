# 基线：vuejs.org-2026-11-06

采集日期：2026-11-06 21:00（**第二次更新**）
目标：`https://vuejs.org/`（Vite 构建的现代站点，入口为 ESM）
命令：

```bash
./assetcrawler.exe -u https://vuejs.org/ -d 2 -max-urls 300 -rl 5 -c 5 \
    -timeout 15 -jsonl -o raw/assetcrawler.jsonl -silent
```

代码：`0f95456` 之后（A/B/C/D 四批 + CSP 线索接入）

## 结果

| 指标 | 值 |
|---|---|
| 抓取总数 | 298（站内 297 + 外链 1） |
| stable 归一条数 | 297 |
| 外链 | 1：`a.nel.cloudflare.com/report/...`（CSP report-to） |

## 本次更新的原因

提取器在这一轮改了五处（作用域化、og/twitter、`data-*`、sourcemap、
CSP 线索），旧基线已经不能用来判断回归，因此按当前代码重采。

**同口径对比结论：stable 零差异**。用同一套 `stable` 规则从旧 raw 重新生成，
与本次结果逐行相同（297 / 297），说明这一轮的改动对本站**没有丢任何 URL**。

唯一的新增是外链 `a.nel.cloudflare.com/report/...`，来自 CSP 的 `report-to`。
它不属站内，所以不计入 `urls-assetcrawler.txt` 与 `urls-stable.txt`，
在 `raw/assetcrawler.jsonl` 里可以查到，`kind=csp-report`。

## 文件说明

| 文件 | 内容 |
|---|---|
| `raw/assetcrawler.jsonl` | 原始输出，保留以便复查（P2-2 的教训） |
| `urls-assetcrawler.txt` | 站内 URL（canonical：去 scheme、去尾斜杠） |
| `urls-stable.txt` | 站内 URL + hash 归一化（`app.<HASH>.js`），**回归对比用这一份** |
| `stats.json` | 计数与时间戳 |

## 对比方式

```bash
# 现代站点必须比 stable，否则一次部署的 hash 轮换会判成几十条增删
python baseline.py stable <新结果目录> -o /tmp/check --host vuejs.org
diff baseline/vuejs.org-2026-11-06/urls-stable.txt /tmp/check/urls-stable.txt
```

## 失效条件

- 站点部署（chunk 哈希全变）→ 用 `urls-stable.txt` 对比即可
- 站点结构改版 → 重建基线，换日期目录
- 采集参数变化（深度、配额、限速）→ 不可直接比较
