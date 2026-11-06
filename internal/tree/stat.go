package tree

import (
	"os"
	"time"
)

const (
	PermFile = os.FileMode(0o444) // 数据文件：只读
	PermCmd  = os.FileMode(0o666) // 命令文件：可写触发、可读响应/用法
	PermDir  = os.FileMode(0o555) // 只读目录
)

// statInfo 是节点的元数据视图：树与数据源之间的数据语言。
// Name 字段与 os.FileInfo 的 Name() 方法同名冲突，故须经 Info() 适配。
type statInfo struct {
	name    string
	isDir   bool
	size    int64
	mode    os.FileMode // 权限位；目录由树自动加 ModeDir
	modTime time.Time
}

// newFileStatInfo / cmdMeta / dirMeta 快速构造对应权限的节点元数据。
func newFileStatInfo(name string, size int64, mtime time.Time) *statInfo {
	return &statInfo{name: name, mode: PermFile, size: size, modTime: mtime}
}

func newCmdStatInfo(name string, size int64, mtime time.Time) *statInfo {
	return &statInfo{name: name, mode: PermCmd, size: size, modTime: mtime}
}

func newDirStatInfo(name string, mtime time.Time) *statInfo {
	return &statInfo{name: name, isDir: true, mode: PermDir, modTime: mtime}
}

func (si statInfo) Name() string       { return si.name }
func (si statInfo) Size() int64        { return si.size }
func (si statInfo) ModTime() time.Time { return si.modTime }
func (si statInfo) IsDir() bool        { return si.isDir }
func (si statInfo) Sys() any           { return nil }

func (si statInfo) Mode() os.FileMode {
	if si.isDir {
		return si.mode | os.ModeDir
	}
	return si.mode
}
