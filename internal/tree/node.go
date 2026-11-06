package tree

// node 是骨架节点；kTabs 的实体子节点（tab-<id>）在解析时由注册表给出。
type node struct {
	name     string
	kind     kind
	arg      subkind
	usage    string // 命令文件无响应时的读取内容（自描述）
	children map[string]*node
	slot     *tabSlot // 实体槽位：静态节点为 nil；tab 目录由解析复制原型携带
}

// withSlot 复制原型并携带实体：浅拷贝共享只读 children，解析产物不回写原型。
func (n *node) withSlot(s *tabSlot) *node {
	cp := *n
	cp.slot = s
	return &cp
}

func newNode(name string, k kind, arg subkind) *node {
	return &node{name: name, kind: k, arg: arg}
}

func newCmdNode(name string, arg subkind, usage string) *node {
	return &node{name: name, kind: kCmdTab, arg: arg, usage: usage}
}
