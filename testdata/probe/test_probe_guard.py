#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""探针夹具自身的回归测试：身份校验与端口自适应。

这些用例守住的是「测量工具本身可信」——夹具测错目标时，所有结论都不可信。
CI 里用 PROBE_BIN 指定已构建的采集器二进制，没有则跳过 CLI 级用例。
"""
import http.server
import os
import socket
import socketserver
import subprocess
import sys
import threading
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import check  # noqa: E402
import common  # noqa: E402

PROBE_BIN = os.environ.get("PROBE_BIN") or os.path.join(check.ROOT, "assetcrawler.exe")


def free_port():
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class ForeignHandler(http.server.BaseHTTPRequestHandler):
    """模拟「恰好占用了端口的其它本地服务」。"""

    def log_message(self, *a):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.end_headers()
        self.wfile.write(b"some other service")


class ProbeGuardTest(unittest.TestCase):

    @staticmethod
    def serve(handler, port):
        httpd = socketserver.TCPServer(("127.0.0.1", port), handler)
        thread = threading.Thread(target=httpd.serve_forever, daemon=True)
        thread.start()
        return httpd, thread

    @staticmethod
    def stop(httpd, thread):
        httpd.shutdown()
        thread.join(timeout=3)
        httpd.server_close()

    def test_wait_ready_rejects_foreign_service(self):
        port = free_port()
        httpd, thread = self.serve(ForeignHandler, port)
        try:
            with self.assertRaises(check.ProbeIdentityError):
                check.wait_ready(check.target_url(port), timeout=2)
        finally:
            self.stop(httpd, thread)

    def test_wait_ready_accepts_probe_server(self):
        port = free_port()
        proc = subprocess.Popen([sys.executable, os.path.join(HERE, "server.py"), str(port)],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        try:
            self.assertTrue(check.wait_ready(check.target_url(port), timeout=10))
        finally:
            proc.terminate()
            proc.wait(timeout=3)

    def test_pick_port_skips_occupied_default(self):
        port = free_port()
        httpd, thread = self.serve(ForeignHandler, port)
        try:
            with mock.patch.object(check, "DEFAULT_PORT", port):
                chosen = check.pick_port()
            self.assertNotEqual(chosen, port)
            self.assertFalse(check.port_in_use(chosen))
        finally:
            self.stop(httpd, thread)

    @unittest.skipUnless(os.path.exists(PROBE_BIN), "需要先构建采集器二进制")
    def test_cli_reports_foreign_service(self):
        """显式端口被冒用时，CLI 必须报身份校验失败，而不是静默测错目标。"""
        port = free_port()
        httpd, thread = self.serve(ForeignHandler, port)
        try:
            result = subprocess.run(
                [sys.executable, os.path.join(HERE, "check.py"),
                 "--bin", PROBE_BIN, "--port", str(port)],
                capture_output=True, text=True, timeout=60)
            output = result.stdout + result.stderr
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("身份校验失败", output)
            self.assertNotIn("[载体]", output)
        finally:
            self.stop(httpd, thread)


if __name__ == "__main__":
    unittest.main(verbosity=2)
