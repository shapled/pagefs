package helper

import (
	"context"
	"sync"
	"time"
)

// MultiSliceCache 是多段切片缓存：每段一个 SliceCache，适配多数据源
// 场景——段即数据源。同一把大锁覆盖全部操作：Refresh 全量 /
// RefreshSlice 单段 / RefreshKey 单点互斥，fetch 也在锁内执行（刷新
// 期间其余缓存操作串行等待）。段锁在段方法内部，锁序恒为
// multi.mu → seg.mu，无死锁环。
type MultiSliceCache[T Keyed[K], K comparable] struct {
	mu     sync.Mutex
	slices []*SliceCache[T, K]
}

// NewMultiSliceCache 建立含 n 段的缓存。
func NewMultiSliceCache[K comparable, T Keyed[K]](n int) *MultiSliceCache[T, K] {
	c := &MultiSliceCache[T, K]{slices: make([]*SliceCache[T, K], n)}
	for i := range c.slices {
		c.slices[i] = NewSliceCache[K, T]()
	}
	return c
}

// Refresh 全量刷新：fetch 收到按段组织的旧快照，返回按段组织的新切片
// （长度须与段数一致）；timeout 内成功则逐段替换，超时或失败沿用现缓存。
// 返回扁平化快照。
func (c *MultiSliceCache[T, K]) Refresh(timeout time.Duration, fetch func(ctx context.Context, old [][]T) ([][]T, error)) []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if next, err := fetch(ctx, c.segmentsLocked()); err == nil && len(next) == len(c.slices) {
		for i, sc := range c.slices {
			sc.Replace(next[i])
		}
	}
	return c.snapshotLocked()
}

// RefreshSlice 单段刷新：fetch 收到该段旧快照返回新段；成功则替换该段。
// 返回该段生效后的切片，段号越界返回 nil。
func (c *MultiSliceCache[T, K]) RefreshSlice(seg int, timeout time.Duration, fetch func(ctx context.Context, old []T) ([]T, error)) []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seg < 0 || seg >= len(c.slices) {
		return nil
	}
	return c.slices[seg].Refresh(timeout, fetch)
}

// RefreshKey 单点更新：按 key 选段后委托段内 SliceCache——定位与重
// 定位都在段锁内进行，调用方不持索引跨锁。
func (c *MultiSliceCache[T, K]) RefreshKey(key K, timeout time.Duration, fetch func(ctx context.Context, old T) (T, error)) (T, error, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sc := range c.slices {
		if sc.Contains(key) {
			return sc.RefreshKey(key, timeout, fetch)
		}
	}
	var zero T
	return zero, nil, ErrCacheMiss
}

// Segments 返回按段组织的旧快照。
func (c *MultiSliceCache[T, K]) Segments() [][]T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.segmentsLocked()
}

// Snapshot 返回扁平化的全量快照（段序拼接）。
func (c *MultiSliceCache[T, K]) Snapshot() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *MultiSliceCache[T, K]) segmentsLocked() [][]T {
	out := make([][]T, len(c.slices))
	for i, sc := range c.slices {
		out[i] = sc.Snapshot()
	}
	return out
}

func (c *MultiSliceCache[T, K]) snapshotLocked() []T {
	out := []T{}
	for _, sc := range c.slices {
		out = append(out, sc.Snapshot()...)
	}
	return out
}
