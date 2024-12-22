package cache

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// CacheEntry 表示缓存中的一个条目
type CacheEntry struct {
	Msg      *dns.Msg    `json:"-"`      // DNS消息不需要序列化
	MsgBytes []byte      `json:"msg"`     // 序列化后的DNS消息
	ExpireAt time.Time   `json:"expire"`  // 过期时间
	IsStale  bool        `json:"-"`       // 标记是否为过期缓存
	LastHit  time.Time   `json:"last_hit"` // 最后一次命中时间
	HitCount int         `json:"hit_count"` // 命中次数（每小时重置）
}

// DNSCache 实现基于内存的DNS缓存
type DNSCache struct {
	sync.RWMutex
	cache map[string]*CacheEntry
	TTL   time.Duration
	Stats struct {
		Hits   int64
		Misses int64
	}
	persistPath string // 持久化文件路径
	OnPreUpdate func(domain string) (*dns.Msg, error) // 预更新回调函数
}

// New 创建一个新的DNS缓存
func New(ttl time.Duration) *DNSCache {
	cache := &DNSCache{
		cache:       make(map[string]*CacheEntry),
		TTL:        ttl,
		persistPath: filepath.Join("data", "dns_cache.json"),
	}
	
	// 确保数据目录存在
	os.MkdirAll(filepath.Dir(cache.persistPath), 0755)
	
	// 尝试加载持久化的缓存数据
	cache.loadFromDisk()
	
	// 启动定期清理和持久化
	go cache.periodicMaintenance()
	
	return cache
}

// loadFromDisk 从磁盘加载缓存数据
func (c *DNSCache) loadFromDisk() error {
	data, err := ioutil.ReadFile(c.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在是正常的
		}
		return fmt.Errorf("读取缓存文件失败: %v", err)
	}

	var persistedCache map[string]*CacheEntry
	if err := json.Unmarshal(data, &persistedCache); err != nil {
		return fmt.Errorf("解析缓存文件失败: %v", err)
	}

	c.Lock()
	defer c.Unlock()

	now := time.Now()
	for key, entry := range persistedCache {
		// 跳过已过期的条目
		if now.After(entry.ExpireAt) {
			continue
		}

		// 反序列化DNS消息
		msg := new(dns.Msg)
		if err := msg.Unpack(entry.MsgBytes); err != nil {
			continue
		}

		entry.Msg = msg
		c.cache[key] = entry
	}

	return nil
}

// saveToDisk 将缓存数据保存到磁盘
func (c *DNSCache) saveToDisk() error {
	c.RLock()
	defer c.RUnlock()

	// 创建要持久化的数据
	persistCache := make(map[string]*CacheEntry, len(c.cache))
	for key, entry := range c.cache {
		// 序列化DNS消息
		msgBytes, err := entry.Msg.Pack()
		if err != nil {
			continue
		}

		persistCache[key] = &CacheEntry{
			MsgBytes: msgBytes,
			ExpireAt: entry.ExpireAt,
			LastHit:  entry.LastHit,
			HitCount: entry.HitCount,
		}
	}

	// 序列化并写入文件
	data, err := json.Marshal(persistCache)
	if err != nil {
		return fmt.Errorf("序列化缓存数据失败: %v", err)
	}

	if err := ioutil.WriteFile(c.persistPath, data, 0644); err != nil {
		return fmt.Errorf("写入缓存文件失败: %v", err)
	}

	return nil
}

// periodicMaintenance 定期维护任务
func (c *DNSCache) periodicMaintenance() {
	cleanTicker := time.NewTicker(5 * time.Minute)
	saveTicker := time.NewTicker(1 * time.Minute)
	updateTicker := time.NewTicker(1 * time.Hour)

	for {
		select {
		case <-cleanTicker.C:
			c.Clean()
		case <-saveTicker.C:
			c.saveToDisk()
		case <-updateTicker.C:
			c.resetHitCounts()
			c.preUpdateHotDomains()
		}
	}
}

// Get 从缓存中获取DNS响应
func (c *DNSCache) Get(key string) (*dns.Msg, error) {
	c.RLock()
	entry, exists := c.cache[key]
	c.RUnlock()

	if !exists {
		c.Stats.Misses++
		return nil, fmt.Errorf("缓存未命中")
	}

	now := time.Now()
	// 更新访问统计
	c.Lock()
	entry.LastHit = now
	entry.HitCount++
	c.Unlock()

	if now.After(entry.ExpireAt) {
		// 如果在5分钟内过期，仍然可以使用
		if now.Sub(entry.ExpireAt) <= 5*time.Minute {
			c.Stats.Hits++
			return entry.Msg.Copy(), nil
		}
		// 标记为过期缓存
		entry.IsStale = true
		c.Stats.Misses++
		return entry.Msg.Copy(), fmt.Errorf("使用过期缓存")
	}

	c.Stats.Hits++
	return entry.Msg.Copy(), nil
}

// GetWithStale 从缓存中获取DNS响应，包括过期的缓存
func (c *DNSCache) GetWithStale(key string) (*dns.Msg, bool, error) {
	c.RLock()
	entry, exists := c.cache[key]
	c.RUnlock()

	if !exists {
		c.Stats.Misses++
		return nil, false, fmt.Errorf("缓存未命中")
	}

	now := time.Now()
	// 更新访问统计
	c.Lock()
	entry.LastHit = now
	entry.HitCount++
	c.Unlock()

	isStale := now.After(entry.ExpireAt)
	// 如果在5分钟内过期，不认为是过期
	if isStale && now.Sub(entry.ExpireAt) <= 5*time.Minute {
		isStale = false
	}

	if isStale {
		c.Stats.Misses++
	} else {
		c.Stats.Hits++
	}

	return entry.Msg.Copy(), isStale, nil
}

// Set 将DNS响应存入缓存
func (c *DNSCache) Set(key string, msg *dns.Msg) error {
	// 从DNS消息中获取最小TTL
	minTTL := c.TTL
	for _, rr := range msg.Answer {
		if rr.Header().Ttl > 0 && time.Duration(rr.Header().Ttl)*time.Second < minTTL {
			minTTL = time.Duration(rr.Header().Ttl) * time.Second
		}
	}

	// 使用最小TTL，但不超过配置的最大TTL
	if minTTL > c.TTL {
		minTTL = c.TTL
	}

	entry := &CacheEntry{
		Msg:      msg.Copy(),
		ExpireAt: time.Now().Add(minTTL),
		LastHit:  time.Now(),
		HitCount: 1,
		IsStale:  false,
	}

	c.Lock()
	c.cache[key] = entry
	c.Unlock()

	return nil
}

// Clean 清理过期的缓存条目
func (c *DNSCache) Clean() error {
	now := time.Now()
	c.Lock()
	// 保留最近1小时内过期的缓存
	staleRetention := 1 * time.Hour
	for key, entry := range c.cache {
		// 只有超过保留期的缓存才会被删除
		if now.After(entry.ExpireAt.Add(staleRetention)) {
			delete(c.cache, key)
		}
	}
	c.Unlock()
	return nil
}

// GetStats 获取缓存统计信息
func (c *DNSCache) GetStats() (hits, misses int64) {
	return c.Stats.Hits, c.Stats.Misses
}

// resetHitCounts 重置所有域名的访问计数
func (c *DNSCache) resetHitCounts() {
	c.Lock()
	defer c.Unlock()
	
	for _, entry := range c.cache {
		entry.HitCount = 0
	}
}

// preUpdateHotDomains 预更新热门域名的缓存
func (c *DNSCache) preUpdateHotDomains() {
	if c.OnPreUpdate == nil {
		return
	}

	c.RLock()
	hotDomains := make([]string, 0)
	for domain, entry := range c.cache {
		// 如果每小时查询次数超过20次，认为是热门域名
		if entry.HitCount >= 20 {
			hotDomains = append(hotDomains, domain)
		}
	}
	c.RUnlock()

	// 异步更新热门域名的缓存
	for _, domain := range hotDomains {
		go func(d string) {
			if msg, err := c.OnPreUpdate(d); err == nil && msg != nil {
				c.Set(d, msg)
			}
		}(domain)
	}
}
