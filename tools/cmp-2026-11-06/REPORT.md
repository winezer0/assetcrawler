# 爬虫对比：katana / crawlergo / assetcrawler

日期：2026-11-06
环境：Windows / amd64，Chrome `C:/Program Files/Google/Chrome/Application/chrome.exe`
二进制定位：

```
katana      C:\ISEC\Deving\tanscan\third\katana\katana.exe
crawlergo   C:\ISEC\Deving\tanscan\third\crawlergo\crawlergo_win_amd64.exe
assetcrawler C:\Users\windows\WorkBuddy\...\tools\bin\assetcrawler.exe
```

统一口径：深度 3，作用域限制在目标主机（`-fs fqdn`），全功能开启。
结果统一按「路径」去重（去 query、去尾斜杠），并单独统计 HTTP 200 的数量。

---

## 一、真实站点：http://testasp.vulnweb.com

Acunetix 公开 ASP 靶站，传统服务端渲染，页面里有 tinyMCE 编辑器（大量 JS 配置）。

| 工具 | 关键参数 | 耗时 | 原始条数 | 唯一路径 | **其中 200** |
|---|---|---:|---:|---:|---:|
| **katana（静态全功能）** | `-d3 -jc -jsl -kf all -fx -aff` | 16s | 463 | 25 | **13** |
| **katana（headless 全功能）** | 上述 + `-hl -xhr-extraction -scp` | **282s 超时** | 52 | 10 | **4** |
| **crawlergo** | `--max-crawled-count 300 --max-tab-count 8` | 26s | 16 | 12 | **6**（+2 个 302） |
| **assetcrawler** | `-d3 -js-engine goja` | **4s** | 43 | 14 | **12** |

### 结果解读

**1. headless 在真实站点是负收益，甚至跑不完。**

282 秒被 `timeout` 杀掉，只产出 4 个有效页面。katana 的 headless 模式要等 Chrome
把每个页面加载完，再加上 `-aff` 自动填表会触发真实表单提交，在响应慢的远程站上
会长时间挂住。同一台机器、同一个站点，**去掉 `-hl` 后 16 秒就跑完了**。

**2. katana 静态全功能的 25 条里，12 条是无效的。**

```
/jscripts/tiny_mce/'+tinyMCE.settings['base_href']+'    ← JS 表达式碎片，3 条这类
/css/editor_content.css  /blank.htm                     ← 404（tinyMCE 自带资源，本站没有）
/langs/  /plugins/  /themes/                            ← 目录本身，不是文件
```

也就是说 463 条原始记录 → 25 个唯一路径 → 只有 13 个真的返回 200。
数字最大，但含 12 条噪音。

**3. assetcrawler 的 14 条几乎全是有效页面。**

12 个 200，另 2 条是 `/Templates/Login.asp?RetURL=...` 的 404——
这两个路径是站点上真实存在的链接（模板页里），本站确实没有对应文件，不是误报。

**4. crawlergo 最浅。**

只做了 46 次请求、26 秒结束。它靠浏览器点击链接和表单推进，没有深度参数，
所以拿到 12 条就停了，其中 3 条 404、1 条 500、2 条 302。

### 独有项（只有单个工具拿到的）

| 工具 | 独有路径 | 性质 |
|---|---|---|
| katana 静态 | `/html/about.html`、`/jscripts/tiny_mce/tiny_mce.js`、`/blank.htm`、`/css/editor_*.css`、`/langs/`、`/plugins/`、`/themes/` | tinyMCE 资源树；部分 404 |
| katana 静态 | `/jscripts/tiny_mce/'+tinyMCE...` × 2 | **JS 表达式碎片，误报** |
| crawlergo | `/application/x-www-form-urlencoded` | **MIME 字符串，误报** |
| assetcrawler | `/Images/logo.gif`、`/avatars/noavatar.gif` | 真实图片，另两个工具都过滤了图片 |

katana 能拿到 tinyMCE 那一片，是因为它 `-jc` 解析了 `/jscripts/tiny_mce/tiny_mce.js`
这个脚本；assetcrawler 全程没有发现任何 `<script src>`（来源统计里 `script` 为 0），
**差异在抓取广度，不在 JS 解析能力**。

---

## 二、JS 重站点：本地动态靶站

`assetcrawler/testdata/dynamic_fixture.py`。静态 HTML 只有 1 个链接，
其余 URL 分别在：内联脚本字符串、外部脚本文件、fetch/XHR、运行时 DOM 注入、
fetch 响应体、表单提交。

| 工具 | 耗时 | 唯一路径 | 有效 | 说明 |
|---|---:|---:|---:|---|
| katana（headless 全功能） | 27s | 10 | 10 | 运行时注入的 4 条全拿到 |
| katana（静态全功能） | 14s | 10 | 10 | 少 `/dyn/from-fetch`（只在响应体里） |
| crawlergo | 16s | 12 | 10 | 多 2 条碎片 `/api`、`/concat` |
| assetcrawler | **1s** | 8 | 8 | 拿不到运行时注入的 4 条 |

覆盖率矩阵（关键差异部分）：

| 路径 | katana 静态 | katana headless | crawlergo | assetcrawler |
|---|:-:|:-:|:-:|:-:|
| `/api/in-js-file`（外部 JS 里的字面量） | ✓ | · | ✓ | **·** |
| `/api/concat`（外部 JS 里拼接） | · | · | · | **✓** |
| `/dyn/injected-a`（JS 创建 a 标签） | ✓ | ✓ | ✓ | **·** |
| `/dyn/iframe`（JS 创建 iframe） | ✓ | ✓ | ✓ | **·** |
| `/dyn/timer`（setTimeout 设图） | ✓ | ✓ | ✓ | **·** |
| `/dyn/from-fetch`（**响应体里返回的**） | · | **✓** | ✓ | · |
| `/api/dyn/fetch` / `/api/dyn/xhr` | ✓ | ✓ | ✓ | ✓ |
| `/form/submit?q=x`（自动填表） | ✓ | ✓ | ✓ | 只给 action，无参数 |
| `/api`、`/concat`（碎片） | · | · | **2 条** | · |

### 关键结论

**只有 `/dyn/from-fetch` 是真正「必须执行 JS」才能拿到的** —— 它出现在 fetch
响应体 `{"next":"/dyn/from-fetch"}` 里。其余 `/dyn/*` 虽然在运行时才进 DOM，
但在内联脚本里是**字符串字面量**，所以 `-jc`（解析 JS 文本）也能拿到。

这说明：**大部分「动态」URL 其实静态可见，浏览器不是必需的**；
真正需要浏览器的是「响应体驱动」和「复杂交互后」的地址。

---

## 三、两个必须知道的坑

**1. katana 这个构建的 `-u` / `-list` 不生效，必须走 stdin。**

```bash
echo "http://target/" | katana -d 3 ...        # 正常
katana -u http://target/ -d 3 ...              # 静默退出，0 结果
```

**2. `-scp` 必须用 Windows 路径。**

```
-scp /c/Program Files/.../chrome.exe           # ERR: specified system chrome binary does not exist
-scp "C:/Program Files/.../chrome.exe"         # 正常
```

---

## 四、结论

| 场景 | 推荐 | 理由 |
|---|---|---|
| 传统站点批量摸 URL | **katana 静态全功能** | 召回最高（13 个有效页），16 秒；但要接受 ~50% 噪音 |
| 要干净的 URL 清单 | **assetcrawler** | 12/14 有效、4 秒、无碎片 |
| SPA / 响应体驱动接口 | **katana headless** 或 **crawlergo** | 静态方案拿不到响应体里的地址 |
| 快速看站点结构 | **crawlergo** | 26 秒，浏览器真实交互，但很浅 |

**别默认开 headless。** 在传统站点上它把 16 秒的任务拖成 282 秒且没跑完，
这是本次对比最反直觉的一条。

### assetcrawler 暴露的两个真实缺口

1. **赋值 / 对象属性里的字符串字面量不收集**。
   `a.href = '/dyn/injected-a'`、`{plain: '/api/in-js-file'}` 都拿不到，
   因为 AST 收集只覆盖「调用参数」和「二元表达式」。
   这是本次 JS 靶站少 4 条的直接原因。
2. **响应体不解析**。`/dyn/from-fetch` 这类只在 JSON 返回里的地址拿不到
   （PLAN 里的 P0-3 未做）。

---

## 复现

原始输出在 `raw/`：

```
raw/ks_static_real.jsonl    katana 静态全功能（真实站，463 条）
raw/ks_real.jsonl           katana headless（真实站，超时前产物）
raw/cg_real.json            crawlergo（真实站）
raw/ac_real.jsonl           assetcrawler（真实站）
raw/katana_full.jsonl       katana headless（本地靶站）
raw/katana_static.jsonl     katana 静态全功能（本地靶站）
raw/cg.json                 crawlergo（本地靶站）
raw/ac.jsonl                assetcrawler（本地靶站）
```

一键比对：

```bash
python tools/compare_tools.py --target http://testasp.vulnweb.com \
    katana_static=raw/ks_static_real.jsonl \
    katana_headless=raw/ks_real.jsonl \
    crawlergo=raw/cg_real.json \
    assetcrawler=raw/ac_real.jsonl
```
