// 实验：goja 挂极简 __webpack_require__，加载 webpack chunk 里的路由模块。
//
// 目标不是做产品，而是回答一个问题：
//
//	「esbuild 定位模块 + goja 挂 require + 执行导出 routes」这条路到底能拿到什么、
//	 又会卡在哪。
//
// 放在 testdata 下，Go 工具链的 ./... 不会编译它，需要手动 go run。
package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/dop251/goja"
)

// chunk 是真实 webpack 5 chunk 的形态：工厂函数表 push 到全局数组。
// 500 是静态字面量路由表，501 是运行时 map 生成的路由表。
const chunk = `
(self.webpackChunkapp = self.webpackChunkapp || []).push([["routes"], {
  500: function (module, exports, __webpack_require__) {
    var router = __webpack_require__(900);
    var routes = [
      { path: "/", name: "home", component: function () { return __webpack_require__.e(101); } },
      { path: "/admin/users/:id", name: "user", component: function () { return __webpack_require__.e(102); } },
      { path: "/report", name: "report", component: function () { return __webpack_require__.e(103); } }
    ];
    module.exports = router.createRouter({ history: router.createWebHistory("/"), routes: routes });
  },
  501: function (module, exports, __webpack_require__) {
    var router = __webpack_require__(900);
    var sections = ["billing", "audit", "settings"];
    var routes = sections.map(function (s, i) {
      return { path: "/" + s, name: s, component: function () { return __webpack_require__.e(200 + i); } };
    });
    module.exports = router.createRouter({ routes: routes });
  },
  900: function (module) {
    module.exports = {
      createRouter: function (opts) { return { routes: opts.routes }; },
      createWebHistory: function (b) { return { base: b }; }
    };
  }
}]);
`

// prelude 在 chunk 之前执行：造一个假的浏览器全局 + 捕获 push 进来的载荷。
const prelude = `
var __self = {};
globalThis.self = __self;
globalThis.window = __self;
__self.webpackChunkapp = { push: function (item) { globalThis.__captured = item; } };
`

// loader 在 chunk 之后执行：极简 __webpack_require__ + 读取路由 + 触发懒加载。
const loader = `
var __mods = __captured[1];
var __cache = {};
var __chunks = [];

function __webpack_require__(id) {
  if (__cache[id]) return __cache[id].exports;
  var m = __cache[id] = { id: id, exports: {} };
  __mods[id](m, m.exports, __webpack_require__);
  return m.exports;
}
__webpack_require__.p = "/static/js/";
__webpack_require__.u = function (e) {
  return "chunk-" + e + "." + { 101: "a1b2c3", 102: "d4e5f6", 103: "9f8e7d", 200: "aa11", 201: "bb22", 202: "cc33" }[e] + ".js";
};
__webpack_require__.e = function (e) { __chunks.push(e); return Promise.resolve(); };

var __result = [];
[500, 501].forEach(function (id) {
  try {
    var exp = __webpack_require__(id);
    var rs = (exp && exp.routes) || [];
    var paths = [];
    rs.forEach(function (r) {
      paths.push({ path: r.path, name: r.name });
      // 调用懒加载 thunk，把 chunk id 抽出来
      if (typeof r.component === "function") { try { r.component(); } catch (e) {} }
    });
    __result.push({ module: id, routes: paths });
  } catch (e) {
    __result.push({ module: id, error: String(e) });
  }
});

JSON.stringify({ loaded: __result, chunks: __chunks });
`

func main() {
	vm := goja.New()

	// 执行超时保护：真实目标是不可信的压缩代码，必须能停。
	timer := time.AfterFunc(3*time.Second, func() { vm.Interrupt("timeout") })
	defer timer.Stop()

	must := func(stage, src string) goja.Value {
		v, err := vm.RunString(src)
		if err != nil {
			fmt.Printf("[%s] 执行失败: %v\n", stage, err)
			return nil
		}
		return v
	}

	must("prelude", prelude)
	must("chunk", chunk)

	if vm.Get("__captured") == nil {
		fmt.Println("未捕获到 chunk 载荷")
		return
	}

	v := must("loader", loader)
	if v == nil {
		return
	}

	var out struct {
		Loaded []struct {
			Module int `json:"module"`
			Routes []struct {
				Path string `json:"path"`
				Name string `json:"name"`
			} `json:"routes"`
			Error string `json:"error"`
		} `json:"loaded"`
		Chunks []int `json:"chunks"`
	}
	if err := json.Unmarshal([]byte(v.String()), &out); err != nil {
		fmt.Println("解析结果失败:", err)
		return
	}

	fmt.Println("=== goja 执行结果 ===")
	for _, m := range out.Loaded {
		if m.Error != "" {
			fmt.Printf("  模块 %d 失败: %s\n", m.Module, m.Error)
			continue
		}
		fmt.Printf("  模块 %d: %d 条路由\n", m.Module, len(m.Routes))
		for _, r := range m.Routes {
			fmt.Printf("      %-22s %s\n", r.Path, r.Name)
		}
	}
	fmt.Printf("  懒加载捕获的 chunk id: %v\n", out.Chunks)
	fmt.Println("  对应文件:", chunkFiles(out.Chunks))
}

func chunkFiles(ids []int) []string {
	hashes := map[int]string{101: "a1b2c3", 102: "d4e5f6", 103: "9f8e7d", 200: "aa11", 201: "bb22", 202: "cc33"}
	var out []string
	for _, id := range ids {
		out = append(out, fmt.Sprintf("/static/js/chunk-%d.%s.js", id, hashes[id]))
	}
	return out
}
