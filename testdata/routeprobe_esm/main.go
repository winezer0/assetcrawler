// 实验二：goja 执行路线在 ESM / DOM 依赖下的失效边界。
package main

import (
	"fmt"

	"github.com/dop251/goja"
)

func try(name, src string) {
	vm := goja.New()
	_, err := vm.RunString(src)
	if err != nil {
		fmt.Printf("  %-22s 失败: %v\n", name, err)
		return
	}
	fmt.Printf("  %-22s 成功\n", name)
}

func main() {
	fmt.Println("=== goja 直接执行各类真实产物片段 ===")
	try("ESM import/export", `import {createRouter} from "vue-router"; export default [{path:"/a"}];`)
	try("ESM export 单独", `export const routes = [{path:"/a"}];`)
	try("动态 import()", `import("./chunk-a.js").then(function(){});`)
	try("可选链/空值合并", `var a = {b:{c:1}}; var x = a?.b?.c ?? 2;`)
	try("class 私有字段", `class A { #p = 1; get p(){return this.#p} }; new A().p;`)
	try("DOM: document 存在", `var el = document.createElement("a");`)
	try("DOM: 打桩后可跑", `var el = ({createElement:function(){return {href:""}}}).createElement("a"); el.href="/x";`)
	try("while(1) 死循环", `while(true){}`)

	fmt.Println("\n=== 结论 ===")
	fmt.Println("  ESM 语法直接拒绝；StripModuleSyntax 那种剥离只在「解析提取」时可用，")
	fmt.Println("  到了「执行」阶段剥离 import 等于删掉依赖，模块必然报错。")
}
