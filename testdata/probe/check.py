#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""check.py —— URL 载体验收夹具的一键检查。

起本地桩服务 → 校验服务身份 → 跑资产采集器 → 与 expected.json 对比 → 输出进度报告。

用法：
  python check.py
  python check.py --bin ../../assetcrawler.exe
  python check.py --port 18081       # 指定端口
  python check.py --verbose          # 打印每个用例的明细

端口策略：默认端口被占用时自动换一个空闲端口；显式 --port 被别的程序占用时
由身份校验直接报错，绝不「连上谁就测谁」。

退出码：无回归为 0，否则 1（可挂 CI）。
"""
import argparse
import json
import os
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request

from common import DEFAULT_PORT, PING_PATH, PROBE_MARKER

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))  # 仓库根


class ProbeIdentityError(Exception):
    """端口上跑的不是本夹具的桩服务。"""


def target_url(port):
    return f"http://127.0.0.1:{port}/"


def port_in_use(port):
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.settimeout(0.3)
        return s.connect_ex(("127.0.0.1", port)) == 0


def pick_port(preferred=0):
    """显式端口照用；否则默认端口空闲就用它，被占用就换一个空闲端口。"""
    if preferred:
        return preferred
    if not port_in_use(DEFAULT_PORT):
        return DEFAULT_PORT
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def wait_ready(base, timeout=10):
    """等服务就绪，并确认它确实是我们的桩服务。

    只探「端口通不通」是不够的：任何本地服务都会响应根路径，那样会静默
    测错目标、报出大面积假回退。这里改为请求 PING_PATH 校验固定标记。
    """
    url = base + PING_PATH.lstrip("/")
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1) as resp:
                body = resp.read().decode("utf-8", "replace").strip()
                if body != PROBE_MARKER:
                    raise ProbeIdentityError(f"{url} 返回了未知标记 {body!r}")
                return True
        except urllib.error.HTTPError as e:
            raise ProbeIdentityError(f"{url} 返回 HTTP {e.code}，不是本夹具的桩服务") from e
        except (urllib.error.URLError, ConnectionError, OSError):
            time.sleep(0.3)
    return False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--bin", default=os.path.join(ROOT, "assetcrawler.exe"),
                    help="资产采集器路径")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--port", type=int, default=0,
                    help="桩服务端口（默认自动选空闲端口）")
    ap.add_argument("--js-engine", default=None,
                    help="传给采集器的 -js-engine（regex | goja | treesitter）")
    args = ap.parse_args()

    if not os.path.exists(args.bin):
        sys.exit(f"找不到采集器: {args.bin}（先 go build -o assetcrawler.exe ./cmd/assetcrawler）")

    port = pick_port(args.port)
    base = target_url(port)
    server = subprocess.Popen([sys.executable, os.path.join(HERE, "server.py"), str(port)],
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    out_file = os.path.join(HERE, "_probe_result.jsonl")
    try:
        try:
            if not wait_ready(base):
                sys.exit(f"桩服务启动失败（端口 {port} 在 10s 内未就绪）")
        except ProbeIdentityError as e:
            sys.exit(f"桩服务身份校验失败：{e}；请检查端口 {port} 是否被其他服务占用")

        cmd = [args.bin, "-u", base, "-d", "3", "-jsonl", "-o", out_file, "-silent"]
        if args.js_engine:
            cmd += ["-js-engine", args.js_engine]
        subprocess.run(cmd, check=True, capture_output=True)

        rows = [json.loads(l) for l in open(out_file, encoding="utf-8") if l.strip()]
        got = {r["url"].replace(base.rstrip("/"), ""): r for r in rows}

        def variants(path):
            """路径的等价值：本身、以及带 query 的所有变体。"""
            return [v for v in got if v == path or v.startswith(path + "?")]

        def first_of(path):
            for v in variants(path):
                return got[v]
            return None
        exp = json.load(open(os.path.join(HERE, "expected.json"), encoding="utf-8"))

        # 1) 载体覆盖
        cases = exp["cases"]
        hit = [c for c in cases if variants(c["url"])]
        print(f"[载体]   {len(hit)}/{len(cases)}")
        for c in cases:
            present = bool(variants(c["url"]))
            if present != c["implemented"]:
                print(f"         ! 记录与实际不符: {c['url']} 实际={present}")
            elif args.verbose or not present:
                print(f"         {'✓' if present else '✗'} {c['url']:<38} {c['via']}")

        # 2) 方法正确性
        mc = exp.get("method_cases", [])
        mok, mbad = 0, []
        for c in mc:
            r = first_of(c["url"])
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
            # 参数可能落在任一 URL 变体上，取覆盖最全的那个
            best_missing = c["params"]
            found_any = False
            for v in variants(c["url"]):
                found_any = True
                r = got[v]
                have = set(r.get("params") or [])
                query = v.split("?", 1)[1] if "?" in v else ""
                miss = [p for p in c["params"] if p not in have and f"{p}=" not in query]
                if len(miss) < len(best_missing):
                    best_missing = miss
            if not found_any:
                pbad.append(f"         ✗ {c['url']:<24} 未抓到该 URL")
                continue
            missing = best_missing
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

        # 退出码的判定口径是「不回归」，不是「满覆盖」：
        # 将来若出现「按设计不接入」的载体，门槛不该跟着变红。
        regressions = [c for c in cases if c["implemented"] and not variants(c["url"])]
        stale = [c for c in cases if not c["implemented"] and variants(c["url"])]
        if stale:
            print(f"         ! 记录落后于实际：{len(stale)} 项已覆盖但 recorded=false，"
                  f"请把 expected.json 的 implemented 改成 true")

        total_ok = not regressions and mok == len(mc) and pok == len(pc) and not noise
        if total_ok and len(hit) == len(cases):
            print("\n结论: 全部达标")
        elif total_ok:
            print(f"\n结论: 无回归（覆盖 {len(hit)}/{len(cases)}，未实现项按设计保留）")
        else:
            print("\n结论: 未达标（见上方 ✗）")
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
