// Package manager 实现数据缓存层。
//
// 缓存策略:
//   - 内存缓存: 适用于 session、频繁查询的客户状态、chart 目录
//   - Redis: 生产环境推荐，支持多实例共享缓存
//   - TTL: 不同数据类型设置不同过期时间
package manager

import (
	"sync"
	"time"
)

// Cache 提供内存缓存实现。
type Cache struct {
	mu       sync.RWMutex
	items    map[string]*cacheItem
	maxSize  int
	stopCh   chan struct{} // 发送信号停止 cleanupLoop
}

type cacheItem struct {
	value    any
	expireAt time.Time
}

// NewCache 创建指定大小的缓存。
func NewCache(maxSize int) *Cache {
	c := &Cache{
		items:   make(map[string]*cacheItem, maxSize),
		maxSize: maxSize,
		stopCh:  make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Close 停止后台清理 goroutine。
func (c *Cache) Close() {
	close(c.stopCh)
}

// Get 获取缓存值，不存在或已过期返回 false。
func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	item, ok := c.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().After(item.expireAt) {
		return nil, false
	}
	return item.value, true
}

// Set 写入缓存值，带 TTL。
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 超出容量时淘汰 25% 旧项
	if len(c.items) >= c.maxSize {
		c.evict()
	}

	c.items[key] = &cacheItem{
		value:    value,
		expireAt: time.Now().Add(ttl),
	}
}

// Delete 删除缓存项。
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// DeleteByPrefix 删除所有匹配前缀的缓存项。
func (c *Cache) DeleteByPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.items, k)
		}
	}
}

func (c *Cache) evict() {
	count := len(c.items) / 4
	for k := range c.items {
		if count <= 0 {
			break
		}
		delete(c.items, k)
		count--
	}
}

func (c *Cache) cleanupLoop() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-t.C:
		c.mu.Lock()
		now := time.Now()
		for k, item := range c.items {
			if now.After(item.expireAt) {
				delete(c.items, k)
			}
		}
		c.mu.Unlock()
		}
	}
}

// =============================================================================
// 缓存键常量
// =============================================================================

const (
	CacheKeyCustomerStatus     = "customer:status:"      // + customerID
	CacheKeyChartVersions      = "chart:versions:"        // + customerID + ":" + chartName
	CacheKeySystemOverview     = "system:overview"
	CacheKeySession            = "session:"               // + token
	CacheKeyCustomerDeployments = "customer:deployments:" // + customerID

	// TTL 定义
	TTLCustomerStatus     = 2 * time.Minute
	TTLChartVersions      = 10 * time.Minute
	TTLSystemOverview     = 1 * time.Minute
	TTLSession            = 24 * time.Hour
)

// =============================================================================
// 缓存辅助方法
// =============================================================================

// GetCustomerStatus 从缓存获取客户状态。
func (c *Cache) GetCustomerStatus(customerID string) (*CustomerStatus, bool) {
	val, ok := c.Get(CacheKeyCustomerStatus + customerID)
	if !ok {
		return nil, false
	}
	status, ok := val.(*CustomerStatus)
	return status, ok
}

// SetCustomerStatus 缓存客户状态。
func (c *Cache) SetCustomerStatus(customerID string, status *CustomerStatus) {
	c.Set(CacheKeyCustomerStatus+customerID, status, TTLCustomerStatus)
}

// InvalidateCustomerStatus 使客户状态缓存失效。
func (c *Cache) InvalidateCustomerStatus(customerID string) {
	c.Delete(CacheKeyCustomerStatus + customerID)
}

// GetSystemOverview 从缓存获取系统概览。
func (c *Cache) GetSystemOverview() (*SystemOverview, bool) {
	val, ok := c.Get(CacheKeySystemOverview)
	if !ok {
		return nil, false
	}
	overview, ok := val.(*SystemOverview)
	return overview, ok
}

// SetSystemOverview 缓存系统概览。
func (c *Cache) SetSystemOverview(overview *SystemOverview) {
	c.Set(CacheKeySystemOverview, overview, TTLSystemOverview)
}

// InvalidateSystemOverview 使系统概览缓存失效。
func (c *Cache) InvalidateSystemOverview() {
	c.Delete(CacheKeySystemOverview)
}
