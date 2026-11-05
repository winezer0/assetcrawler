#!/usr/bin/env python3
"""动态渲染靶站：用来区分「静态解析」与「浏览器执行」两类爬虫。

设计意图：把不同类型的 URL 载体分开放，让每个工具的结果能对应到具体能力。

  静态可见（纯 HTML 标签）：
      /static/link-a          <a href>
      /static/img.png         <img src>
      /form/submit            <form action>
  只在脚本文件里（需要解析 JS 文本）：
      /api/in-js-file         app.js 里的字符串字面量
      /api/concat             app.js 里拼接出来的（需要求值）
  只在运行时产生（必须执行 JS + 有 DOM）：
      /dyn/injected-a         JS 创建 <a> 插入 DOM
      /dyn/iframe             JS 创建 iframe
      /dyn/timer              setTimeout 后跳转
  XHR / fetch（需要抓取网络请求）：
      /api/dyn/fetch          fetch()
      /api/dyn/xhr            XMLHttpRequest
  需要交互（需要自动填表 / 点击）：
      /dyn/form-submit        表单提交后的落地页

用法：
    python testdata/dynamic_fixture.py [port]
    katana -u http://127.0.0.1:18097/ -headless ...
"""
import http.server
import socketserver
import sys

INDEX = """<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>dyn</title></head>
<body>
<a href="/static/link-a">static link</a>
<img src="/static/img.png">
<form action="/form/submit" method="get">
  <input name="q" value="x">
  <button type="submit">go</button>
</form>
<script src="/static/js/app.js"></script>
<script>
document.addEventListener('DOMContentLoaded', function () {
  // 1) 动态插入链接：静态解析看不到
  var a = document.createElement('a');
  a.href = '/dyn/injected-a';
  a.textContent = 'injected';
  document.body.appendChild(a);

  // 2) 动态创建 iframe
  var f = document.createElement('iframe');
  f.src = '/dyn/iframe';
  document.body.appendChild(f);

  // 3) fetch
  fetch('/api/dyn/fetch').then(function (r) { return r.json(); }).then(function (j) {
    var a2 = document.createElement('a');
    a2.href = j.next;              // 响应里带出的下一跳
    a2.textContent = 'from-fetch';
    document.body.appendChild(a2);
  });

  // 4) XMLHttpRequest
  var x = new XMLHttpRequest();
  x.open('GET', '/api/dyn/xhr');
  x.send();

  // 5) 延时跳转的页面
  setTimeout(function () { new Image().src = '/dyn/timer'; }, 200);
});
</script>
</body></html>"""

APP_JS = """
// 只在脚本文件里的地址：必须解析 JS 文本才能拿到
var API = '/api';
window.__endpoints = {
  plain: '/api/in-js-file',
  built: API + '/concat'
};
"""

PAGE = "<html><body><h1>%s</h1><a href='/static/link-a'>back</a></body></html>"

ROUTES = {
    "/": (INDEX, "text/html"),
    "/static/js/app.js": (APP_JS, "application/javascript"),
    "/api/dyn/fetch": ('{"next":"/dyn/from-fetch"}', "application/json"),
    "/api/dyn/xhr": ('{"ok":true}', "application/json"),
}

# 其余路径一律返回一个正常页面（200），便于区分「抓到且有效」与「404」
FALLBACK_STATUS = 200


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        path = self.path.split("?")[0]
        entry = ROUTES.get(path)
        if entry:
            body, ctype = entry
        elif path.endswith((".png", ".jpg", ".gif")):
            body, ctype = "", "image/png"
        else:
            body, ctype = PAGE % path, "text/html"
        data = body.encode()
        self.send_response(FALLBACK_STATUS)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18097
    socketserver.TCPServer.allow_reuse_address = True
    with socketserver.TCPServer(("127.0.0.1", port), Handler) as httpd:
        print(f"动态靶站已启动: http://127.0.0.1:{port}/")
        httpd.serve_forever()
