import http.server, socketserver

HTML = """<!DOCTYPE html><html><head>
<meta property="og:url" content="/shared/og-page">
<meta name="twitter:image" content="/img/twitter-card.png">
<link rel="canonical" href="/canonical-page">
<link rel="stylesheet" href="/style.css">
<style>.hero{background:url('/css-inline/hero.jpg')}</style>
</head><body>
<a href="/normal-link">link</a>
<script src="/app.js"></script>
<div data-api-endpoint="/api/from-data-attr"></div>
<form action="/search" method="get"><input name="q"><input name="page"><select name="sort"></select></form>
<form action="/login" method="post"><input name="username"><input name="password"></form>
<script>
  $.get("/api/list", {page:1, size:20});
  axios.post("/api/users", {role:"admin"});
  axios.delete("/api/users/1");
  fetch("/api/items", {method:"PUT"});
</script>
<script>var mode="debug"; fetch("/api/" + mode + "/config"); fetch("/api/data");</script>
</body></html>"""

CSS = """@font-face{src:url('/fonts/inter.woff2')}
.bg{background-image:url('/css-file/bg.jpg')}
.api{background:url("/css-file/api-hint.json")}"""

JS = """var BASE="/v2";
fetch(BASE + "/orders");
const lazy = () => import("./chunk-login.js");
//# sourceMappingURL=app.js.map"""

MAP = '{"version":3,"sources":["webpack:///src/secret-panel.vue","webpack:///src/api/order.js"]}'

JSON_BODY = '{"next":"/api/page/2","links":{"self":"/api/items/1","avatar":"/static/u/1.png"}}'

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/":
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            # CSP 里常见域名白名单与上报地址
            self.send_header("Content-Security-Policy", "default-src 'self'; connect-src https://api.internal.example.com; report-uri /csp-report-endpoint")
            self.end_headers(); self.wfile.write(HTML.encode())
        elif p == "/style.css":
            self.send_response(200); self.send_header("Content-Type", "text/css"); self.end_headers()
            self.wfile.write(CSS.encode())
        elif p == "/app.js":
            self.send_response(200); self.send_header("Content-Type", "application/javascript"); self.end_headers()
            self.wfile.write(JS.encode())
        elif p == "/app.js.map":
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(MAP.encode())
        elif p == "/api/data":
            self.send_response(200); self.send_header("Content-Type", "application/json"); self.end_headers()
            self.wfile.write(JSON_BODY.encode())
        else:
            self.send_response(404); self.end_headers()

with socketserver.TCPServer(("127.0.0.1", 18080), H) as httpd:
    httpd.serve_forever()
