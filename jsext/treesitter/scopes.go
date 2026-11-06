package treesitter

import "github.com/odvcencio/gotreesitter"

// scopeTable 是按作用域分层的常量表。
//
// 为什么需要分层：`extractor.vars` 早期是一张全局 map，多个模块各自声明
// `const n="/api/v1"` 时后写覆盖先写。打包产物里每个模块是一个独立函数、
// 内部短变量同名是常态，于是前一个模块的地址会被后一个模块的值污染，
// 产出源码里根本不存在的 URL（实测 4 条里错 2 条）。
//
// 分层粒度取「函数级」而不是「块级」：URL 常量几乎都声明在函数或模块顶层，
// 块级重名在真实产物里未见，简化不带来可观测的误差。
//
// 这里不做词法作用域提升（`var` 与 `const` 的差异）：两者都记在最近函数作用域，
// 对「这个字面量是什么」这个用途没有区别，`let` 的暂存死区同理不影响静态取值。
type scopeTable struct {
	// vars 是 scopeID → 名字 → 字面值。
	vars map[uint32]map[string]string
	// parent 是 scopeID → 父作用域 ID，用于沿链向上查找。
	parent map[uint32]uint32
}

func newScopeTable() *scopeTable {
	return &scopeTable{
		vars:   map[uint32]map[string]string{},
		parent: map[uint32]uint32{},
	}
}

// declare 登记一个作用域及其父作用域（幂等）。
func (s *scopeTable) declare(scope, parent uint32) {
	if _, ok := s.parent[scope]; ok {
		return
	}
	s.parent[scope] = parent
}

// set 在指定作用域写入常量。
func (s *scopeTable) set(scope uint32, name, value string) {
	m := s.vars[scope]
	if m == nil {
		m = map[string]string{}
		s.vars[scope] = m
	}
	m[name] = value
}

// maxScopeDepth 是向上查找的层数上限，防止 parent 链成环。
const maxScopeDepth = 64

// lookup 从 scope 开始沿作用域链向外查找。
func (s *scopeTable) lookup(name string, scope uint32) (string, bool) {
	cur := scope
	for i := 0; i < maxScopeDepth; i++ {
		if m := s.vars[cur]; m != nil {
			if v, ok := m[name]; ok {
				return v, true
			}
		}
		next, ok := s.parent[cur]
		if !ok || next == cur {
			return "", false
		}
		cur = next
	}
	return "", false
}

// enterScope 在进入作用域边界节点时登记父子关系，返回该作用域的 ID。
// 不是边界时返回 (0, false)，调用方保持当前作用域不变。
//
// 用 switch 而不是 map 查表：这个函数对每个节点都会调用一次，
// 大文件上有几十万次，字符串 switch 由编译器优化成按长度的比较，比 map 快一档。
func (e *extractor) enterScope(n *gotreesitter.Node) (uint32, bool) {
	switch n.Type(e.lang) {
	case "program",
		"function_declaration",
		"function_expression",
		"arrow_function",
		"generator_function",
		"generator_function_declaration",
		"method_definition":
		id := n.StartByte()
		e.scopes.declare(id, e.curScope)
		return id, true
	}
	return 0, false
}
