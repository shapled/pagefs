package helper

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

// ErrCacheMiss 表示键在缓存中不存在。
var ErrCacheMiss = errors.New("helper: cache entry missing")

// Keyed 是缓存元素的自描述键约束：缓存据此在锁内定位元素，
// 调用方不持索引跨锁使用。
type Keyed[K comparable] interface {
	Key() K
}

// SliceCache 是切片缓存：不知道元素是什么、数据从哪来，只负责存储、
// 快照与实时优先策略——fetch 在 timeout 内成功则更新缓存，超时或失败
// 回退缓存值。
type SliceCache[T Keyed[K], K comparable] struct {
	mu sync.Mutex
	s  []T
}

func NewSliceCache[K comparable, T Keyed[K]]() *SliceCache[T, K] {
	return &SliceCache[T, K]{}
}

// Refresh 整片更新：fetch 收到旧快照（自行决定如何合并），在 timeout
// 内成功则以返回值替换缓存；超时或失败沿用现缓存。返回生效后的切片。
func (c *SliceCache[T, K]) Refresh(timeout time.Duration, fetch func(ctx context.Context, old []T) ([]T, error)) []T {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if next, err := fetch(ctx, c.Snapshot()); err == nil {
		c.mu.Lock()
		c.s = next
		c.mu.Unlock()
		return next
	}
	return c.Snapshot()
}

// RefreshKey 单项更新：按 key 锁内定位（不持索引跨锁），fetch 收到定位
// 到的旧元素、返回合并后的新元素。返回值彼此独立：值 = 实时值或缓存
// 回退值；fetchErr = 获取过程错误（nil 即实时值）；err 仅 ErrCacheMiss
// （键不存在，含 fetch 期间被并发刷新移除）。
func (c *SliceCache[T, K]) RefreshKey(key K, timeout time.Duration, fetch func(ctx context.Context, old T) (T, error)) (T, error, error) {
	c.mu.Lock()
	old, j := c.findLocked(key)
	c.mu.Unlock()
	if j < 0 {
		var zero T
		return zero, nil, ErrCacheMiss
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	v, fetchErr := fetch(ctx, old)

	// fetch 期间切片可能已被并发 list 事件重排：按 key 重定位再写
	c.mu.Lock()
	defer c.mu.Unlock()
	j = c.indexLocked(key)
	if j < 0 {
		var zero T
		return zero, fetchErr, ErrCacheMiss
	}
	if fetchErr != nil {
		log.Printf("helper: RefreshKey fetch failed, fallback to cache: %v", fetchErr)
		return c.s[j], fetchErr, nil
	}
	c.s[j] = v
	return v, nil, nil
}

// Replace 以给定切片替换缓存内容（供上层组合实现段替换）。
func (c *SliceCache[T, K]) Replace(s []T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s = s
}

// Contains 报告键是否在缓存中。
func (c *SliceCache[T, K]) Contains(key K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.indexLocked(key) >= 0
}

// Snapshot 返回缓存的副本。
func (c *SliceCache[T, K]) Snapshot() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]T(nil), c.s...)
}

func (c *SliceCache[T, K]) findLocked(key K) (T, int) {
	for i, s := range c.s {
		if s.Key() == key {
			return s, i
		}
	}
	var zero T
	return zero, -1
}

func (c *SliceCache[T, K]) indexLocked(key K) int {
	for i, s := range c.s {
		if s.Key() == key {
			return i
		}
	}
	return -1
}
