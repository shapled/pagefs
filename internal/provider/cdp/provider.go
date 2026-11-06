// Package cdp 把 Chrome DevTools Protocol 暴露的页面数据实现为 pagefs 的
// tab 数据源。它只懂页面与页面内容，不懂路径骨架与 pagefs id——那些是
// 树的全局约定。
package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shapled/pagefs/internal/provider"
)

const (
	// 类型与正文分开取：分流依据 ContentType 的真实值，正文按类型
	// 选表达式——HTML 用 outerHTML，其余（text/plain、JSON）用渲染
	// 文本，绕开浏览器的展示包装。
	contentHTMLExpr = "document.documentElement.outerHTML"
	contentTextExpr = "document.body.innerText"
	contentTypeExpr = "document.contentType"
	metaExpr        = `JSON.stringify({t:document.title||'',u:location.href||''})`
)

// Provider 实现 tree.Provider。pull 型：读取现取，命令完成才返回。
type Provider struct {
	cli *Client
	mu  sync.Mutex
}

func NewProvider(cli *Client) *Provider {
	return &Provider{cli: cli}
}

// TabsList 枚举当前全部页面。
func (p *Provider) TabsList(ctx context.Context) ([]provider.TabMeta, error) {
	tgts, err := p.cli.Targets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]provider.TabMeta, 0, len(tgts))
	for _, tg := range tgts {
		out = append(out, provider.TabMeta{ID: tg.ID, Title: tg.Title, URL: tg.URL})
	}
	return out, nil
}

func (p *Provider) ContentType(ctx context.Context, tabID string) (string, error) {
	v, err := p.cli.Evaluate(ctx, tabID, contentTypeExpr, false)
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

// Meta 返回页面实时元数据（title / 当前 url）：一次 evaluate 同取两项。
func (p *Provider) Meta(ctx context.Context, tabID string) (provider.TabMeta, error) {
	v, err := p.cli.Evaluate(ctx, tabID, metaExpr, false)
	if err != nil {
		return provider.TabMeta{}, err
	}
	s, ok := v.(string)
	if !ok {
		return provider.TabMeta{}, fmt.Errorf("cdp: unexpected meta result %T", v)
	}
	var r struct {
		T, U string
	}
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		return provider.TabMeta{}, fmt.Errorf("cdp: decode meta: %w", err)
	}
	return provider.TabMeta{ID: tabID, Title: r.T, URL: r.U}, nil
}

// StatContent 返回页面内容文件的信息：size 走一次真实读取。
func (p *Provider) StatContent(ctx context.Context, tabID string) (provider.StatInfo, error) {
	data, err := p.ReadContent(ctx, tabID)
	if err != nil {
		return provider.StatInfo{}, err
	}
	return provider.StatInfo{Size: int64(len(data)), ModTime: time.Now()}, nil
}

// ReadContent 按页面类型取正文：先求值类型，再选提取表达式。
func (p *Provider) ReadContent(ctx context.Context, tabID string) ([]byte, error) {
	ct, err := p.ContentType(ctx, tabID)
	if err != nil {
		return nil, err
	}
	expr := contentTextExpr
	if strings.HasPrefix(ct, "text/html") {
		expr = contentHTMLExpr
	}
	v, err := p.cli.Evaluate(ctx, tabID, expr, false)
	if err != nil {
		return nil, err
	}
	s, ok := v.(string)
	if !ok {
		return nil, fmt.Errorf("cdp: unexpected content result %T", v)
	}
	return []byte(s), nil
}

func (p *Provider) ReadScreenshot(ctx context.Context, tabID string) ([]byte, error) {
	return p.cli.CaptureScreenshot(ctx, tabID)
}

func (p *Provider) OpenTab(ctx context.Context, url string) (string, error) {
	return p.cli.CreateTarget(ctx, url)
}

// Navigate 触发即回：命令受理即返回，加载在后台进行。
func (p *Provider) Navigate(ctx context.Context, tabID, url string) error {
	return p.cli.Navigate(ctx, tabID, url)
}

func (p *Provider) Reload(ctx context.Context, tabID string) error {
	return p.cli.Reload(ctx, tabID)
}

func (p *Provider) GoBack(ctx context.Context, tabID string) error {
	_, err := p.cli.Evaluate(ctx, tabID, "history.back()", false)
	return err
}

func (p *Provider) GoForward(ctx context.Context, tabID string) error {
	_, err := p.cli.Evaluate(ctx, tabID, "history.forward()", false)
	return err
}

func (p *Provider) Activate(ctx context.Context, tabID string) error {
	return p.cli.ActivateTarget(ctx, tabID)
}

func (p *Provider) CloseTab(ctx context.Context, tabID string) error {
	return p.cli.CloseTarget(ctx, tabID)
}

// ViewSource 以 view-source: 打开页面当前 URL（实时 href：注册表里
// 的 URL 在导航后是陈旧的），返回新页面的稳定键。
func (p *Provider) ViewSource(ctx context.Context, tabID string) (string, error) {
	v, err := p.cli.Evaluate(ctx, tabID, "location.href", false)
	if err != nil {
		return "", err
	}
	href, _ := v.(string)
	if href == "" {
		return "", fmt.Errorf("cdp: no location href")
	}
	return p.cli.CreateTarget(ctx, "view-source:"+href)
}

// Eval 求值 JS 表达式，Promise 落定后取值。
func (p *Provider) Eval(ctx context.Context, tabID, expr string) (any, error) {
	return p.cli.Evaluate(ctx, tabID, expr, true)
}
