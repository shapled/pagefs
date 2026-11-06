// Package cdp 是一个面向 pagefs 需求的极薄 CDP 客户端：列出 targets、
// 页面内求值、页面级导航/截图，以及浏览器级的建/关/激活。
package cdp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// Target 是 /json/list 里 type=page 的条目。
type Target struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	Type     string `json:"type"`
	Debugger string `json:"webSocketDebuggerUrl"`
}

const browserKey = "@browser" // conns 里浏览器级连接的键：Target 域命令走它

// Client 连接一个 CDP endpoint（如 http://127.0.0.1:9222）。
type Client struct {
	HTTP  string // http endpoint，用于发现 targets
	mu    sync.Mutex
	conns map[string]*conn // target.ID（或 browserKey）-> 已建立的 ws 连接
}

func NewClient(httpEndpoint string) *Client {
	return &Client{HTTP: httpEndpoint, conns: make(map[string]*conn)}
}

// Targets 返回当前所有 page 类型的 targets。
func (c *Client) Targets(ctx context.Context) ([]Target, error) {
	var all []Target
	if err := c.getJSON(ctx, "/json/list", &all); err != nil {
		return nil, err
	}
	var pages []Target
	for _, t := range all {
		if t.Type == "page" {
			pages = append(pages, t)
		}
	}
	return pages, nil
}

// Evaluate 在指定页面执行 JS，返回结果值（returnByValue）。
// awaitPromise 为 true 时等待表达式返回的 Promise 落定。
func (c *Client) Evaluate(ctx context.Context, targetID, expr string, awaitPromise bool) (any, error) {
	var v struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		Exception *json.RawMessage `json:"exceptionDetails"`
	}
	if err := c.call(ctx, targetID, "Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
		"awaitPromise":  awaitPromise,
	}, &v); err != nil {
		return nil, err
	}
	if v.Exception != nil {
		return nil, fmt.Errorf("cdp: evaluate failed: %s", string(*v.Exception))
	}
	return v.Result.Value, nil
}

// CreateTarget 打开新页面，返回 targetId。
func (c *Client) CreateTarget(ctx context.Context, url string) (string, error) {
	var out struct {
		ID string `json:"targetId"`
	}
	if err := c.call(ctx, browserKey, "Target.createTarget", map[string]any{"url": url}, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (c *Client) CloseTarget(ctx context.Context, targetID string) error {
	if err := c.call(ctx, browserKey, "Target.closeTarget", map[string]any{"targetId": targetID}, nil); err != nil {
		return err
	}
	c.dropConn(targetID) // 页面已关，连接作废
	return nil
}

func (c *Client) ActivateTarget(ctx context.Context, targetID string) error {
	return c.call(ctx, browserKey, "Target.activateTarget", map[string]any{"targetId": targetID}, nil)
}

// Navigate 触发页面级导航；返回即受理，加载完成由调用方等待。
// 页面级命令（而非 location.href）：view-source:/chrome:// 等特权
// scheme 页面脚本跳不过去，必须走浏览器侧。
func (c *Client) Navigate(ctx context.Context, targetID, url string) error {
	return c.call(ctx, targetID, "Page.navigate", map[string]any{"url": url}, nil)
}

func (c *Client) Reload(ctx context.Context, targetID string) error {
	return c.call(ctx, targetID, "Page.reload", map[string]any{"ignoreCache": false}, nil)
}

// CaptureScreenshot 截取当前视口，返回 PNG 字节。
func (c *Client) CaptureScreenshot(ctx context.Context, targetID string) ([]byte, error) {
	var out struct {
		Data string `json:"data"`
	}
	if err := c.call(ctx, targetID, "Page.captureScreenshot", map[string]any{"format": "png"}, &out); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(out.Data)
}

// Close 关闭全部 ws 连接。
func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cn := range c.conns {
		cn.close()
	}
	c.conns = make(map[string]*conn)
}

// ---- http 与 ws 连接 ----

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.HTTP+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cdp: get %s: %w", path, err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("cdp: decode %s: %w", path, err)
	}
	return nil
}

// browserConn 建立浏览器级 ws 连接（/json/version 给出其端点）。
func (c *Client) browserConn(ctx context.Context) (*conn, error) {
	c.mu.Lock()
	if cn, ok := c.conns[browserKey]; ok {
		c.mu.Unlock()
		return cn, nil
	}
	c.mu.Unlock()

	var info struct {
		Debugger string `json:"webSocketDebuggerUrl"`
	}
	if err := c.getJSON(ctx, "/json/version", &info); err != nil {
		return nil, err
	}
	if info.Debugger == "" {
		return nil, errors.New("cdp: no browser debugger url")
	}
	ws, _, err := websocket.DefaultDialer.DialContext(ctx, info.Debugger, nil)
	if err != nil {
		return nil, fmt.Errorf("cdp: dial browser: %w", err)
	}
	cn := &conn{ws: ws, pending: make(map[int]chan rpcResult)}
	go cn.readLoop()
	c.mu.Lock()
	c.conns[browserKey] = cn
	c.mu.Unlock()
	return cn, nil
}

// ---- ws 连接与 RPC 关联 ----

type conn struct {
	ws      *websocket.Conn
	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResult
	closed  bool
}

type rpcResult struct {
	val json.RawMessage
	err error
}

func (c *Client) dial(ctx context.Context, targetID string) (*conn, error) {
	c.mu.Lock()
	if cn, ok := c.conns[targetID]; ok {
		c.mu.Unlock()
		return cn, nil
	}
	c.mu.Unlock()

	tgts, err := c.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var url string
	for _, t := range tgts {
		if t.ID == targetID {
			url = t.Debugger
		}
	}
	if url == "" {
		return nil, errTargetGone
	}
	ws, _, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cdp: dial %s: %w", targetID, err)
	}
	cn := &conn{ws: ws, pending: make(map[int]chan rpcResult)}
	go cn.readLoop()
	c.mu.Lock()
	c.conns[targetID] = cn
	c.mu.Unlock()
	return cn, nil
}

var errTargetGone = errors.New("cdp: target not found")

func (cn *conn) readLoop() {
	for {
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := cn.ws.ReadJSON(&msg); err != nil {
			cn.mu.Lock()
			cn.closed = true
			for id, ch := range cn.pending {
				ch <- rpcResult{err: fmt.Errorf("cdp: connection lost: %w", err)}
				delete(cn.pending, id)
			}
			cn.mu.Unlock()
			return
		}
		cn.mu.Lock()
		ch, ok := cn.pending[msg.ID]
		delete(cn.pending, msg.ID)
		cn.mu.Unlock()
		if !ok {
			continue
		}
		if msg.Error != nil {
			ch <- rpcResult{err: errors.New(msg.Error.Message)}
		} else {
			ch <- rpcResult{val: msg.Result}
		}
	}
}

func (cn *conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	cn.mu.Lock()
	if cn.closed {
		cn.mu.Unlock()
		return nil, errors.New("cdp: connection closed")
	}
	cn.nextID++
	id := cn.nextID
	ch := make(chan rpcResult, 1)
	cn.pending[id] = ch
	cn.mu.Unlock()

	req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := cn.ws.WriteMessage(websocket.TextMessage, req); err != nil {
		return nil, fmt.Errorf("cdp: send %s: %w", method, err)
	}
	select {
	case r := <-ch:
		return r.val, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (cn *conn) close() {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	if !cn.closed {
		cn.closed = true
		cn.ws.Close()
	}
}

func (c *Client) call(ctx context.Context, key, method string, params, out any) error {
	var cn *conn
	var err error
	if key == browserKey {
		cn, err = c.browserConn(ctx)
	} else {
		cn, err = c.dial(ctx, key)
	}
	if err != nil {
		return err
	}
	raw, err := cn.call(ctx, method, params)
	if err != nil {
		c.dropConn(key) // 连接级失败：丢弃，下次重连
		return err
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *Client) dropConn(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cn, ok := c.conns[key]; ok {
		delete(c.conns, key)
		cn.close()
	}
}
