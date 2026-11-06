package routegraph

import (
	"strings"
	"testing"
	"time"
)

// chunkWithDynamicRoutes 是真实 webpack 懒加载 chunk 的形态：
// 模块表通过 push 自注册，501 号模块的路由表由运行时 map 生成。
const chunkWithDynamicRoutes = `
(self.webpackChunkapp = self.webpackChunkapp || []).push([["routes"], {
  500: function (module, exports, __webpack_require__) {
    var router = __webpack_require__(900);
    var routes = [
      { path: "/", name: "home", component: function () { return __webpack_require__.e(101); } },
      { path: "/admin/users/:id", name: "user", component: function () { return __webpack_require__.e(102); } }
    ];
    module.exports = router.createRouter({ routes: routes });
  },
  501: function (module, exports, __webpack_require__) {
    var router = __webpack_require__(900);
    var sections = ["billing", "audit", "settings"];
    var routes = sections.map(function (s, i) {
      return { path: "/" + s, name: s, component: function () { return __webpack_require__.e(200 + i); } };
    });
    module.exports = router.createRouter({ routes: routes });
  }
}]);
`

// TestExecDynamicRoutes 验证执行兜底能拿到静态解析拿不到的路由。
func TestExecDynamicRoutes(t *testing.T) {
	res, err := ExtractByExec([]byte(chunkWithDynamicRoutes), ExecOptions{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	got := map[string]bool{}
	for _, r := range res.Routes {
		got[r.Path] = true
	}
	for _, want := range []string{"/", "/admin/users/:id", "/billing", "/audit", "/settings"} {
		if !got[want] {
			t.Errorf("缺少路由 %s，实际: %v", want, got)
		}
	}
	if len(res.Chunks) == 0 {
		t.Error("未捕获任何懒加载 chunk id")
	}
}

// TestStaticMissesDynamicRoutes 明确静态层的边界：
// map 生成的路由在源码里没有完整 path 字面量，静态必然拿不到。
func TestStaticMissesDynamicRoutes(t *testing.T) {
	res := Extract([]byte(chunkWithDynamicRoutes))
	for _, r := range res.Routes {
		if strings.Contains(r.Path, "billing") {
			t.Fatalf("静态层不应该能推出 /billing，却拿到了: %+v", res.Routes)
		}
	}
	if len(res.Routes) == 0 {
		t.Log("静态层连字面量路由都没拿到，说明本夹具的写法需要检查")
	}
}

// TestExecTimeout 保证死循环不会拖死进程。
func TestExecTimeout(t *testing.T) {
	src := `(self.webpackChunkapp=[]).push([["x"],{1:function(){ while(true){} }}]);`
	start := time.Now()
	_, err := ExtractByExec([]byte(src), ExecOptions{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("死循环应该因超时返回错误")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时保护失效，耗时 %v", d)
	}
	t.Logf("按预期超时中断: %v（耗时 %v）", err, time.Since(start).Round(time.Millisecond))
}

// TestExecRejectsESM 说明 ESM 产物在执行路径上完全不可用。
func TestExecRejectsESM(t *testing.T) {
	src := `import {createRouter} from "vue-router"; export const routes=[{path:"/a"}];`
	if _, err := ExtractByExec([]byte(src), ExecOptions{}); err == nil {
		t.Fatal("ESM 语法应当解析失败")
	} else {
		t.Logf("按预期失败: %v", err)
	}
}
