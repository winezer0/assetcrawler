package treesitter

import (
	"strings"

	"github.com/odvcencio/gotreesitter"
)

// importRef 是一个 import 绑定还原出来的来源信息。
//
// 只关心「调用点上的名字是从哪来的」这一件事：`import { get as fetchIt } from "axios"`
// 之后调用 `fetchIt("/api/x")`，本地名是 fetchIt，模块是 axios，模块内原名是 get。
// 有了它才能判出 HTTP 方法——否则只能看到一个陌生的标识符。
type importRef struct {
	// Module 是 import 语句里的模块路径，如 "axios"、"@/utils/request"。
	Module string
	// Name 是模块内被导入的成员名；默认导入与命名空间导入为空。
	Name string
}

// httpModuleHints 是「这个模块大概是 HTTP 客户端」的关键词。
//
// 只用来决定「要不要拿原名当 HTTP 动词解释」，不是白名单：
// `import { get } from "lodash"` 的 get 是取属性，不能判成 GET，
// 而 lodash 不含这些关键词，正好被排除。
var httpModuleHints = []string{
	"axios", "http", "request", "fetch", "api", "superagent",
}

func httpModule(path string) bool {
	l := strings.ToLower(path)
	for _, h := range httpModuleHints {
		if strings.Contains(l, h) {
			return true
		}
	}
	return false
}

// collectImportBindings 建立「本地名 → 来源」映射。
//
// 必须早于 collectCalls：方法判定发生在收集调用点的当时，
// 映射晚建等于没有。
func (e *extractor) collectImportBindings(root *gotreesitter.Node) {
	e.walk(root, func(n *gotreesitter.Node) {
		if n.Type(e.lang) != "import_statement" {
			return
		}
		src := n.ChildByFieldName("source", e.lang)
		if src == nil {
			return
		}
		module, ok := e.literal(src)
		if !ok || module == "" {
			return
		}
		for i := 0; i < n.NamedChildCount(); i++ {
			if c := n.NamedChild(i); c.Type(e.lang) == "import_clause" {
				e.collectImportClause(c, module)
			}
		}
	})
}

// collectImportClause 拆解 import 子句的三种形态：
//
//	import request from "@/utils/request"       默认导入
//	import { get as fetchIt } from "axios"      具名导入（可带别名）
//	import * as api from "@/api"                命名空间导入
func (e *extractor) collectImportClause(clause *gotreesitter.Node, module string) {
	for i := 0; i < clause.NamedChildCount(); i++ {
		switch c := clause.NamedChild(i); c.Type(e.lang) {
		case "identifier":
			e.bindImport(c.Text(e.src), importRef{Module: module})
		case "named_imports":
			e.collectNamedImports(c, module)
		case "namespace_import":
			for j := 0; j < c.NamedChildCount(); j++ {
				if id := c.NamedChild(j); id.Type(e.lang) == "identifier" {
					e.bindImport(id.Text(e.src), importRef{Module: module})
				}
			}
		}
	}
}

func (e *extractor) collectNamedImports(list *gotreesitter.Node, module string) {
	for i := 0; i < list.NamedChildCount(); i++ {
		spec := list.NamedChild(i)
		if spec.Type(e.lang) != "import_specifier" {
			continue
		}
		name := spec.ChildByFieldName("name", e.lang)
		if name == nil {
			continue
		}
		local := name.Text(e.src)
		if alias := spec.ChildByFieldName("alias", e.lang); alias != nil {
			local = alias.Text(e.src)
		}
		e.bindImport(local, importRef{Module: module, Name: name.Text(e.src)})
	}
}

func (e *extractor) bindImport(local string, ref importRef) {
	if local == "" {
		return
	}
	// 同名重复导入时保留第一条：后出现的多半是另一个作用域的局部同名变量，
	// 覆盖反而会制造错误的来源。
	if _, exists := e.imports[local]; exists {
		return
	}
	e.imports[local] = ref
}

// importedMethod 把「裸标识符调用」还原成 HTTP 方法。
//
// 只在同时满足三条时给结论，否则返回空串（保持「未知」而不是猜）：
//  1. 调用点是裸标识符，不是 obj.method 形式；
//  2. 该名字来自 import，且模块像 HTTP 客户端；
//  3. 模块内原名本身是 HTTP 动词。
func (e *extractor) importedMethod(name string) string {
	ref, ok := e.imports[name]
	if !ok || ref.Name == "" || !httpModule(ref.Module) {
		return ""
	}
	if m, ok := httpVerbs[strings.ToLower(ref.Name)]; ok {
		return m
	}
	return ""
}
