#!/usr/bin/env python3
"""多爬虫结果统一对比。

支持解析：
  - katana 的 jsonl（--jsonl 输出）
  - crawlergo 的 json（--output-json/--output-mode json）
  - assetcrawler 的 jsonl
  - tanscan 的 /api/v1/scan/task/urls 响应

用法：
    python compare_tools.py --target http://127.0.0.1:18097 \
        katana_headless=katana_full.jsonl \
        katana_static=katana_static.jsonl \
        crawlergo=cg.json \
        tanscan=ts_urls.json \
        assetcrawler=ac.jsonl
"""
import argparse
import json
import urllib.parse


def norm(url, target):
    """归一化到「相对 target 的路径 + query」，便于跨工具比对。"""
    if not url or url.startswith("data:"):
        return None
    u = urllib.parse.urlsplit(url)
    if not u.netloc:
        return None
    if urllib.parse.unquote(u.path) == "/favicon.ico":
        return None
    base = urllib.parse.urlsplit(target)
    if u.netloc != base.netloc:
        return None
    path = u.path or "/"
    return path + (("?" + u.query) if u.query else "")


def strip_query(p):
    return p.split("?")[0]


def load_katana(path):
    out = []
    for line in open(path, encoding="utf-8"):
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            rec = json.loads(line)
        except Exception:
            continue
        req = rec.get("request") or {}
        out.append({"url": req.get("endpoint"), "method": req.get("method")})
    return out


def load_crawlergo(path):
    d = json.load(open(path, encoding="utf-8"))
    return [{"url": r.get("url"), "method": r.get("method")}
            for r in (d.get("req_list") or [])]


def load_assetcrawler(path):
    out = []
    for line in open(path, encoding="utf-8"):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        out.append({"url": r.get("url"), "method": r.get("method")})
    return out


def load_tanscan(path):
    d = json.load(open(path, encoding="utf-8"))
    items = ((d.get("data") or {}).get("items")) or []
    return [{"url": it.get("url"), "method": it.get("method") or "GET"} for it in items]


LOADERS = {
    "katana": load_katana,
    "crawlergo": load_crawlergo,
    "assetcrawler": load_assetcrawler,
    "tanscan": load_tanscan,
}


def detect(path):
    if path.endswith(".jsonl"):
        with open(path, encoding="utf-8") as f:
            head = f.readline()
        return "crawlergo" if '"req_list"' in head else (
            "katana" if '"request"' in head else "assetcrawler")
    return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--target", required=True)
    ap.add_argument("pairs", nargs="+", help="name=path")
    args = ap.parse_args()

    sets = {}
    meta = {}
    for pair in args.pairs:
        name, _, path = pair.partition("=")
        kind = None
        for k in LOADERS:
            if k in name:
                kind = k
                break
        if kind is None:
            kind = detect(path) or "assetcrawler"
        rows = LOADERS[kind](path)
        paths = {}
        for r in rows:
            p = norm(r.get("url"), args.target)
            if not p:
                continue
            paths.setdefault(strip_query(p), r.get("method") or "GET")
        sets[name] = set(paths)
        meta[name] = {"rows": len(rows), "uniq": len(sets[name]), "kind": kind}

    names = list(sets)
    allu = set().union(*sets.values()) if sets else set()

    print(f"目标: {args.target}")
    print(f"{'工具':<20}{'原始条数':>8}{'唯一路径':>9}{'独有':>7}{'覆盖全站':>10}")
    print("-" * 58)
    for n in names:
        uniq = sets[n] - allu
        print(f"{n:<20}{meta[n]['rows']:>8}{meta[n]['uniq']:>9}{len(uniq):>7}"
              f"{meta[n]['uniq'] * 100 // max(len(allu), 1):>9}%")
    print(f"{'合并去重':<20}{'':>8}{len(allu):>9}")

    print("\n各工具独有（只有它拿到）：")
    for n in names:
        only = sorted(sets[n] - set().union(*[sets[m] for m in names if m != n]))
        print(f"  {n}: {len(only)}")
        for u in only:
            print(f"      {u}")

    print("\n覆盖率矩阵（✓=抓到）:")
    hdr = "  ".join(f"{n[:9]:>9}" for n in names)
    print(f"{'路径':<34}{hdr}")
    for u in sorted(allu):
        cells = "  ".join(f"{'✓' if u in sets[n] else '·':>9}" for n in names)
        print(f"{u:<34}{cells}")


if __name__ == "__main__":
    main()
