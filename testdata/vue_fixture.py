#!/usr/bin/env python3
"""Vue 3 + webpack 5 产物夹具。

模拟 `vue-cli-service build` 之后的真实形态，用来验证 AST 引擎在
现代前端产物上的提取能力。特征刻意按真实产物还原：

  - 变量被 terser 压成单字母（axios -> n，publicPath -> o.p）
  - 接口地址大量使用模板字符串（`` `${c}/orders/${id}` ``）
  - 派生常量代替重复字面量（`` var u = c + "/orders" ``）
  - webpack 的 chunk 加载器与 hash 映射表
  - sourceMappingURL 注释
  - CSS 里 @font-face 与 url()

用法：
    python testdata/vue_fixture.py            # 监听 127.0.0.1:18096
    assetcrawler -u http://127.0.0.1:18096/ -d 3 -jsonl -js-engine goja

期望结果（goja / treesitter 两个引擎应完全一致）：

    真实地址（会被抓取，200）：
        /  /static/css/app.7f8e9d.css  /static/js/app.4a5b6c.js
        /api/v3/products          GET
        /api/v3/orders            POST   <- 派生常量 u + 压缩后方法名
        /api/v3/ping              POST   <- n({url,method,data}) 配置对象

    模板（只输出、不抓取，confidence=template）：
        /api/v3/orders/EXPR              PUT
        /api/v3/search?q=EXPR&page=EXPR  GET

已知未覆盖（需要枚举 chunk hash 映射表才能拿到）：
        /static/js/chunk-101.a1b2c3.js
"""
import http.server
import json
import socketserver

INDEX = """<!DOCTYPE html><html><head><meta charset="utf-8">
<link rel="stylesheet" href="/static/css/app.7f8e9d.css">
</head><body><div id="app"></div>
<script src="/static/js/app.4a5b6c.js"></script></body></html>"""

APP_JS = r"""
!function(){"use strict";var e={},
r={};function o(n){var t=r[n];if(void 0!==t)return t.exports;var a=r[n]={id:n,exports:{}};return e[n].call(a.exports,a,a.exports,o),a.exports}
o.m=e,o.p="/static/js/",o.u=function(e){return"chunk-"+e+"."+{101:"a1b2c3",102:"d4e5f6"}[e]+".js"},
o.e=function(e){return Promise.all([o.u(e)].map(function(u){return new Promise(function(n,t){var a=document.createElement("script");a.src=o.p+u,a.onload=n,a.onerror=t,document.head.appendChild(a)})}))},
o(100),o(200),o(300),o(400)};
e[101]=function(n,t,a){var i=a(1),s=[{path:"/",name:"home",children:[{path:"admin",name:"admin"},{path:"user/:id",name:"user"}]},{path:"/login",name:"login"}];
i.createRouter({history:i.createWebHistory("/"),routes:s})};
e[200]=function(n,t,a){var n=a(2),c="/api/v3",u=c+"/orders";
n.get("/api/v3/products",{params:{page:1,size:20,category:"shoes"}});
n.post(u,{sku:"A-1",qty:2,note:"x"});
n.put(`${c}/orders/${id}`,{status:"paid"});
n.delete(c+"/orders/"+id);
n({url:"/api/v3/ping",method:"POST",data:{t:1}});
window.fetch(`/api/v3/search?q=${q}&page=${p}`)};
e[300]=function(n,t,a){a.p="/static/js/";var r="https://cdn.example.com";new Image().src=r+"/img/banner.webp";
document.head.appendChild(function(){var s=document.createElement("style");s.textContent=".hero{background:url(/static/img/hero@2x.png)}",s}())};
e[400]=function(n,t,a){window.__ENV__={VUE_APP_API:"/api/v3",VUE_APP_CDN:"https://cdn.example.com/assets",STATIC:"/static/"}};
//# sourceMappingURL=app.js.map
"""

CHUNK_JS = r"""
(window.webpackJsonp=window.webpackJsonp||[]).push([[101],{700:function(e,t,n){"use strict";
var r=n(1),o={name:"Admin",template:'<div class="admin"><img src="/static/img/avatar-default.png"></div>'};
e.exports=o}},[[700,0]]]);
//# sourceMappingURL=chunk-101.a1b2c3.js.map
"""

SOURCE_MAP = json.dumps({
    "version": 3,
    "file": "app.js",
    "sources": [
        "webpack:///src/main.js",
        "webpack:///src/api/order.js",
        "webpack:///src/views/Admin.vue",
        "webpack:///src/router/index.js",
    ],
    "sourcesContent": ["", "", "", ""],
})

CSS = """.hero{background:url(/static/img/hero@2x.png)}
@font-face{font-family:v;src:url(/static/fonts/v.woff2)}"""

ROUTES = {
    "/": (INDEX, "text/html"),
    "/static/js/app.4a5b6c.js": (APP_JS, "application/javascript"),
    "/static/js/chunk-101.a1b2c3.js": (CHUNK_JS, "application/javascript"),
    "/static/js/app.4a5b6c.js.map": (SOURCE_MAP, "application/json"),
    "/static/css/app.7f8e9d.css": (CSS, "text/css"),
    "/api/v3/products": ('{"data":[],"next":"/api/v3/products?page=2"}', "application/json"),
}


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        path = self.path.split("?")[0]
        entry = ROUTES.get(path)
        if entry is None:
            self.send_response(404)
            self.end_headers()
            return
        body, ctype = entry
        data = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


if __name__ == "__main__":
    with socketserver.TCPServer(("127.0.0.1", 18096), Handler) as httpd:
        print("Vue/webpack 夹具已启动: http://127.0.0.1:18096/")
        httpd.serve_forever()
