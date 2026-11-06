package assetcrawler

import "strings"

// 前端路由里的动态参数有两种写法（vue-router / react-router / 自研表都大同小异）：
//
//	/user/:id          命名参数
//	/files/*rest       通配
//
// 静态爬取只能看到形状，看不到取值。展开成占位值的意义在于：`/user/1` 这类地址
// 在多数实现里会走到与真实记录相同的模板，从而暴露真实接口调用。
// 它同时也是一批 404 的来源，所以必须由调用方显式开启并限流。

const (
	// routeParamPlaceholder 命名参数的占位值。
	routeParamPlaceholder = "1"
	// routeWildcardPlaceholder 通配段的占位值。
	routeWildcardPlaceholder = "index"
)

// expandRoutePath 把一条路由展开成具体路径。
//
// 没有动态段时返回 false——调用方据此跳过，不做无意义的重复入队。
func expandRoutePath(p string) (string, bool) {
	segs := strings.Split(p, "/")
	changed := false
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			segs[i] = routeParamPlaceholder
			changed = true
		case strings.HasPrefix(s, "*"):
			segs[i] = routeWildcardPlaceholder
			changed = true
		}
	}
	if !changed {
		return "", false
	}
	return strings.Join(segs, "/"), true
}
