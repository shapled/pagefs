// Package cdp 把 Chrome DevTools Protocol 暴露的页面数据实现为 pagefs 的
// tab 数据源。它只懂页面与页面内容，不懂路径骨架与 pagefs id——那些是
// 树的全局约定。
package cdp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/shapled/pagefs/internal/provider"
)

const (
	domExpr = "document.documentElement.outerHTML"
)

// Provider 实现 tree.Provider。pull 型：stat/read 时现取并缓存——
// go-nfs 的 READ 依赖 Stat 的 size 判定 EOF，GETATTR 必须给出真实 size。
type Provider struct {
	cli *Client
	mu  sync.Mutex
}

func NewProvider(cli *Client) *Provider {
	return &Provider{cli: cli}
}

// Tabs 枚举当前全部页面；顺带清理已关闭页面的内容缓存。
func (p *Provider) TabsList(ctx context.Context) ([]provider.TabMeta, error) {
	tgts, err := p.cli.Targets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.TabMeta, 0, len(tgts))
	alive := make(map[string]bool, len(tgts))
	for _, tg := range tgts {
		alive[tg.ID] = true
		out = append(out, provider.TabMeta{ID: tg.ID, Title: tg.Title, URL: tg.URL})
	}
	return out, nil
}

// StatContent 返回页面内容文件的信息。
func (p *Provider) StatContent(ctx context.Context, tabID string) (provider.StatInfo, error) {
	data, err := p.ReadContent(ctx, tabID)
	if err != nil {
		return provider.StatInfo{}, err
	}
	return provider.StatInfo{Size: int64(len(data)), ModTime: time.Now()}, nil
}

// ReadContent 读取当前页面内容的 DOM 序列化。
func (p *Provider) ReadContent(ctx context.Context, tabID string) ([]byte, error) {
	v, err := p.cli.Evaluate(ctx, tabID, domExpr, false)
	if err != nil {
		return nil, err
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("pagefs/cdp: unexpected evaluate result type %T", v)
	}
	return []byte(s), nil
}
