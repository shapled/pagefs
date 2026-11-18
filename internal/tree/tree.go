// Package tree 是 pagefs 的核心：骨架以节点树存在（node/kind），
// 解析路径得到（节点，tab 实体），行为按 kind 分派、对接 Provider
// 的语义方法。树只认识 "tab" 这一全局概念。
package tree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	billy "github.com/go-git/go-billy/v5"

	"github.com/shapled/pagefs/internal/helper"
	"github.com/shapled/pagefs/internal/provider"
)

const (
	tabPrefix = "tab-"
)

// runtimeVars 是可热更运行时参数（/vars 的背后存储）。
type runtimeVars struct {
	srcTimeout    time.Duration // 数据源调用超时
	metaTimeout   time.Duration // 实时读取与探针超时（须低于客户端 RPC 超时）
	cmdFlushDelay time.Duration // 命令写入防抖窗口
}

func defaultVars() runtimeVars {
	return runtimeVars{
		srcTimeout:    10 * time.Second,
		metaTimeout:   500 * time.Millisecond, // RPC 内同步 CDP 读取的上限（macOS 默认 RPC 超时 0.8s）
		cmdFlushDelay: 300 * time.Millisecond,
	}
}

var (
	// 携带 os.ErrPermission：go-nfs 由此映射为 NFSStatusAccess（EACCES），
	// 否则落 IO 类状态，客户端报 "RPC struct is bad"。
	errReadOnly = fmt.Errorf("pagefs: read-only file: %w", os.ErrPermission)
)

// tabSlot 是注册表里的一条：pagefs id ↔ 数据源 + 页面。
type tabSlot struct {
	id  string
	pIx int
	tab provider.TabMeta
}

// Key 实现 helper.Keyed：注册表以 pagefs id 为键，缓存据此锁内定位。
func (s tabSlot) Key() string { return s.id }

const (
	rootName = "pagefs"
	tabsName = "tabs"
)

// helpText 是 /help 的内容：树的自我说明，与骨架同步维护。
const helpText = `pagefs —— 把浏览器映射为文件系统。procfs/ftrace 风格：读 = 查询，写 = 命令。

/list                          所有 tab 索引：三行一组（id / title / url），组间 ---
/open                          写入 URL 开新 tab；响应第三行输出新 tab 路径
/status                        只读状态快照（JSON）
/vars/<param>                  运行时参数：srcTimeout / metaTimeout / cmdFlushDelay（可写热更）
/tab-<id>/title                标题
/tab-<id>/url                  URL
/tab-<id>/contentType          页面 MIME 类型
/tab-<id>/content              页面内容（HTML 取 outerHTML，其余取渲染文本）
/tab-<id>/screenshot.png       当前视口截图（PNG）
/tab-<id>/navigate             写 URL 原地跳转（触发即回，后台加载）
/tab-<id>/reload               重载（触发即回，后台加载）
/tab-<id>/back|forward         历史导航（触发即回）
/tab-<id>/activate             置前台（抢占桌面焦点）
/tab-<id>/close                关闭 tab（rmdir /tab-<id> 等价）
/tab-<id>/view-source          以源码视图打开本页当前 URL；响应两行：ok|err / 新 tab 路径
/tab-<id>/evaljs               写 JS 表达式，等 Promise 落定，结果 JSON 化

用法：
  cat /list                                     # 枚举所有 tab
  echo "https://example.com" > /open            # 开新 tab
  cat /open                                     # 三行：ok / url / 新 tab 路径
  echo "document.title" > /tab-xxx/evaljs       # 求值
  cat /tab-xxx/evaljs                           # 三行：ok / 表达式 / "Example Domain"
  echo clear > /tab-xxx/evaljs                  # 重置节点，还原用法
  rmdir /tab-xxx                                # 关闭 tab

语义：数据文件读时现取；命令文件写入触发（含空内容，无参动词即靠它），
读取第一行 ok|err，其后按命令回显请求原文（URL/表达式）并附输出或
错误信息，末尾带换行；触发词类命令（reload/close/view-source 等）
不回显请求。写入 clear 或 clr = 重置节点还原用法。命令触发即回
（ok = 已受理），页面加载在后台进行，就绪与否自行查询（如 evaljs 写
document.readyState）。
tab id 固定 tab- 前缀，ls / | grep tab- 即纯枚举；/list 与 ls 显示最近缓存快照，list 事件后台刷新。
`

// Tree 是虚拟树：骨架即结构，MultiSliceCache 上的 tab 注册表是唯一
// 动态命名空间（每数据源一段）。
type Tree struct {
	root      *node
	tabProto  *node
	ids       *helper.Generator
	providers []provider.Provider
	boot      time.Time

	mu        sync.Mutex
	tabCache  *helper.MultiSliceCache[tabSlot, string] // tab 注册表：段即数据源
	vars      runtimeVars                              // 可热更运行时参数（/vars 背后存储）
	lastErr   string                                   // 最近一次实体/命令错误（/status 展示）
	pending   map[string]*pendingCmd                   // 路径 → 待执行命令
	responses map[string]string                        // 路径 → 最近一次命令响应
}

// New 建立树，注册提供 tab 实体的数据源。
func New(ids *helper.Generator, providers ...provider.Provider) *Tree {
	proto := node{name: tabPrefix + "<id>", kind: kTabDir, children: map[string]*node{
		"title":          newNode("title", kMeta, skTitle),
		"url":            newNode("url", kMeta, skURL),
		"contentType":    newNode("contentType", kMeta, skContentType),
		"content":        newNode("content", kContent, ""),
		"screenshot.png": newNode("screenshot.png", kShot, ""),
		"navigate":       newCmdNode("navigate", skURL, "写入 URL 原地跳转（触发即回，后台加载）"),
		"reload":         newCmdNode("reload", skNone, "写入任意内容触发重载（触发即回，后台加载）"),
		"back":           newCmdNode("back", skNone, "写入任意内容后退历史（触发即回）"),
		"forward":        newCmdNode("forward", skNone, "写入任意内容前进历史（触发即回）"),
		"activate":       newCmdNode("activate", skNone, "写入任意内容把窗口置前台（抢占桌面焦点）"),
		"close":          newCmdNode("close", skNone, "写入任意内容关闭 tab"),
		"view-source":    newCmdNode("view-source", skNone, "写入任意内容，以源码视图打开本页当前 URL；响应第二行输出新 tab 路径"),
		"evaljs":         newCmdNode("evaljs", skExpr, "写入 JS 表达式（可返回 Promise），响应第三行输出 JSON 结果"),
	}}
	// 拍平：根即动态命名空间（tab- 前缀实体）+ 控制文件，无 /tabs 层。
	root := node{name: rootName, kind: kTabs, children: map[string]*node{
		"list": newNode("list", kRender, ""),
		"open": {name: "open", kind: kCmdRoot, arg: skURL,
			usage: "写入 URL 开新 tab（触发即回），响应第三行输出新 tab 路径"},
		"help":   newNode("help", kHelp, ""),
		"status": newNode("status", kStatus, ""),
		"vars": {name: "vars", kind: kDir, children: map[string]*node{
			"srcTimeout":    newNode("srcTimeout", kVar, skSrcTimeout),
			"metaTimeout":   newNode("metaTimeout", kVar, skMetaTimeout),
			"cmdFlushDelay": newNode("cmdFlushDelay", kVar, skCmdFlushDelay),
		}},
	}}
	return &Tree{
		root:      &root,
		tabProto:  &proto,
		ids:       ids,
		providers: providers,
		boot:      time.Now(),
		tabCache:  helper.NewMultiSliceCache[string, tabSlot](len(providers)),
		vars:      defaultVars(),
		pending:   map[string]*pendingCmd{},
		responses: map[string]string{},
	}
}

// ---- 路径解析 ----

// resolve 沿骨架走路径。kTabs 是动态命名空间：tab- 前缀查聚合
// （alive 自带未命中复核），取不到即不存在；tab 目录由原型复制携带实体。
func (t *Tree) resolve(p *Path) (*node, error) {
	cur := t.root
	for _, part := range p.Parts {
		if cur.kind == kTabs && strings.HasPrefix(part, tabPrefix) {
			s, ok := t.tabAlive(part)
			if !ok {
				return nil, errNoTab(part)
			}
			cur = t.tabProto.withSlot(&s)
			continue
		}
		child, ok := cur.children[part]
		if !ok {
			return nil, fs.ErrNotExist
		}
		cur = child
	}
	return cur, nil
}

// ---- 行为分派 ----

// stat 返回节点元数据；实体文件的 size 必须真实（NFS 客户端按它决定
// READ 是否短路），取不到真实 size 的节点先执行获取。
func (t *Tree) stat(p *Path) (*statInfo, error) {
	n, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	if p.EndWithSlash && !n.kind.isDir() {
		return nil, fs.ErrNotExist // 文件不接受尾斜杠
	}
	switch n.kind {
	case kDir, kTabs:
		return newDirStatInfo(n.name, t.boot), nil
	case kTabDir:
		return newDirStatInfo(n.slot.id, t.boot), nil
	case kRender:
		return newFileStatInfo(n.name, int64(len(t.renderTabsList())), t.boot), nil
	case kHelp:
		return newFileStatInfo(n.name, int64(len(helpText)), t.boot), nil
	case kVar:
		v := t.getVar(n.arg)
		return newCmdStatInfo(n.name, int64(len(v)), time.Now()), nil
	case kStatus:
		b := t.statusJSON()
		return newFileStatInfo(n.name, int64(len(b)), t.boot), nil
	case kMeta:
		switch n.arg {
		case skTitle, skURL:
			v, err := t.metaValue(n, n.arg)
			if err != nil {
				return nil, err
			}
			return newFileStatInfo(n.name, int64(len(v)), time.Now()), nil
		case skContentType:
			ct, err := t.providers[n.slot.pIx].ContentType(t.srcCtx(), n.slot.tab.ID)
			if err != nil {
				return nil, t.tabErr(n.slot.id, err)
			}
			return newFileStatInfo(n.name, int64(len(ct)), time.Now()), nil
		}
		return nil, fs.ErrNotExist
	case kContent:
		info, err := t.providers[n.slot.pIx].StatContent(t.srcCtx(), n.slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(n.slot.id, err)
		}
		return newFileStatInfo(n.name, info.Size, info.ModTime), nil
	case kShot:
		data, err := t.providers[n.slot.pIx].ReadScreenshot(t.srcCtx(), n.slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(n.slot.id, err)
		}
		return newFileStatInfo(n.name, int64(len(data)), time.Now()), nil
	case kCmdRoot, kCmdTab:
		// GETATTR 即冲刷：客户端以 GETATTR 的 size 界定后续 READ 的长度，
		// size 必须是执行后响应的真实长度。代价：多块写的下一块 pre-op
		// Stat 会把首块提前执行——超过单个 WRITE 分块的命令会被分段，
		// 现实命令远小于分块，按可接受限制处理。
		t.flushPending(p.String())
		resp, has := t.response(p.String())
		if !has {
			resp = n.usage // 无响应时读到的即用法说明
		}
		return newCmdStatInfo(n.name, int64(len(resp)), time.Now()), nil
	}
	return nil, fs.ErrNotExist
}

// metaValue 实时优先读取元数据：RefreshKey 单点刷新（metaTimeout 上
// 限），fetchErr 走缓存回退由 helper 记日志；ErrCacheMiss → ENOENT。
func (t *Tree) metaValue(n *node, arg subkind) (string, error) {
	m, _, err := t.tabCache.RefreshKey(n.slot.id, t.metaTimeout(), t.tabMetaMerge)
	if err != nil {
		return "", errNoTab(n.slot.id)
	}
	switch arg {
	case skTitle:
		return m.tab.Title, nil
	case skURL:
		return m.tab.URL, nil
	}
	return "", ErrUnknownSubKind
}

// list 返回目录子项；listing 不触发数据源拉取，精确 size 走单文件 stat。
func (t *Tree) list(p *Path) ([]os.FileInfo, error) {
	n, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	switch n.kind {
	case kDir, kTabDir:
		return t.staticListing(n), nil
	case kTabs:
		out := t.staticListing(n)
		// ls 永不阻塞：返回缓存快照，list 事件后台异步刷新（冷启动缓存
		// 为空时同步刷一次，保证首次枚举可见）
		if snap := t.tabCache.Snapshot(); len(snap) == 0 {
			for _, s := range t.refreshTabs(t.metaTimeout()) { // 冷启动同步：短超时，RPC 内
				out = append(out, newDirStatInfo(s.id, t.boot))
			}
		} else {
			for _, s := range snap {
				out = append(out, newDirStatInfo(s.id, t.boot))
			}
			t.refreshTabsAsync()
		}
		return out, nil
	}
	return nil, fs.ErrInvalid
}

// staticListing 按名字序输出静态骨架子项。
func (t *Tree) staticListing(n *node) []os.FileInfo {
	names := make([]string, 0, len(n.children))
	for name := range n.children {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]os.FileInfo, 0, len(names))
	for _, name := range names {
		child := n.children[name]
		if child.kind.isDir() {
			out = append(out, newDirStatInfo(name, t.boot))
		} else if child.kind.isCmd() {
			out = append(out, newCmdStatInfo(name, 0, t.boot))
		} else {
			out = append(out, newFileStatInfo(name, 0, t.boot))
		}
	}
	return out
}

// read 读取文件内容（打开时快照）。读打开是命令的冲刷点：
// cat 的 open 先于 fstat，冲刷后 GETATTR 即拿到响应的真实 size。
func (t *Tree) read(p *Path) ([]byte, error) {
	n, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	if n.kind.isDir() || p.EndWithSlash {
		return nil, fs.ErrInvalid
	}
	switch n.kind {
	case kRender:
		return t.renderTabsList(), nil
	case kHelp:
		return []byte(helpText), nil
	case kVar:
		return []byte(t.getVar(n.arg)), nil
	case kStatus:
		return t.statusJSON(), nil
	case kMeta:
		switch n.arg {
		case skTitle, skURL:
			v, err := t.metaValue(n, n.arg)
			if err != nil {
				return nil, err
			}
			return []byte(v), nil
		case skContentType:
			ct, err := t.providers[n.slot.pIx].ContentType(t.srcCtx(), n.slot.tab.ID)
			if err != nil {
				return nil, t.tabErr(n.slot.id, err)
			}
			return []byte(ct), nil
		}
	case kContent:
		data, err := t.providers[n.slot.pIx].ReadContent(t.srcCtx(), n.slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(n.slot.id, err)
		}
		return data, nil
	case kShot:
		data, err := t.providers[n.slot.pIx].ReadScreenshot(t.srcCtx(), n.slot.tab.ID)
		if err != nil {
			return nil, t.tabErr(n.slot.id, err)
		}
		return data, nil
	case kCmdRoot, kCmdTab:
		t.flushPending(p.String())
		resp, has := t.response(p.String())
		if !has {
			resp = n.usage // 无响应时读到的即用法说明
		}
		return []byte(resp), nil
	}
	return nil, fs.ErrNotExist
}

// ---- tab 注册表（MultiSliceCache 之上的注册表语义）----

// enumerateAll 全量 list 事件的 fetch：逐数据源枚举、按段稳定 id；
// 全部数据源不可达时报错（缓存沿用，瞬时故障不清空注册表）。
func (t *Tree) enumerateAll(ctx context.Context, old [][]tabSlot) ([][]tabSlot, error) {
	out := make([][]tabSlot, len(old))
	reachable := 0
	for ix := range t.providers {
		seg, err := t.enumerateSeg(ctx, ix, old[ix])
		if err != nil {
			out[ix] = old[ix]
			continue
		}
		out[ix] = seg
		reachable++
	}
	if reachable == 0 {
		return nil, context.DeadlineExceeded
	}
	return out, nil
}

// enumerateSeg 单数据源枚举并按旧段稳定 id：已有页面保持 id，
// 新页面分配 id，消失的随段回收。
func (t *Tree) enumerateSeg(ctx context.Context, pIx int, old []tabSlot) ([]tabSlot, error) {
	tabs, err := t.providers[pIx].TabsList(ctx)
	if err != nil {
		return nil, err
	}
	used := make(map[string]bool, len(tabs))
	next := make([]tabSlot, 0, len(tabs))
	for _, tab := range tabs {
		id := ""
		for _, o := range old {
			if !used[o.id] && o.tab.ID == tab.ID {
				id = o.id
				break
			}
		}
		if id == "" {
			id = tabPrefix + t.ids.Next()
		}
		used[id] = true
		next = append(next, tabSlot{id: id, pIx: pIx, tab: tab})
	}
	return next, nil
}

// refreshTabs 触发一次全量 list 事件，返回扁平快照。
func (t *Tree) refreshTabs(timeout time.Duration) []tabSlot {
	return t.tabCache.Refresh(t.srcTimeout(), t.enumerateAll)
}

// refreshTabSeg 单源 list 事件：只重枚举第 pIx 段（新 tab 必然落在
// 已知的那个数据源段，open/view-source 的应答路径用）。
func (t *Tree) refreshTabSeg(pIx int, timeout time.Duration) {
	t.tabCache.RefreshSlice(pIx, timeout, func(ctx context.Context, old []tabSlot) ([]tabSlot, error) {
		return t.enumerateSeg(ctx, pIx, old)
	})
}

func (t *Tree) lookupTab(id string) (tabSlot, bool) {
	for _, s := range t.tabCache.Snapshot() {
		if s.id == id {
			return s, true
		}
	}
	return tabSlot{}, false
}

// tabBySource 按数据源稳定键查 slot：open/view-source 执行后回查分配的 id。
func (t *Tree) tabBySource(key string) (tabSlot, bool) {
	for _, s := range t.tabCache.Snapshot() {
		if s.tab.ID == key {
			return s, true
		}
	}
	return tabSlot{}, false
}

// tabAlive 取在册实体：命中即回；未命中（新 tab 尚未入册）触发全量
// list 事件发现——RefreshKey 只能刷新已存在的槽位，发现不了新实体。
// RPC 内同步执行，用短超时（metaTimeout）。
func (t *Tree) tabAlive(id string) (tabSlot, bool) {
	if s, ok := t.lookupTab(id); ok {
		return s, true
	}
	t.refreshTabs(t.metaTimeout())
	return t.lookupTab(id)
}

// tabGone 复核实体是否消失：优先 RefreshKey 单点实时探测（fetchErr =
// nil 即还在且顺手保鲜），探测失败（瞬断或真死）再全量复核一次区分。
// RPC 内同步执行，用短超时（metaTimeout）。
func (t *Tree) tabGone(id string) bool {
	_, fetchErr, err := t.tabCache.RefreshKey(id, t.metaTimeout(), t.tabMetaMerge)
	if err != nil {
		return true // 键已不在缓存
	}
	if fetchErr == nil {
		return false // 实时取到 = 还在
	}
	t.refreshTabs(t.metaTimeout())
	_, ok := t.lookupTab(id)
	return !ok
}

// tabErr 在页面实体操作失败后复核 tab 是否已消失，是则统一为 ENOENT。
func (t *Tree) tabErr(id string, err error) error {
	if t.tabGone(id) {
		return errNoTab(id)
	}
	t.setLastErr(err)
	return err
}

// tabMetaMerge 实时元数据 fetch：取本槽当前页面 Meta 并合并进 tabSlot
// （id/pIx 保留）。
func (t *Tree) tabMetaMerge(ctx context.Context, old tabSlot) (tabSlot, error) {
	meta, err := t.providers[old.pIx].Meta(ctx, old.tab.ID)
	if err != nil {
		return tabSlot{}, err
	}
	old.tab = meta
	return old, nil
}

// errNoTab 用 fs.PathError 承载：go-nfs 以 os.IsNotExist 判定 ENOENT，
// 而它不解包 fmt.Errorf 的 %w 链，包一层会被映射成 EIO。
func errNoTab(id string) error {
	return &fs.PathError{Op: "pagefs tab", Path: id, Err: fs.ErrNotExist}
}

func (t *Tree) srcCtx() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), t.srcTimeout())
	// 超时只约束本次数据源调用；cancel 由 goroutine 兜底释放。
	go func() { <-time.After(2 * t.srcTimeout()); cancel() }()
	return ctx
}

// srcTimeout / metaTimeout / flushDelay 读取当前可热更参数。
func (t *Tree) srcTimeout() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vars.srcTimeout
}

func (t *Tree) metaTimeout() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vars.metaTimeout
}

func (t *Tree) flushDelay() time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.vars.cmdFlushDelay
}

func (t *Tree) setLastErr(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastErr = err.Error()
}

// getVar 返回运行时参数的当前值（/vars/<param> 读侧）。
func (t *Tree) getVar(name subkind) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch name {
	case skSrcTimeout:
		return t.vars.srcTimeout.String()
	case skMetaTimeout:
		return t.vars.metaTimeout.String()
	case skCmdFlushDelay:
		return t.vars.cmdFlushDelay.String()
	}
	return ""
}

// setVar 校验并应用运行时参数，返回 (载荷, 错误原因)。
func (t *Tree) setVar(name subkind, val string) (string, string) {
	d, err := time.ParseDuration(strings.TrimSpace(val))
	if err != nil || d <= 0 {
		return "err", "invalid duration: " + val
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch name {
	case skSrcTimeout:
		t.vars.srcTimeout = d
	case skMetaTimeout:
		t.vars.metaTimeout = d
	case skCmdFlushDelay:
		t.vars.cmdFlushDelay = d
	default:
		return "err", "unknown var"
	}
	return "ok", d.String()
}

// statusJSON 组装 /status 只读快照。
func (t *Tree) statusJSON() []byte {
	t.mu.Lock()
	lastErr := t.lastErr
	t.mu.Unlock()
	type status struct {
		Boot      string `json:"boot"`
		Providers int    `json:"providers"`
		Tabs      int    `json:"tabs"`
		LastError string `json:"lastError,omitempty"`
	}
	st := status{
		Boot:      t.boot.Format(time.RFC3339),
		Providers: len(t.providers),
		Tabs:      len(t.tabCache.Snapshot()),
		LastError: lastErr,
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return b
}

// response 返回命令路径上的最近响应；has 形式供墓碑判定。
func (t *Tree) response(path string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, has := t.responses[path]
	return s, has
}

// ---- tabs/list 渲染（全局格式约定） ----

func (t *Tree) renderTabsList() []byte {
	var b strings.Builder
	// 与 ls 同策略：即时返回缓存快照，list 事件后台刷新；冷启动同步刷一次。
	if snap := t.tabCache.Snapshot(); len(snap) > 0 {
		t.renderFrom(snap, &b)
		t.refreshTabsAsync()
		return []byte(b.String())
	}
	t.renderFrom(t.refreshTabs(t.metaTimeout()), &b)
	return []byte(b.String())
}

func (t *Tree) renderFrom(snap []tabSlot, b *strings.Builder) {
	for i, s := range snap {
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
}

// refreshTabsAsync 后台触发 list 事件：枚举永不阻塞读路径。
func (t *Tree) refreshTabsAsync() {
	go t.refreshTabs(t.srcTimeout())
}

// sanitize 保证逐行结构稳定：字段内容不允许出现换行。
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

func (t *Tree) Open(name string) (billy.File, error)   { return t.openAbs(name, false) }
func (t *Tree) Create(name string) (billy.File, error) { return t.openAbs(name, true) }

func (t *Tree) OpenFile(name string, flag int, perm os.FileMode) (billy.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_APPEND|os.O_TRUNC) != 0 {
		return t.openAbs(name, true)
	}
	return t.openAbs(name, false)
}

func (t *Tree) openAbs(name string, forWrite bool) (f billy.File, err error) {
	defer guard(&err)
	p, err := absPath(name)
	if err != nil {
		return nil, err
	}
	tr("Open", p.String(), nil)
	if forWrite {
		return t.openWrite(p)
	}
	return t.openRead(p)
}

// openRead 打开文件为内存快照句柄。
func (t *Tree) openRead(p *Path) (billy.File, error) {
	data, err := t.read(p)
	tr("Read", p.String(), err)
	if err != nil {
		return nil, err
	}
	return &vfile{name: p.String(), r: bytes.NewReader(data)}, nil
}

// openWrite 只对命令文件放行，实体数据文件一律只读。
// 不在此 reset pending：go-nfs 逐 op 开句柄，中途 reset 会丢掉前面的块；
// 放弃旧命令是 SETATTR 截断（cmdFile.Truncate）的职责。
func (t *Tree) openWrite(p *Path) (billy.File, error) {
	n, err := t.resolve(p)
	if err != nil {
		return nil, err
	}
	if !n.kind.isCmd() && n.kind != kVar {
		return nil, errReadOnly
	}
	return &cmdFile{t: t, path: p.String(), n: n}, nil
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
	info, err = t.stat(p)
	tr("Stat", p.String(), err)
	return info, err
}

func (t *Tree) ReadDir(name string) (out []os.FileInfo, err error) {
	defer guard(&err)
	p, err := absPath(name)
	if err != nil {
		return nil, err
	}
	out, err = t.list(p)
	tr("ReadDir", p.String(), err)
	return out, err
}

// Change 是 SETATTR 元数据操作的 no-op：虚拟树的权限/时间无外部效力，
// 全部接受以免客户端的重定向流程卡在模式/时间设置上。
func (t *Tree) Chmod(string, os.FileMode) error            { return nil }
func (t *Tree) Lchown(string, int, int) error              { return nil }
func (t *Tree) Chown(string, int, int) error               { return nil }
func (t *Tree) Chtimes(string, time.Time, time.Time) error { return nil }

func (t *Tree) Join(elem ...string) string { return path.Join(elem...) }
func (t *Tree) Root() string               { return "/" }

func (t *Tree) Rename(string, string) error { return errReadOnly }

// tr 输出操作级 trace（PAGEFS_TRACE 开启时），定位协议级问题用。
var tr = func(op, path string, err error) {
	if os.Getenv("PAGEFS_TRACE") == "" {
		return
	}
	if err != nil {
		log.Printf("[trace] %s %s ERR %v", op, path, err)
	} else {
		log.Printf("[trace] %s %s", op, path)
	}
}

func (t *Tree) Remove(name string) error {
	defer func() {
		tr("Remove", name, nil)
	}()
	p, err := absPath(name)
	if err != nil {
		return err
	}
	// 特化：RMDIR(tab-dir) → close（rm -r 的成员 EROFS 报错不影响最终关闭）
	if len(p.Parts) == 1 && strings.HasPrefix(p.Parts[0], tabPrefix) {
		slot, ok := t.tabAlive(p.Parts[0])
		if !ok {
			return errNoTab(p.Parts[0])
		}
		return t.providers[slot.pIx].CloseTab(t.srcCtx(), slot.tab.ID)
	}
	return errReadOnly
}
func (t *Tree) MkdirAll(string, os.FileMode) error              { return errReadOnly }
func (t *Tree) Symlink(string, string) error                    { return errReadOnly }
func (t *Tree) Readlink(string) (string, error)                 { return "", billy.ErrNotSupported }
func (t *Tree) TempFile(dir, prefix string) (billy.File, error) { return nil, errReadOnly }
func (t *Tree) Chroot(string) (billy.Filesystem, error)         { return nil, billy.ErrNotSupported }

// Capabilities 声明读写能力：写仅限命令文件，逐路径把关在 openWrite。
func (t *Tree) Capabilities() billy.Capability {
	return billy.ReadCapability | billy.WriteCapability | billy.ReadAndWriteCapability |
		billy.SeekCapability | billy.TruncateCapability
}

// guard 把树内任何 panic 兜底为 IO 错误：协议服务不能因 bug 崩进程。
func guard(err *error) {
	if r := recover(); r != nil {
		log.Printf("[dbg] panic: %v\n%s", r, debug.Stack())
		*err = fmt.Errorf("pagefs: internal error: %v", r)
	}
}
