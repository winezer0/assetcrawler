//go:build ignore

// jsext_prototype.go —— PLAN.md 中 P0-1「JS AST 部分求值」的可行性验证原型。
//
// 独立运行，不参与主模块构建：
//
//	go run testdata/probe/jsext_prototype.go
//
// 验证结论（2026-11-06）：
//   - 纯 Go、CGO_ENABLED=0 可编译，只依赖 dop251/goja
//   - 能把 "/api/" + mode + "/config" 还原成 /api/debug/config（含常量传播）
//   - 对照 jsluice（需 CGO）只能给出 "/users/" 这类碎片，不做常量传播
//
// 两个实现坑：
//  1. goja 未导出 AST Walker，需自行反射遍历
//  2. VariableStatement 的结构是 List []*Binding，不是单个 Binding
package main

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
	"github.com/dop251/goja/token"
)

const src = `
	var API = "/api/v1";
	var mode = "debug";
	fetch(API + "/users/" + id);
	fetch("/api/" + mode + "/config");
	document.location = "/login?redirect=" + redirect + "&method=oauth";
	fetch("/api/static/list");
`

// vars 保存能静态确定的字符串常量，用于常量传播。
var vars = map[string]string{}

// eval 做部分求值：字面量与已知常量拼接，未知节点记为 EXPR。
func eval(n ast.Expression) (string, bool) {
	switch e := n.(type) {
	case *ast.StringLiteral:
		return e.Value.String(), true
	case *ast.Identifier:
		if v, ok := vars[e.Name.String()]; ok {
			return v, true
		}
	case *ast.BinaryExpression:
		if e.Operator == token.PLUS {
			l, lok := eval(e.Left)
			r, rok := eval(e.Right)
			return l + r, lok && rok
		}
	}
	return "EXPR", false
}

// walkNode 反射遍历 AST（goja 未导出 Walker）。
func walkNode(n any, fn func(any)) {
	if n == nil {
		return
	}
	v := reflect.ValueOf(n)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		fn(n)
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < v.NumField(); i++ {
		walkValue(v.Field(i), fn)
	}
}

func walkValue(f reflect.Value, fn func(any)) {
	switch f.Kind() {
	case reflect.Ptr, reflect.Interface:
		if !f.IsNil() {
			walkNode(f.Interface(), fn)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < f.Len(); i++ {
			walkValue(f.Index(i), fn)
		}
	}
}

func main() {
	f, err := parser.ParseFile(nil, "", src, 0)
	if err != nil {
		fmt.Println("parse failed, should fallback to regex:", err)
		return
	}

	// 第一遍：收集 var X = "字符串常量"
	walkNode(f, func(n any) {
		vs, ok := n.(*ast.VariableStatement)
		if !ok {
			return
		}
		for _, b := range vs.List {
			id, ok := b.Target.(*ast.Identifier)
			if !ok {
				continue
			}
			lit, ok := b.Initializer.(*ast.StringLiteral)
			if !ok {
				continue
			}
			vars[id.Name.String()] = lit.Value.String()
		}
	})

	// 第二遍：对每个加法表达式做部分求值
	//
	// 注意两个坑：
	//  1. full=true 不能丢。常量传播后，原本含变量的拼接会变成完全确定的
	//     字面量路径（如 /api/debug/config），这正是精度最高的产出。
	//  2. 嵌套的 + 表达式会重复产出子串（a+b+c 会同时得到 a+b 和 a+b+c）。
	//     必须按源码区间只保留最外层，否则 /api/debug 会作为 /api/debug/config
	//     的子集混进结果。
	type cand struct {
		text       string
		start, end int
		full       bool
	}
	var cands []cand
	seen := map[string]bool{}
	walkNode(f, func(n any) {
		be, ok := n.(*ast.BinaryExpression)
		if !ok || be.Operator != token.PLUS {
			return
		}
		s, full := eval(be)
		if !strings.ContainsAny(s, "/:") || seen[s] {
			return
		}
		seen[s] = true
		cands = append(cands, cand{text: s, start: int(be.Idx0()), end: int(be.Idx1()), full: full})
	})

	var out []string
	for i, a := range cands {
		contained := false
		for j, b := range cands {
			if i == j {
				continue
			}
			// b 严格包含 a，则 a 是子表达式，丢弃
			if b.start <= a.start && a.end <= b.end && (b.start < a.start || a.end < b.end) {
				contained = true
				break
			}
		}
		if contained {
			continue
		}
		kind := "待校验"
		if a.full {
			kind = "已确定"
		}
		out = append(out, fmt.Sprintf("[%s] %s", kind, a.text))
	}

	fmt.Println("=== 部分求值结果 ===")
	for _, s := range out {
		fmt.Println(" ", s)
	}
}
