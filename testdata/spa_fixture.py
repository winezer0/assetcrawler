import http.server, socketserver

INDEX = """<!DOCTYPE html><html><head>
<link rel="stylesheet" href="/static/css/app.css">
<script src="/static/js/runtime.js"></script>
</head><body>
<div id="app"></div>
<script>window.__CONFIG__={apiBase:"/api/v2",cdn:"https://cdn.example.com/static"};</script>
<script src="/static/js/app.js"></script>
<script type="module" src="/static/js/main.mjs"></script>
</body></html>"""

APP_JS = """var BASE="/api/v2";
fetch(BASE+"/orders/"+id);
axios.get(BASE+"/users",{params:{page:1,limit:20}});
axios.post(BASE+"/orders",{sku:"A1",qty:2});
$.ajax({url:BASE+"/legacy/list",type:"POST",data:{a:1,b:2}});
var x=new XMLHttpRequest();x.open("DELETE","/api/v2/carts/9");
const lazy=()=>import("./chunk-report.js");
//# sourceMappingURL=app.js.map"""

RUNTIME_JS = """var R="/api/v2/auth";fetch(R+"/session");"""

MAIN_MJS = """import {x} from './dep.mjs';
fetch("/api/v2/dashboard");"""

CHUNK = """fetch("/api/v2/reports/export");"""

CSS = """.a{background:url('/static/img/bg.png')}"""

MAP = '{"version":3,"sources":["webpack:///src/views/Admin.vue"]}'

ROUTES = {
    "/": (INDEX, "text/html"),
    "/static/js/app.js": (APP_JS, "application/javascript"),
    "/static/js/runtime.js": (RUNTIME_JS, "application/javascript"),
    "/static/js/main.mjs": (MAIN_MJS, "application/javascript"),
    "/static/js/chunk-report.js": (CHUNK, "application/javascript"),
    "/static/css/app.css": (CSS, "text/css"),
    "/static/js/app.js.map": (MAP, "application/json"),
    "/api/v2/users": ('{"data":[],"next":"/api/v2/users?page=2"}', "application/json"),
}

class H(http.server.BaseHTTPRequestHandler):
    def log_message(self,*a): pass
    def do_GET(self):
        p = self.path.split("?")[0]
        if p in ROUTES:
            body, ct = ROUTES[p]
            self.send_response(200); self.send_header("Content-Type", ct)
            self.send_header("Content-Length", str(len(body.encode()))); self.end_headers()
            self.wfile.write(body.encode())
        else:
            self.send_response(404); self.end_headers()

with socketserver.TCPServer(("127.0.0.1", 18095), H) as s:
    s.serve_forever()
