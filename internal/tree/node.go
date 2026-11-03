package tree

import (
	"os"
	"time"
)

const (
	PermFile = os.FileMode(0o444) // 只读文件
	PermDir  = os.FileMode(0o555) // 只读目录
)

// FileNode 快速构造只读文件节点。
func FileNode(name string, size int64, mtime time.Time) Node {
	return Node{Name: name, Mode: PermFile, Size: size, ModTime: mtime}
}

// DirNode 快速构造只读目录节点。
func DirNode(name string, mtime time.Time) Node {
	return Node{Name: name, IsDir: true, Mode: PermDir, ModTime: mtime}
}

// Node 是树节点的元数据：树与数据源之间的数据语言。
// Name 字段与 os.FileInfo 的 Name() 方法同名冲突，故须经 Info() 适配。
type Node struct {
	Name    string
	IsDir   bool
	Size    int64
	Mode    os.FileMode // 权限位；目录由树自动加 ModeDir
	ModTime time.Time
}

func (n Node) Info() os.FileInfo { return fileInfo{n} }

// fileInfo 是 Node → os.FileInfo 的适配层，仅在树的 billy 边界使用。
type fileInfo struct{ n Node }

func (f fileInfo) Name() string       { return f.n.Name }
func (f fileInfo) Size() int64        { return f.n.Size }
func (f fileInfo) ModTime() time.Time { return f.n.ModTime }
func (f fileInfo) IsDir() bool        { return f.n.IsDir }
func (f fileInfo) Sys() any           { return nil }

func (f fileInfo) Mode() os.FileMode {
	if f.n.IsDir {
		return f.n.Mode | os.ModeDir
	}
	return f.n.Mode
}
