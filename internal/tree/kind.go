package tree

import "errors"

type kind uint8

const (
	kDir     kind = iota // 静态目录
	kTabs                // tab 集合目录：静态控制项 + tab-<id> 动态实体
	kTabDir              // 单个 tab 的目录
	kRender              // 树渲染的集合数据文件
	kMeta                // 元数据文件：title/url 取注册表，contentType 取数据源
	kContent             // 页面内容
	kShot                // 截图
	kCmdRoot             // 集合级命令
	kCmdTab              // tab 级命令
	kHelp                // 树内自述文件（静态文本）
)

func (k kind) isDir() bool { return k == kDir || k == kTabs || k == kTabDir }
func (k kind) isCmd() bool { return k == kCmdRoot || k == kCmdTab }

// arg 是 kMeta 的取数来源 / 命令的参数形态。
type subkind string

const (
	skTitle       subkind = "title"
	skURL         subkind = "url"
	skContentType subkind = "contentType"
	skExpr        subkind = "expr"
	skNone        subkind = "none"
)

var (
	ErrUnknownSubKind = errors.New("unknown subkind")
)
