// Package tree 是 pagefs 的核心：定义固定路径骨架（/tabs.list、/tabs/<id>/…）、
// 分配词组 id、维护 tab 注册表，并把页面内实体文件路由给数据源。
// 树只认识 "tab" 这一全局概念；具体页面数据由 Provider 提供。
package tree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	billy "github.com/go-git/go-billy/v5"

	"github.com/shapled/pagefs/internal/helper"
	"github.com/shapled/pagefs/internal/provider"
)

const (
	srcTimeout  = 10 * time.Second
	contentName = "content"
)

var (
	errReadOnly = errors.New("pagefs: read-only file")
)

// tabSlot 是注册表里的一条：pagefs id ↔ 数据源 + 页面。
type tabSlot struct {
	id  string
	pIx int
	tab provider.TabMeta
}

// Tree 是只读虚拟树。
type Tree struct {
	ids       *helper.Generator
	providers []provider.Provider
	boot      time.Time

	mu   sync.Mutex
	tabs []tabSlot // 按最近一次枚举的顺序
}

// Lstat implements [billy.Filesystem].
func (t *Tree) Lstat(filename string) (os.FileInfo, error) { return t.statAbs(filename) }

// Stat implements [billy.Filesystem].
func (t *Tree) Stat(filename string) (os.FileInfo, error) { return t.statAbs(filename) }

func (t *Tree) statAbs(name string) (info os.FileInfo, err error) {
	defer guard(&err)
	p, err := absPath(name)
	if err != nil {
		return nil, err
	}
	return t.stat(p)
}

// New 建立树，注册提供 tab 实体的数据源。
func New(ids *helper.Generator, providers ...provider.Provider) *Tree {
	return &Tree{ids: ids, providers: providers, boot: time.Now()}
}

// ---- 路径骨架 ----

// stat 返回节点元数据；实体文件的 size 由数据源保证真实。
func (t *Tree) stat(p *Path) (os.FileInfo, error) {
	if p.IsRoot() {
		return DirNode(rootName, t.boot).Info(), nil
	}
	if p.IsTabsList() {
		return FileNode(tabsListName, int64(len(t.renderTabsList())), t.boot).Info(), nil
	}
	if p.IsTabs() {
		t.reconcile()
		return DirNode(tabsName, t.boot).Info(), nil
	}
	if p.IsTabDir() {
		if _, ok := t.lookupTab(p.TabID()); !ok {
			return nil, errNoTab(p.TabID())
		}
		return DirNode(p.TabID(), t.boot).Info(), nil
	}
	if p.IsTabFile() {
		slot, ok := t.lookupTab(p.TabID())
		if !ok {
			return nil, errNoTab(p.TabID())
		}
		// 骨架文件名 → Provider 的语义元数据入口。
		if p.TabFile() != contentName {
			return nil, fs.ErrNotExist
		}
		info, err := t.providers[slot.pIx].StatContent(t.srcCtx(), slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(p.TabID(), err)
		}
		return FileNode(p.TabFile(), info.Size, info.ModTime).Info(), nil
	}
	return nil, fs.ErrNotExist
}

// list 返回目录子项。
func (t *Tree) list(p *Path) ([]os.FileInfo, error) {
	if p.IsRoot() {
		return []os.FileInfo{
			FileNode(tabsListName, 0, t.boot).Info(),
			DirNode(tabsName, t.boot).Info(),
		}, nil
	}
	if p.IsTabs() {
		t.reconcile()
		out := []os.FileInfo{}
		for _, s := range t.tabsSnapshot() {
			out = append(out, DirNode(s.id, t.boot).Info())
		}
		return out, nil
	}
	if p.IsTabDir() {
		if _, ok := t.lookupTab(p.TabID()); !ok {
			return nil, errNoTab(p.TabID())
		}
		// 目录内容由骨架决定；listing 不触发拉取，精确 size 走单文件 stat。
		return []os.FileInfo{
			FileNode(contentName, 0, t.boot).Info(),
		}, nil
	}
	return nil, fs.ErrNotExist
}

// read 打开文件：内容在打开时快照。
func (t *Tree) read(p *Path) ([]byte, error) {
	if p.IsTabsList() {
		return t.renderTabsList(), nil
	}
	if p.IsTabFile() {
		slot, ok := t.lookupTab(p.TabID())
		if !ok {
			return nil, errNoTab(p.TabID())
		}
		// 骨架文件名 → Provider 的语义读取入口。
		if p.TabFile() != contentName {
			return nil, fs.ErrNotExist
		}
		data, err := t.providers[slot.pIx].ReadContent(t.srcCtx(), slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(p.TabID(), err)
		}
		return data, nil
	}
	return nil, fs.ErrNotExist
}

// ---- tab 注册表 ----

// reconcile 枚举全部数据源并刷新注册表：已有页面保持 id 稳定，
// 新页面分配 id，消失的回收。仅在 listing / 复核时同步执行。
// 数据源暂不可达时沿用旧集合。
func (t *Tree) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), srcTimeout)
	defer cancel()
	type liveTab struct {
		ix  int
		tab provider.TabMeta
	}
	var live []liveTab
	for ix, prov := range t.providers {
		tabs, err := prov.TabsList(ctx)
		if err != nil {
			continue
		}
		for _, tab := range tabs {
			live = append(live, liveTab{ix, tab})
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	used := make(map[string]bool, len(live))
	next := make([]tabSlot, 0, len(live))
	for _, lt := range live {
		id := ""
		for _, old := range t.tabs {
			if !used[old.id] && old.pIx == lt.ix && old.tab.ID == lt.tab.ID {
				id = old.id // 已有页面：id 保持稳定
				break
			}
		}
		if id == "" {
			id = t.ids.Next()
		}
		used[id] = true
		next = append(next, tabSlot{id: id, pIx: lt.ix, tab: lt.tab})
	}
	t.tabs = next
}

func (t *Tree) lookupTab(id string) (tabSlot, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.tabs {
		if s.id == id {
			return s, true
		}
	}
	return tabSlot{}, false
}

func (t *Tree) tabsSnapshot() []tabSlot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]tabSlot(nil), t.tabs...)
}

// tabErr 在页面实体操作失败后复核 tab 是否已消失，是则统一为 ENOENT。
func (t *Tree) tabErr(id string, err error) error {
	t.reconcile()
	if _, ok := t.lookupTab(id); !ok {
		return errNoTab(id)
	}
	return err
}

func errNoTab(id string) error {
	return fmt.Errorf("pagefs: tab %s: %w", id, fs.ErrNotExist)
}

func (t *Tree) srcCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), srcTimeout)
	// 超时只约束本次数据源调用；cancel 由 goroutine 兜底释放。
	go func() { <-time.After(2 * srcTimeout); cancel() }()
	return ctx
}

// ---- tabs.list 渲染（全局格式约定） ----

func (t *Tree) renderTabsList() []byte {
	t.reconcile()
	var b strings.Builder
	for i, s := range t.tabsSnapshot() {
		if i > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(s.id)
		b.WriteString("\n")
		b.WriteString(sanitize(s.tab.Title))
		b.WriteString("\n")
		b.WriteString(sanitize(s.tab.URL))
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// sanitize 保证三行一组的结构稳定：字段内容不允许出现换行。
func sanitize(s string) string {
	if strings.ContainsAny(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r", " ")
		s = strings.ReplaceAll(s, "\n", " ")
	}
	return s
}

// ---- billy.Filesystem 适配 ----

// absPath 把 billy 边界进来的路径规范为绝对 Path（go-nfs 传的是相对路径）。
func absPath(name string) (*Path, error) {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return NewPath(name)
}

func (t *Tree) Open(name string) (billy.File, error)   { return t.openAbs(name) }
func (t *Tree) Create(name string) (billy.File, error) { return nil, errReadOnly }

func (t *Tree) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		return nil, errReadOnly
	}
	return t.openAbs(name)
}

func (t *Tree) openAbs(name string) (f billy.File, err error) {
	defer guard(&err)
	p, err := absPath(name)
	if err != nil {
		return nil, err
	}
	return t.open(p)
}

func (t *Tree) open(p *Path) (billy.File, error) {
	data, err := t.read(p)
	if err != nil {
		return nil, err
	}
	return &vfile{name: p.String(), r: bytes.NewReader(data)}, nil
}

func (t *Tree) ReadDir(name string) (out []os.FileInfo, err error) {
	defer guard(&err)
	p, err := absPath(name)
	if err != nil {
		return nil, err
	}
	return t.list(p)
}

func (t *Tree) Join(elem ...string) string { return path.Join(elem...) }
func (t *Tree) Root() string               { return "/" }

func (t *Tree) Rename(string, string) error                     { return errReadOnly }
func (t *Tree) Remove(string) error                             { return errReadOnly }
func (t *Tree) MkdirAll(string, os.FileMode) error              { return errReadOnly }
func (t *Tree) Symlink(string, string) error                    { return errReadOnly }
func (t *Tree) Readlink(string) (string, error)                 { return "", billy.ErrNotSupported }
func (t *Tree) TempFile(dir, prefix string) (billy.File, error) { return nil, errReadOnly }
func (t *Tree) Chroot(string) (billy.Filesystem, error)         { return nil, billy.ErrNotSupported }

// Capabilities 声明只读，billy.Capabilities 据此拒绝写类操作。
func (t *Tree) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.SeekCapability
}

// guard 把树内任何 panic 兜底为 IO 错误：协议服务不能因 bug 崩进程。
func guard(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("pagefs: internal error: %v", r)
	}
}
