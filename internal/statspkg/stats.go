package statspkg

import (
	"container/ring"
	"dnsenhance/internal/cache"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type QueryLog struct {
	Time   time.Time
	Domain string
	Type   string // "CN" 或 "Foreign"
	Status string // "Cache Hit", "Success", "Failed"
	QType  string // "A", "AAAA", "CNAME" 等
	Result string // IP地址或其他查询结果
}

type PersistentStats struct {
	TotalQueries   int64            `json:"total_queries"`
	AllTimeQueries int64            `json:"all_time_queries"`
	CacheHits      int64            `json:"cache_hits"`
	CNQueries      int64            `json:"cn_queries"`
	ForeignQueries int64            `json:"foreign_queries"`
	FailedQueries  int64            `json:"failed_queries"`
	BlockedQueries int64            `json:"blocked_queries"`
	PeakQPS        int64            `json:"peak_qps"`
	DomainStats    map[string]int64 `json:"domain_stats"`
	BlockedStats   map[string]int64 `json:"blocked_stats"`
}

type Stats struct {
	sync.RWMutex
	TotalQueries    int64
	AllTimeQueries  int64
	CacheHits       int64
	CNQueries       int64
	ForeignQueries  int64
	FailedQueries   int64
	BlockedQueries  int64
	LastHourQueries int64
	PeakQPS         int64
	CurrentQPS      int64
	StartTime       time.Time
	DomainStats     sync.Map
	BlockedStats    sync.Map
	DNSLatency      *SimpleHistogram
	Cache           *cache.DNSCache
	logs            *ring.Ring
	qpsCounter      *ring.Ring
	persistentPath  string
}

type SimpleHistogram struct {
	values []float64
	mu     sync.Mutex
}

func NewSimpleHistogram() *SimpleHistogram {
	return &SimpleHistogram{
		values: make([]float64, 0),
	}
}

func (h *SimpleHistogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value)
	if len(h.values) > 1000 {
		h.values = h.values[1:]
	}
}

func (h *SimpleHistogram) Average() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.values) == 0 {
		return 0
	}

	var sum float64
	for _, v := range h.values {
		sum += v
	}
	return (sum / float64(len(h.values))) * 1000 // 转换为毫秒
}

func (h *SimpleHistogram) Percentile(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.values) == 0 {
		return 0
	}

	sorted := make([]float64, len(h.values))
	copy(sorted, h.values)
	sort.Float64s(sorted)

	index := int(float64(len(sorted)-1) * p)
	return sorted[index] * 1000 // 转换为毫秒
}

func New() *Stats {
	return &Stats{
		StartTime:      time.Now(),
		DNSLatency:     NewSimpleHistogram(),
		logs:           ring.New(1000),
		qpsCounter:     ring.New(60),
		persistentPath: "data/stats.json",
	}
}

func (s *Stats) AddLog(domain, queryType, status, qType, result string) {
	s.Lock()
	defer s.Unlock()
	s.logs.Value = QueryLog{
		Time:   time.Now(),
		Domain: domain,
		Type:   queryType,
		Status: status,
		QType:  qType,
		Result: result,
	}
	s.logs = s.logs.Next()
}

func (s *Stats) GetLogs() []QueryLog {
	s.RLock()
	defer s.RUnlock()

	var logs []QueryLog
	s.logs.Do(func(v interface{}) {
		if v != nil {
			logs = append(logs, v.(QueryLog))
		}
	})
	return logs
}

func (s *Stats) LoadStats() error {
	data, err := os.ReadFile(s.persistentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在不算错误
		}
		return fmt.Errorf("读取统计数据失败: %v", err)
	}

	var stats PersistentStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return fmt.Errorf("解析统计数据失败: %v", err)
	}

	s.Lock()
	defer s.Unlock()

	// 恢复基本统计
	s.TotalQueries = stats.TotalQueries
	s.AllTimeQueries = stats.AllTimeQueries
	s.CacheHits = stats.CacheHits
	s.CNQueries = stats.CNQueries
	s.ForeignQueries = stats.ForeignQueries
	s.FailedQueries = stats.FailedQueries
	s.BlockedQueries = stats.BlockedQueries
	s.PeakQPS = stats.PeakQPS

	// 恢复域名统计
	for domain, count := range stats.DomainStats {
		s.DomainStats.Store(domain, count)
	}

	// 恢复拦截统计
	for domain, count := range stats.BlockedStats {
		s.BlockedStats.Store(domain, count)
	}

	return nil
}

func (s *Stats) SaveStats() error {
	s.RLock()
	defer s.RUnlock()

	// 收集域名统计
	domainStats := make(map[string]int64)
	s.DomainStats.Range(func(key, value interface{}) bool {
		domainStats[key.(string)] = value.(int64)
		return true
	})

	// 收集拦截统计
	blockedStats := make(map[string]int64)
	s.BlockedStats.Range(func(key, value interface{}) bool {
		blockedStats[key.(string)] = value.(int64)
		return true
	})

	data := PersistentStats{
		TotalQueries:   s.TotalQueries,
		AllTimeQueries: s.AllTimeQueries,
		CacheHits:      s.CacheHits,
		CNQueries:      s.CNQueries,
		ForeignQueries: s.ForeignQueries,
		FailedQueries:  s.FailedQueries,
		BlockedQueries: s.BlockedQueries,
		PeakQPS:        s.PeakQPS,
		DomainStats:    domainStats,
		BlockedStats:   blockedStats,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化统计数据失败: %v", err)
	}

	if err := os.WriteFile(s.persistentPath, jsonData, 0644); err != nil {
		return fmt.Errorf("保存统计数据失败: %v", err)
	}

	return nil
}

func (s *Stats) GetTopDomains(blocked bool, limit int) []map[string]interface{} {
	s.RLock()
	defer s.RUnlock()

	var items []struct {
		Domain string
		Count  int64
	}

	statsMap := &s.DomainStats
	if blocked {
		statsMap = &s.BlockedStats
	}

	// 收集所有数据
	statsMap.Range(func(key, value interface{}) bool {
		items = append(items, struct {
			Domain string
			Count  int64
		}{
			Domain: key.(string),
			Count:  value.(int64),
		})
		return true
	})

	// 按访问量降序排序
	sort.Slice(items, func(i, j int) bool {
		return items[i].Count > items[j].Count
	})

	// 限制数量
	if len(items) > limit {
		items = items[:limit]
	}

	// 转换为所需格式
	result := make([]map[string]interface{}, len(items))
	for i, item := range items {
		result[i] = map[string]interface{}{
			"domain": strings.TrimSuffix(item.Domain, "."),
			"count":  item.Count,
		}
	}

	return result
}

func (s *Stats) Cleanup() {
	s.Lock()
	defer s.Unlock()

	// 清理过期的统计数据
	s.LastHourQueries = 0
	s.CurrentQPS = 0

	// 清理过期的延迟数据
	s.DNSLatency = NewSimpleHistogram()
}

func (s *Stats) RecordDomainAccess(domain string, blocked bool) {
	if blocked {
		if count, ok := s.BlockedStats.Load(domain); ok {
			s.BlockedStats.Store(domain, count.(int64)+1)
		} else {
			s.BlockedStats.Store(domain, int64(1))
		}
	} else {
		if count, ok := s.DomainStats.Load(domain); ok {
			s.DomainStats.Store(domain, count.(int64)+1)
		} else {
			s.DomainStats.Store(domain, int64(1))
		}
	}
}

func (s *Stats) IncrementBlocked() {
	s.Lock()
	defer s.Unlock()
	s.BlockedQueries++
}

func (s *Stats) IncrementTotal() {
	s.Lock()
	defer s.Unlock()
	s.TotalQueries++
	s.AllTimeQueries++
	s.LastHourQueries++
	s.updateQPS()
}

func (s *Stats) IncrementCacheHits() {
	s.Lock()
	defer s.Unlock()
	s.CacheHits++
}

func (s *Stats) IncrementCN() {
	s.Lock()
	defer s.Unlock()
	s.CNQueries++
}

func (s *Stats) IncrementForeign() {
	s.Lock()
	defer s.Unlock()
	s.ForeignQueries++
}

func (s *Stats) IncrementFailed() {
	s.Lock()
	defer s.Unlock()
	s.FailedQueries++
}

func (s *Stats) updateQPS() {
	now := time.Now()
	s.qpsCounter.Value = struct {
		time  time.Time
		count int64
	}{now, 1}
	s.qpsCounter = s.qpsCounter.Next()

	var count int64
	var validCount int64
	s.qpsCounter.Do(func(v interface{}) {
		if v != nil {
			data := v.(struct {
				time  time.Time
				count int64
			})
			if now.Sub(data.time) <= time.Second*60 {
				count += data.count
				validCount++
			}
		}
	})

	if validCount > 0 {
		currentQPS := count / validCount
		if currentQPS > s.PeakQPS {
			s.PeakQPS = currentQPS
		}
		s.CurrentQPS = currentQPS
	}
}
