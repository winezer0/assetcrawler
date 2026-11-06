//go:build routeexec

package routegraph

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// 执行兜底：把 webpack 的懒加载 chunk 真跑一遍，从模块导出的路由对象里取路由。
//
// 只解决静态解析拿不到的那一种情况：路由表由运行时 map/concat 生成，
// 源码里根本没有完整的 path 字面量。
//
// 明确的能力边界（实测）：
//   - 只对「chunk 通过 self.webpackChunkX.push([[...], {模块表}]) 自注册」的产物有效。
//     入口 bundle 把模块表关在 IIFE 闭包里，外部无法枚举，这类拿不到。
//   - ESM / Vite 产物完全不可用：goja 不认 import/export，连动态 import() 都报语法错误。
//   - 会执行目标站的代码，因此必须带超时（vm.Interrupt），并有体积上限。

// ExecOptions 控制执行兜底的行为。
type ExecOptions struct {
	// Timeout 单次执行的时间上限，默认 2 秒。
	Timeout time.Duration
	// MaxSourceBytes 超过这个体积不执行，默认 2MB。
	MaxSourceBytes int
}

func (o ExecOptions) withDefaults() ExecOptions {
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Second
	}
	if o.MaxSourceBytes <= 0 {
		o.MaxSourceBytes = 2 << 20
	}
	return o
}

// prelude 造一个足够让模块初始化跑起来的最小浏览器环境。
//
// document/window 的桩按「怎么调都不炸」设计：
// 读任何属性返回自身，调用返回自身，避免模块在 init 阶段就因为
// 拿不到 API 而中断——我们只要它把路由表构造出来。
const execPrelude = `
globalThis.__payloads = [];
var __stub = new Proxy(function () {}, {
  get: function (t, k) {
    if (k === "then") return undefined;          // 不要被当成 Promise
    if (k === Symbol.toPrimitive) return function () { return ""; };
    if (k === "length") return 0;
    return __stub;
  },
  apply: function (t, self, args) { return __stub; },
  construct: function () { return __stub; }
});
var __self = {
  webpackChunkapp: {
    push: function (item) { globalThis.__payloads.push(item); return 0; }
  },
  location: { href: "http://localhost/", pathname: "/", search: "", hash: "" },
  navigator: { userAgent: "" },
  addEventListener: function () {},
  removeEventListener: function () {},
  setTimeout: function () { return 0; },
  clearTimeout: function () {},
  localStorage: __stub,
  sessionStorage: __stub
};
globalThis.self = __self;
globalThis.window = __self;
globalThis.document = __stub;
globalThis.location = __self.location;
globalThis.navigator = __self.navigator;
globalThis.XMLHttpRequest = __stub;
globalThis.fetch = function () { return __stub; };
globalThis.Image = __stub;
globalThis.__routers = [];
globalThis.__exportedRoutes = [];
`

// execLoader 在 chunk 执行完之后跑：建 require、遍历模块表、抓路由。
//
// 关键是桩函数的行为：当它被传入一个带 routes 数组的对象时（createRouter/createWebHistory
// 这类框架 API），把这个对象记下来并原样返回。否则路由表会被桩吞掉，什么都拿不到。
const execLoader = `
// 载荷可能来自两条路径：
//   1) (self.webpackChunkX = self.webpackChunkX || []).push(...) —— 被注入的 push 捕获
//   2) self.webpackChunkX = []; ...push(...) —— 直接覆盖成真数组，拦截失效，
//      只能事后从数组里读回来
var __payloads2 = __payloads.slice();
try {
  var __arr = globalThis.self && globalThis.self.webpackChunkapp;
  if (__arr && typeof __arr.length === "number") {
    for (var a = 0; a < __arr.length; a++) { __payloads2.push(__arr[a]); }
  }
} catch (e) {}

var __mods = {};
for (var i = 0; i < __payloads2.length; i++) {
  var p = __payloads2[i];
  if (p && p[1] && typeof p[1] === "object") {
    for (var k in p[1]) { if (typeof p[1][k] === "function") __mods[k] = p[1][k]; }
  }
}

var __cache = {};
var __chunkIds = [];

function __fwStub() {
  return new Proxy(function () {}, {
    get: function (t, k) {
      if (k === "then") return undefined;
      return __fwStub();
    },
    apply: function (t, self, args) {
      for (var i = 0; i < args.length; i++) {
        var a = args[i];
        if (a && typeof a === "object" && Object.prototype.toString.call(a.routes) === "[object Array]") {
          globalThis.__routers.push(a);
          return a;
        }
      }
      return __fwStub();
    },
    construct: function () { return __fwStub(); }
  });
}

function __webpack_require__(id) {
  if (__cache[id]) return __cache[id].exports;
  var m = __cache[id] = { id: id, exports: {} };
  var f = __mods[id];
  if (typeof f !== "function") {                     // 依赖模块不在本 chunk 里，给桩
    var stub = __fwStub();                           // 注意要把桩缓存住：
    __cache[id].exports = stub;                      // 否则第二次 require 会拿到空对象
    return stub;
  }
  try {
    f(m, m.exports, __webpack_require__);
  } catch (e) {
    m.__err = String(e);
  }
  return m.exports;
}
__webpack_require__.p = "/";
__webpack_require__.u = function (e) { return String(e); };
__webpack_require__.e = function (e) { __chunkIds.push(String(e)); return Promise.resolve(); };
__webpack_require__.d = function (exports, def) {
  for (var k in def) { try { exports[k] = def[k]; } catch (e) {} }
};
__webpack_require__.r = function (exports) {
  try { exports.__esModule = true; } catch (e) {}
};
__webpack_require__.n = function (m) { return function () { return m; }; };

var __moduleIds = [];
for (var id in __mods) { __moduleIds.push(id); }
__moduleIds.sort();

for (var j = 0; j < __moduleIds.length; j++) {
  var ex;
  try { ex = __webpack_require__(__moduleIds[j]); } catch (e) { continue; }
  if (!ex || typeof ex !== "object") continue;
  // 直接导出 routes 数组
  if (Object.prototype.toString.call(ex.routes) === "[object Array]") {
    globalThis.__routers.push(ex);
  }
  // 导出默认值本身是路由数组
  var d = ex.default;
  if (Object.prototype.toString.call(d) === "[object Array]" && d.length &&
      d[0] && typeof d[0] === "object" && ("path" in d[0])) {
    globalThis.__exportedRoutes.push({ routes: d });
  }
}

// 把各路路由表里的 path/name 摊平，并触发懒加载 thunk 记下 chunk id
var __out = [];
var __seenPath = {};
function __pushRoute(it) {
  if (!it || typeof it !== "object" || typeof it.path !== "string") return;
  if (__seenPath[it.path]) return;
  __seenPath[it.path] = 1;
  __out.push({ path: it.path, name: typeof it.name === "string" ? it.name : "" });
  if (typeof it.component === "function") { try { it.component(); } catch (e) {} }
}
for (var r = 0; r < __routers.length; r++) {
  var rs = __routers[r].routes || [];
  for (var q = 0; q < rs.length; q++) { __pushRoute(rs[q]); }
}
for (var x = 0; x < __exportedRoutes.length; x++) {
  var arr = __exportedRoutes[x].routes || [];
  for (var y = 0; y < arr.length; y++) { __pushRoute(arr[y]); }
}

JSON.stringify({ routes: __out, chunkIds: __chunkIds });
`

// ExecAvailable 表示当前二进制是否编入了执行兜底。
func ExecAvailable() bool { return true }

// ExtractByExec 执行一段 webpack chunk，从其模块导出里恢复路由与懒加载 chunk id。
//
// 安全性：会运行目标站的 JS。默认超时 2 秒、体积上限 2MB，
// 且只在调用方明确开启时使用（见 Options.RouteExec）。
func ExtractByExec(src []byte, opts ExecOptions) (Result, error) {
	opts = opts.withDefaults()
	if len(src) > opts.MaxSourceBytes {
		return Result{}, fmt.Errorf("源码 %d 字节超过执行上限 %d", len(src), opts.MaxSourceBytes)
	}
	if !strings.Contains(string(src), "push") {
		return Result{}, fmt.Errorf("不是自注册 chunk，跳过执行")
	}

	vm := goja.New()
	var timedOut atomic.Bool
	timer := time.AfterFunc(opts.Timeout, func() {
		timedOut.Store(true)
		vm.Interrupt("timeout")
	})
	defer func() {
		timer.Stop()
		vm.ClearInterrupt()
	}()

	if _, err := vm.RunString(execPrelude); err != nil {
		return Result{}, fmt.Errorf("准备执行环境: %w", err)
	}
	if _, err := vm.RunString(string(src)); err != nil {
		if timedOut.Load() {
			return Result{}, fmt.Errorf("执行超时（%v）", opts.Timeout)
		}
		return Result{}, fmt.Errorf("执行 chunk: %w", err)
	}
	v, err := vm.RunString(execLoader)
	if err != nil {
		if timedOut.Load() {
			// loader 里的 try/catch 会吞掉 interrupt，不能只靠返回的 error 判断
			return Result{}, fmt.Errorf("执行超时（%v）", opts.Timeout)
		}
		return Result{}, fmt.Errorf("读取导出: %w", err)
	}
	if timedOut.Load() {
		return Result{}, fmt.Errorf("执行超时（%v）", opts.Timeout)
	}

	var out struct {
		Routes []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"routes"`
		ChunkIDs []string `json:"chunkIds"`
	}
	if err := json.Unmarshal([]byte(v.String()), &out); err != nil {
		return Result{}, fmt.Errorf("解析执行结果: %w", err)
	}

	var res Result
	seen := map[string]struct{}{}
	for _, r := range out.Routes {
		if !validRoutePath(r.Path) {
			continue
		}
		if _, dup := seen[r.Path]; dup {
			continue
		}
		seen[r.Path] = struct{}{}
		res.Routes = append(res.Routes, Route{Path: r.Path, Name: r.Name, Source: "exec"})
	}
	// chunk id 只能给出编号，文件名要靠静态层的 u 函数映射；
	// 这里保留编号，供调用方与静态结果对齐。
	sid := map[string]struct{}{}
	for _, id := range out.ChunkIDs {
		if _, dup := sid[id]; dup {
			continue
		}
		sid[id] = struct{}{}
		res.Chunks = append(res.Chunks, Chunk{ID: id, Source: "exec.e"})
	}
	return res, nil
}
