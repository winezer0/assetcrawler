#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""探针夹具的共享常量。

server.py 与 check.py 从同一处取身份标记，避免两边各写一份导致漂移。
曾经踩过一次坑：18080 被别的本地服务占用时，check.py 只探「端口通不通」
就把那个服务当成桩服务，结果报出大面积假回退。现在改为校验固定标记。
"""
PROBE_MARKER = "assetcrawler-probe-v1"
PING_PATH = "/__probe_ping"
DEFAULT_PORT = 18080
