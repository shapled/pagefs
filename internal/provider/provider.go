package provider

import (
	"context"
	"time"
)

// TabMeta 是数据源枚举出的一个浏览器页面。
type TabMeta struct {
	ID    string // 数据源内的稳定键（如 CDP targetId）
	Title string
	URL   string
}

// StatInfo 是数据源视角的文件信息：内容长度与修改时间。
// 节点的其余属性（名字、权限、目录性）由树按骨架约定填充。
type StatInfo struct {
	Size    int64
	ModTime time.Time
}

// Provider 提供 tab 实体：树负责路径骨架与 id，操作时把数据源内的
// 稳定键（tabID）透传给它。
//
// 读取是查询（读时现取），操作是命令：导航类方法等到页面 load 完成
// 才返回（完成语义），失败经 error 上抛、由树转为可读响应。
type Provider interface {
	// TabsList 枚举当前全部页面，实现应当无状态现查。
	TabsList(ctx context.Context) ([]TabMeta, error)

	// ContentType 返回页面 MIME 类型（document.contentType）。
	ContentType(ctx context.Context, tabID string) (string, error)

	// Meta 返回页面实时元数据（title / 当前 url），供实时读与注册表保鲜。
	Meta(ctx context.Context, tabID string) (TabMeta, error)

	// StatContent 返回页面内容文件的信息。size 必须真实——NFS 客户端
	// 在 READ 前先 GETATTR，按 size 决定是否短路；size=0 会让客户端
	// 认为文件为空。
	StatContent(ctx context.Context, tabID string) (StatInfo, error)

	// ReadContent 读取页面内容：HTML 取 outerHTML，其余类型（text/plain、
	// JSON、view-source）取渲染文本，绕开浏览器的展示包装。
	ReadContent(ctx context.Context, tabID string) ([]byte, error)

	// ReadScreenshot 截取当前视口，返回 PNG 字节。
	ReadScreenshot(ctx context.Context, tabID string) ([]byte, error)

	// OpenTab 打开新页面，返回数据源内新页面的稳定键。
	OpenTab(ctx context.Context, url string) (string, error)

	// Navigate / Reload / GoBack / GoForward 触发即回：命令受理即返回，
	// 页面加载在后台进行，就绪与否由调用方自行查询（如 evaljs）。
	Navigate(ctx context.Context, tabID, url string) error
	Reload(ctx context.Context, tabID string) error
	GoBack(ctx context.Context, tabID string) error
	GoForward(ctx context.Context, tabID string) error

	// Activate 把页面窗口置前台（有桌面副作用）。
	Activate(ctx context.Context, tabID string) error

	// CloseTab 关闭页面。
	CloseTab(ctx context.Context, tabID string) error

	// ViewSource 以源码视图（view-source:）打开该页面当前 URL（实时
	// 取 href，注册表可能陈旧），返回数据源内新页面的稳定键。
	ViewSource(ctx context.Context, tabID string) (string, error)

	// Eval 在页面内求值 JS 表达式，表达式可返回 Promise（落定后取值）。
	Eval(ctx context.Context, tabID, expr string) (any, error)
}
