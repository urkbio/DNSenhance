package main

import (
	"container/ring"
	"encoding/json"
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

type Stats struct {
	TotalQueries    int64 `json:"total_queries"`
	AllTimeQueries  int64 `json:"all_time_queries"`
	CacheHits       int64 `json:"cache_hits"`
	CNQueries       int64 `json:"cn_queries"`
	ForeignQueries  int64 `json:"foreign_queries"`
	FailedQueries   int64 `json:"failed_queries"`
	BlockedQueries  int64 `json:"blocked_queries"`
	StartTime       time.Time
	LastHourQueries int64
	PeakQPS         int64
	CurrentQPS      int64
	cache           *DNSCache // 添加对缓存的引用
	sync.RWMutex    `json:"-"`
	logs            *ring.Ring
	qpsCounter      *ring.Ring                  // 用于计算QPS的计数器
	DNSLatency      *SimpleHistogram            // 添加这一行
	ResponseTimes   *ring.Ring                  // 响应时间分布
	ErrorTypes      sync.Map                    // 错误类型统计
	UpstreamLatency map[string]*SimpleHistogram // 上游服务器延迟
	DomainStats     sync.Map                    // 记录域名访问次数
	BlockedStats    sync.Map                    // 记录被拦截域名次数
	UpstreamUsage   map[string]int64            // 上游服务器使用次数
	persistentPath  string                      // 统计数据持久化路径
}

func NewStats() *Stats {
	return &Stats{
		logs:       ring.New(1000),
		qpsCounter: ring.New(60),
		StartTime:  time.Now(),
		DNSLatency: NewSimpleHistogram(),
		UpstreamLatency: map[string]*SimpleHistogram{
			"https://223.5.5.5/dns-query":       NewSimpleHistogram(),
			"https://223.6.6.6/dns-query":       NewSimpleHistogram(),
			"https://doh.pub/dns-query":         NewSimpleHistogram(),
			"https://dns.alidns.com/dns-query":  NewSimpleHistogram(),
			"https://9.9.9.11/dns-query":        NewSimpleHistogram(),
			"https://208.67.222.222/dns-query":  NewSimpleHistogram(),
			"https://208.67.220.220/dns-query":  NewSimpleHistogram(),
			"https://149.112.112.112/dns-query": NewSimpleHistogram(),
			"https://101.101.101.101/dns-query": NewSimpleHistogram(),
		},
		UpstreamUsage:  make(map[string]int64),
		persistentPath: "stats.json",
	}
}

func (s *Stats) addLog(domain, queryType, status, qType, result string) {
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

func (s *Stats) getLogs() []QueryLog {
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

func (s *Stats) updateQPS() {
	s.Lock()
	defer s.Unlock()

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

func (s *Stats) incrementTotal() {
	s.Lock()
	s.TotalQueries++
	s.AllTimeQueries++
	s.LastHourQueries++
	s.Unlock()
	s.updateQPS()
}

func (s *Stats) incrementCacheHits() {
	s.Lock()
	s.CacheHits++
	s.Unlock()
}

func (s *Stats) incrementCN() {
	s.Lock()
	s.CNQueries++
	s.Unlock()
}

func (s *Stats) incrementForeign() {
	s.Lock()
	s.ForeignQueries++
	s.Unlock()
}

func (s *Stats) incrementFailed() {
	s.Lock()
	s.FailedQueries++
	s.Unlock()
}

func (s *Stats) incrementBlocked() {
	s.Lock()
	s.BlockedQueries++
	s.Unlock()
}

func (s *Stats) recordError(errType string) {
	if count, ok := s.ErrorTypes.LoadOrStore(errType, int64(1)); ok {
		s.ErrorTypes.Store(errType, count.(int64)+1)
	}
}

func (s *Stats) cleanup() {
	s.Lock()
	defer s.Unlock()

	// 清理过期的响应时间记录
	now := time.Now()
	s.ResponseTimes.Do(func(v interface{}) {
		if v != nil {
			if t, ok := v.(time.Time); ok && now.Sub(t) > 24*time.Hour {
				v = nil
			}
		}
	})

	// 清理错误统计
	s.ErrorTypes.Range(func(key, value interface{}) bool {
		if count, ok := value.(int64); ok && count == 0 {
			s.ErrorTypes.Delete(key)
		}
		return true
	})
}

func (s *Stats) recordDomainAccess(domain string, blocked bool) {
	if blocked {
		if count, ok := s.BlockedStats.LoadOrStore(domain, int64(1)); ok {
			s.BlockedStats.Store(domain, count.(int64)+1)
		}
	} else {
		if count, ok := s.DomainStats.LoadOrStore(domain, int64(1)); ok {
			s.DomainStats.Store(domain, count.(int64)+1)
		}
	}
}

func (s *Stats) getTopDomains(blocked bool, limit int) []map[string]interface{} {
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

	// 限制数量为前50个
	if len(items) > limit {
		items = items[:limit]
	}

	// 转换为所需格式
	result := make([]map[string]interface{}, len(items))
	for i, item := range items {
		result[i] = map[string]interface{}{
			"domain": strings.TrimSuffix(item.Domain, "."), // 移除末尾的点
			"count":  item.Count,
		}
	}

	return result
}

type PersistentStats struct {
	AllTimeQueries int64            `json:"all_time_queries"`
	DomainStats    map[string]int64 `json:"domain_stats"`
	BlockedStats   map[string]int64 `json:"blocked_stats"`
	UpstreamUsage  map[string]int64 `json:"upstream_usage"`
}

func (s *Stats) SaveStats() error {
	s.RLock()
	defer s.RUnlock()

	data := PersistentStats{
		AllTimeQueries: s.AllTimeQueries,
		DomainStats:    s.exportDomainStats(),
		BlockedStats:   s.exportBlockedStats(),
		UpstreamUsage:  s.UpstreamUsage,
	}

	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.persistentPath, jsonData, 0644)
}

func (s *Stats) LoadStats() error {
	data, err := os.ReadFile(s.persistentPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var stats PersistentStats
	if err := json.Unmarshal(data, &stats); err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()

	s.AllTimeQueries = stats.AllTimeQueries

	// 恢复域名统计
	for domain, count := range stats.DomainStats {
		s.DomainStats.Store(domain, count)
	}

	// 恢复拦截统计
	for domain, count := range stats.BlockedStats {
		s.BlockedStats.Store(domain, count)
	}

	// 恢复上游使用统计
	for endpoint, count := range stats.UpstreamUsage {
		s.UpstreamUsage[endpoint] = count
	}

	return nil
}

func (s *Stats) exportDomainStats() map[string]int64 {
	result := make(map[string]int64)
	s.DomainStats.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(int64)
		return true
	})
	return result
}

func (s *Stats) exportBlockedStats() map[string]int64 {
	result := make(map[string]int64)
	s.BlockedStats.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(int64)
		return true
	})
	return result
}

func (s *Stats) importDomainStats(stats map[string]interface{}) {
	for domain, count := range stats {
		s.DomainStats.Store(domain, int64(count.(float64)))
	}
}

func (s *Stats) importBlockedStats(stats map[string]interface{}) {
	for domain, count := range stats {
		s.BlockedStats.Store(domain, int64(count.(float64)))
	}
}

func (s *Stats) getUpstreamStats() []map[string]interface{} {
	var stats []map[string]interface{}
	for endpoint, usage := range s.UpstreamUsage {
		stats = append(stats, map[string]interface{}{
			"endpoint": endpoint,
			"usage":    usage,
		})
	}
	return stats
}
