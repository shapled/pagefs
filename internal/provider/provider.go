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
// 读取行为按语义命名：ReadDOM / ReadRaw 是内容入口，它们的差异是
// pagefs 对外承诺的一部分——
//   - ReadDOM：当前活 DOM（页面此刻的形态，含插件注入的标记）；
//   - ReadRaw：初始源码（浏览器最初收到的响应体）。
//
// 两者对同一页面通常不同（SPA/动态页面差异巨大）。
type Provider interface {
	// TabsList 枚举当前全部页面，实现应当无状态现查。
	TabsList(ctx context.Context) ([]TabMeta, error)

	// StatContent 返回页面内容文件的信息。size 必须真实——NFS 客户端
	// 在 READ 前先 GETATTR，按 size 决定是否短路；size=0 会让客户端
	// 认为文件为空。
	StatContent(ctx context.Context, tabID string) (StatInfo, error)

	// ReadContent 读取当前页面内容的 DOM 序列化。内容形态由浏览器
	// 决定：HTML 页面是标记文本，JSON/文本页面被包装在渲染视图里
	// （按 content type 区分取法是后续改进点）。
	ReadContent(ctx context.Context, tabID string) ([]byte, error)
}
