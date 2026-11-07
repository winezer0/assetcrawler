#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""baseline.py —— URL 采集结果的快照与对比工具。

两个子命令：

  snapshot  把各工具的原始输出归一化成基线（canonical URL 列表 + 统计）
  diff      用同一套归一化规则比较基线与新结果，回答「多了什么 / 少了什么」

归一化规则与 BENCHMARK.md 一致：剥 ANSI、去 fragment、percent-decode、
折叠 ../ 与重复斜杠、去尾斜杠。刻意不改 query 顺序，因为参数顺序在多数
框架里有语义（如 ?a=1&b=2 与 ?b=2&a=1 可能命中同一逻辑，但无法确定，
保守起见不合并）。

用法：
  python baseline.py snapshot out/cmp -o tools/baseline/testasp-2026-11-06
  python baseline.py diff tools/baseline/testasp-2026-11-06 out/cmp-new
"""
import argparse
import json
import os
import re
import sys
import urllib.parse

ANSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")
URL_RE = re.compile(r"https?://[^\s<>\"'\]\)]+")

# 各工具的原始输出文件 → 展示名。新增工具时在这里加一行。
SOURCES = [
    ("hakrawler", "hakrawler.txt", "text"),
    ("gospider", "gospider.txt", "text"),
    ("cariddi", "cariddi.txt", "text"),
    ("URLFinder-m1", "urlfinder_m1.txt", "text"),
    ("URLFinder-m2", "urlfinder_m2.txt", "text"),
    ("assetcrawler", "assetcrawler.jsonl", "jsonl"),
]


def strip_junk(u: str) -> str:
    # 去掉正则从 JSON/HTML 片段里带出的尾随标点
    while u and u[-1] in ",;.)]>'\"":
        u = u[:-1]
    return u.split("#")[0]


def canonical(u: str) -> str:
    p = urllib.parse.urlsplit(u)
    segs = []
    for s in urllib.parse.unquote(p.path).split("/"):
        if s in ("", "."):
            continue
        if s == "..":
            if segs:
                segs.pop()
        else:
            segs.append(s)
    path = "/" + "/".join(segs)
    q = urllib.parse.unquote(p.query)
    return (p.hostname or "").lower() + path + ("?" + q if q else "")


def host_of(u: str) -> str:
    return (urllib.parse.urlsplit(u).hostname or "").lower()


def load(path: str, kind: str):
    """返回 None 表示文件不存在（工具未运行成功）。"""
    if not os.path.exists(path):
        return None
    out = []
    with open(path, "r", encoding="utf-8", errors="ignore") as fh:
        for line in fh:
            line = ANSI.sub("", line).strip()
            if not line:
                continue
            if kind == "jsonl":
                try:
                    obj = json.loads(line)
                    if isinstance(obj, dict) and "url" in obj:
                        out.append(strip_junk(obj["url"]))
                        continue
                except json.JSONDecodeError:
                    pass
            out += [strip_junk(m) for m in URL_RE.findall(line)]
    return out


def collect(raw_dir: str, target_host: str):
    """读取一个目录下所有工具的输出，返回 {工具名: {inside:set, outside:set, raw_count:int}}。"""
    result = {}
    for name, fname, kind in SOURCES:
        urls = load(os.path.join(raw_dir, fname), kind)
        if urls is None:
            result[name] = None
            continue
        inside, outside = set(), set()
        for u in urls:
            h = host_of(u)
            if h.endswith(target_host):
                inside.add(canonical(u))
            elif h:
                outside.add(canonical(u))
        result[name] = {
            "inside": inside,
            "outside": outside,
            "raw_count": len(urls),
        }
    return result


def cmd_snapshot(args):
    data = collect(args.raw_dir, args.host)
    os.makedirs(args.out, exist_ok=True)

    stats = {}
    union = set()
    for name, d in data.items():
        if d is None:
            stats[name] = {"status": "no_output"}
            continue
        union |= d["inside"]
        stats[name] = {
            "status": "ok",
            "inside_unique": len(d["inside"]),
            "outside_unique": len(d["outside"]),
            "raw_count": d["raw_count"],
        }

    with open(os.path.join(args.out, "stats.json"), "w", encoding="utf-8") as fh:
        json.dump(
            {
                "target_host": args.host,
                "generated_at": args.generated_at,
                "tools": stats,
                "union_inside": len(union),
            },
            fh,
            ensure_ascii=False,
            indent=2,
        )

    # 所有工具合并后的站内 URL（最重要的对照基线）
    with open(os.path.join(args.out, "urls-all.txt"), "w", encoding="utf-8") as fh:
        for u in sorted(union):
            fh.write(u + "\n")

    # 逐工具落盘，便于单独 diff
    for name, d in data.items():
        if d is None:
            continue
        with open(os.path.join(args.out, f"urls-{name}.txt"), "w", encoding="utf-8") as fh:
            for u in sorted(d["inside"]):
                fh.write(u + "\n")

    print(f"基线已生成: {args.out}")
    print(f"{'工具':<16}{'站内':>8}{'外链':>8}{'原始':>8}")
    for name, s in stats.items():
        if s["status"] != "ok":
            print(f"{name:<16}{'无输出':>24}")
            continue
        print(
            f"{name:<16}{s['inside_unique']:>8}{s['outside_unique']:>8}{s['raw_count']:>8}"
        )
    print(f"{'合并去重':<16}{len(union):>8}")


def read_set(path):
    if not os.path.exists(path):
        return set()
    with open(path, "r", encoding="utf-8") as fh:
        return {ln.strip() for ln in fh if ln.strip()}


# 构建产物文件名里的内容哈希：app.B91ytTIH.js、index.8fdf0d4d.js、main.ABC_123-.css
_HASH_RE = re.compile(r"\.[A-Za-z0-9_-]{6,}\.(js|mjs|css|woff2?)$")


def stable_of(u: str) -> str:
    """把文件名里的 hash 段替换成 <HASH>，其余原样保留。"""
    return _HASH_RE.sub(r".<HASH>.\1", u)


def cmd_stable(args):
    """从原始输出生成 hash 归一化基线。

    规则只动扩展名前的最后一段，且要求长度 >= 6：
    Vue 的 `app.B91ytTIH.js` 会归一，而 `app.js`、`vue.global.js` 不会被动。
    """
    raw = os.path.join(args.raw_dir, "assetcrawler.jsonl")
    urls = load(raw, "jsonl")
    if urls is None:
        sys.exit(f"找不到 {raw}")

    # 只保留站内 URL：与 urls-assetcrawler.txt 同口径，
    # 否则 CSP 带进来的外部域名会让 stable 基线漂移。
    out = sorted({stable_of(u) for u in urls if host_of(u).endswith(args.host)})
    path = os.path.join(args.out, "urls-stable.txt")
    with open(path, "w", encoding="utf-8") as fh:
        for u in out:
            fh.write(u + "\n")
    print(f"urls-stable.txt: {len(out)} 条 -> {path}")


def cmd_diff(args):
    base_stats = os.path.join(args.baseline, "stats.json")
    if not os.path.exists(base_stats):
        sys.exit(f"基线不存在: {base_stats}")

    with open(base_stats, encoding="utf-8") as fh:
        base = json.load(fh)
    host = base["target_host"]
    cur = collect(args.raw_dir, host)

    print(f"目标: {host}")
    print(f"{'工具':<16}{'基线':>8}{'当前':>8}{'新增':>8}{'消失':>8}")
    print("-" * 50)
    regressions = 0
    for name, d in cur.items():
        old = read_set(os.path.join(args.baseline, f"urls-{name}.txt"))
        if d is None:
            print(f"{name:<16}{len(old):>8}{'无输出':>8}")
            continue
        new = d["inside"]
        added, lost = new - old, old - new
        flag = "  <-- 回退" if lost else ""
        if lost:
            regressions += 1
        print(
            f"{name:<16}{len(old):>8}{len(new):>8}{len(added):>8}{len(lost):>8}{flag}"
        )
        for u in sorted(added):
            print(f"    + {u}")
        for u in sorted(lost):
            print(f"    - {u}")

    print()
    if regressions:
        print(f"结论: {regressions} 个工具出现了相对基线的 URL 丢失，需排查。")
        sys.exit(1)
    print("结论: 无回退（没有相对基线丢失的 URL）。")


def main():
    ap = argparse.ArgumentParser(description="URL 采集结果快照与对比")
    sub = ap.add_subparsers(dest="cmd", required=True)

    sp = sub.add_parser("snapshot", help="生成基线")
    sp.add_argument("raw_dir", help="存放各工具原始输出的目录")
    sp.add_argument("-o", "--out", required=True, help="基线输出目录")
    sp.add_argument("--host", default="testasp.vulnweb.com", help="目标域名")
    sp.add_argument("--generated-at", default="", help="生成时间（写入 stats.json）")
    sp.set_defaults(func=cmd_snapshot)

    tp = sub.add_parser("stable", help="生成 hash 归一化基线")
    tp.add_argument("raw_dir", help="存放各工具原始输出的目录")
    tp.add_argument("-o", "--out", required=True, help="基线目录（已存在）")
    tp.add_argument("--host", default="testasp.vulnweb.com", help="目标域名")
    tp.set_defaults(func=cmd_stable)

    dp = sub.add_parser("diff", help="与基线对比")
    dp.add_argument("baseline", help="基线目录")
    dp.add_argument("raw_dir", help="新结果所在目录")
    dp.set_defaults(func=cmd_diff)

    args = ap.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
