#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check.py —— URL 载体验收夹具的一键检查。

起本地桩服务 → 跑资产采集器 → 与 expected.json 对比 → 输出进度报告。

用法：
  python check.py
  python check.py --bin ../../assetcrawler.exe
  python check.py --verbose        # 打印每个用例的明细

退出码：全部达标为 0，否则 1（可挂 CI）。
"""
import argparse
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
TARGET = "http://127.0.0.1:18080/"
ROOT = os.path.dirname(os.path.dirname(HERE))  # 仓库根


def wait_ready(timeout=10):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            urllib.request.urlopen(TARGET, timeout=1)
            return True
        except (urllib.error.URLError, ConnectionError, OSError):
            time.sleep(0.3)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "assetcrawler.exe"),
                    help="资产采集器路径")
    ap.add_argument("--verbose", action="store_true")
    args = ap.parse_args()

    if not os.path.exists(args.bin):
        sys.exit(f"找不到采集器: {args.bin}（先 go build -o assetcrawler.exe ./cmd/assetcrawler）")

    server = subprocess.Popen([sys.executable, os.path.join(HERE, "server.py")],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    out_file = os.path.join(HERE, "_probe_result.jsonl")
    try:
        if not wait_ready():
            sys.exit("桩服务启动失败（端口 18080 可能被占用）")

        subprocess.run([args.bin, "-u", TARGET, "-d", "3", "-jsonl",
                        "-o", out_file, "-silent"],
                       check=True, capture_output=True)

        rows = [json.loads(l) for l in open(out_file, encoding="utf-8") if l.strip()]
        got = {r["url"].replace(TARGET.rstrip("/"), ""): r for r in rows}
        exp = json.load(open(os.path.join(HERE, "expected.json"), encoding="utf-8"))

        # 1) 载体覆盖
        cases = exp["cases"]
        hit = [c for c in cases if c["url"] in got]
        print(f"[载体]   {len(hit)}/{len(cases)}")
        for c in cases:
            present = c["url"] in got
            if present != c["implemented"]:
                print(f"         ! 记录与实际不符: {c['url']} 实际={present}")
            elif args.verbose or not present:
                print(f"         {'✓' if present else '✗'} {c['url']:<38} {c['via']}")

        # 2) 方法正确性
        mc = exp.get("method_cases", [])
        mok, mbad = 0, []
        for c in mc:
            r = got.get(c["url"])
            actual = (r or {}).get("method", "-")
            if actual == c["method"]:
                mok += 1
            else:
                mbad.append(f"         ✗ {c['url']:<24} 期望 {c['method']:<7} 实际 {actual}")
        print(f"[方法]   {mok}/{len(mc)}")
        for line in mbad:
            print(line)

        # 3) 参数提取：参数可能落在 URL 的 query 上，也可能落在未来新增的 params 字段
        pc = exp.get("param_cases", [])
        pok, pbad = 0, []
        for c in pc:
            r = got.get(c["url"])
            if r is None:
                pbad.append(f"         ✗ {c['url']:<24} 未抓到该 URL")
                continue
            have = set(r.get("params") or [])
            query = r["url"].split("?", 1)[1] if "?" in r["url"] else ""
            missing = [p for p in c["params"] if p not in have and f"{p}=" not in query]
            if missing:
                pbad.append(f"         ✗ {c['url']:<24} 缺参数 {missing}")
            else:
                pok += 1
        print(f"[参数]   {pok}/{len(pc)}")
        for line in pbad:
            print(line)

        # 4) 噪音（应当消失）
        noise = [n for n in exp.get("known_noise", []) if n["url"] in got]
        print(f"[噪音]   {len(noise)} 条残留" + (f"：{[n['url'] for n in noise]}" if noise else ""))

        total_ok = len(hit) == len(cases) and mok == len(mc) and pok == len(pc) and not noise
        print("\n结论:", "全部达标" if total_ok else "未达标（见上方 ✗）")
        return 0 if total_ok else 1
    finally:
        server.terminate()
        try:
            server.wait(timeout=3)
        except subprocess.TimeoutExpired:
            server.kill()
        if os.path.exists(out_file):
            os.remove(out_file)


if __name__ == "__main__":
    sys.exit(main())
