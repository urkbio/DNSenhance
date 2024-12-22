package statspkg

import (
	"container/list"
	"dnsenhance/internal/cache"
	"encoding/json"
	"fmt"
	"os"
	"sort"
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

type TimeSeriesData struct {
	Timestamp   time.Time `json:"timestamp"`
	Total       int64     `json:"total"`
	Success     int64     `json:"success"`
	CacheHits   int64     `json:"cache_hits"`
	Failed      int64     `json:"failed"`
	Blocked     int64     `json:"blocked"`
	CN          int64     `json:"cn"`
	Foreign     int64     `json:"foreign"`
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
	logs            *list.List
	Cache           *cache.DNSCache
	qpsCounter      *list.List
	persistentPath  string
	DNSLatency      *SimpleHistogram
	timeSeriesData  []TimeSeriesData
}

type SimpleHistogram struct {
	values []float64
	mu     sync.Mutex
}

type LatencyStats struct {
	Average float64
	P95     float64
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

func NewStats(cache *cache.DNSCache) *Stats {
	s := &Stats{
		StartTime:      time.Now(),
		Cache:          cache,
		logs:           list.New(),
		qpsCounter:     list.New(),
		persistentPath: "data/stats.json",
		DNSLatency:     NewSimpleHistogram(),
		timeSeriesData: make([]TimeSeriesData, 0),
	}

	// 每秒更新QPS
	go func() {
		ticker := time.NewTicker(time.Second)
		for range ticker.C {
			s.updateQPS()
		}
	}()

	// 启动时间序列数据记录
	s.StartTimeSeriesRecording()

	return s
}

func (s *Stats) AddLog(domain, queryType, status, qType, result string) {
	s.Lock()
	defer s.Unlock()

	log := QueryLog{
		Time:   time.Now(),
		Domain: domain,
		Type:   queryType,
		Status: status,
		QType:  qType,
		Result: result,
	}

	s.logs.PushBack(log)
}

func (s *Stats) GetLogs() []QueryLog {
	s.RLock()
	defer s.RUnlock()

	var logs []QueryLog
	for e := s.logs.Back(); e != nil; e = e.Prev() {
		logs = append(logs, e.Value.(QueryLog))
	}

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

func (s *Stats) GetTopDomains(n int) map[string]int64 {
	result := make(map[string]int64)
	s.DomainStats.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(int64)
		return true
	})
	return getTopN(result, n)
}

func (s *Stats) GetBlockedDomains(n int) map[string]int64 {
	result := make(map[string]int64)
	s.BlockedStats.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(int64)
		return true
	})
	return getTopN(result, n)
}

func getTopN(m map[string]int64, n int) map[string]int64 {
	if len(m) == 0 {
		return make(map[string]int64)
	}

	type kv struct {
		Key   string
		Value int64
	}
	var ss []kv
	for k, v := range m {
		ss = append(ss, kv{k, v})
	}

	sort.Slice(ss, func(i, j int) bool {
		return ss[i].Value > ss[j].Value
	})

	result := make(map[string]int64)
	for i := 0; i < n && i < len(ss); i++ {
		result[ss[i].Key] = ss[i].Value
	}
	return result
}

func (s *Stats) IncrementDomainCount(domain string) {
	if domain == "" {
		return
	}
	value, _ := s.DomainStats.LoadOrStore(domain, int64(0))
	s.DomainStats.Store(domain, value.(int64)+1)
}

func (s *Stats) IncrementBlockedCount(domain string) {
	if domain == "" {
		return
	}
	value, _ := s.BlockedStats.LoadOrStore(domain, int64(0))
	s.BlockedStats.Store(domain, value.(int64)+1)
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

	// 更新QPS
	s.qpsCounter.PushBack(struct {
		time  time.Time
		count int64
	}{time.Now(), 1})
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
	s.Lock()
	defer s.Unlock()

	now := time.Now()
	var count int64
	var validCount int64

	for e := s.qpsCounter.Front(); e != nil; e = e.Next() {
		data := e.Value.(struct {
			time  time.Time
			count int64
		})
		if now.Sub(data.time) <= time.Second*60 {
			count += data.count
			validCount++
		}
	}

	if validCount > 0 {
		s.CurrentQPS = count / validCount
		if s.CurrentQPS > s.PeakQPS {
			s.PeakQPS = s.CurrentQPS
		}
	} else {
		s.CurrentQPS = 0
	}
}

func (s *Stats) RecordTimeSeriesData() {
	s.Lock()
	defer s.Unlock()

	// 创建新的时间序列数据点
	dataPoint := TimeSeriesData{
		Timestamp:   time.Now(),
		Total:      s.TotalQueries,
		Success:    s.TotalQueries - s.FailedQueries - s.BlockedQueries,
		CacheHits:  s.CacheHits,
		Failed:     s.FailedQueries,
		Blocked:    s.BlockedQueries,
		CN:         s.CNQueries,
		Foreign:    s.ForeignQueries,
	}

	fmt.Printf("Recording time series data point: %+v\n", dataPoint)

	// 添加新数据点
	s.timeSeriesData = append(s.timeSeriesData, dataPoint)

	// 只保留最近24小时的数据（每分钟一个数据点，所以是1440个点）
	if len(s.timeSeriesData) > 1440 {
		s.timeSeriesData = s.timeSeriesData[len(s.timeSeriesData)-1440:]
	}
}

func (s *Stats) GetTimeSeriesData() []TimeSeriesData {
	s.RLock()
	defer s.RUnlock()

	// 如果没有数据，立即记录一个点
	if len(s.timeSeriesData) == 0 {
		s.RUnlock()
		s.RecordTimeSeriesData()
		s.RLock()
	}

	// 创建一个副本以避免并发访问问题
	dataCopy := make([]TimeSeriesData, len(s.timeSeriesData))
	copy(dataCopy, s.timeSeriesData)

	return dataCopy
}

func (s *Stats) StartTimeSeriesRecording() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			s.RecordTimeSeriesData()
		}
	}()
}

func (s *Stats) GetTotalQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.TotalQueries
}

func (s *Stats) GetAllTimeQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.AllTimeQueries
}

func (s *Stats) GetCacheHits() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.CacheHits
}

func (s *Stats) GetCNQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.CNQueries
}

func (s *Stats) GetForeignQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.ForeignQueries
}

func (s *Stats) GetFailedQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.FailedQueries
}

func (s *Stats) GetBlockedQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.BlockedQueries
}

func (s *Stats) GetLastHourQueries() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.LastHourQueries
}

func (s *Stats) GetCurrentQPS() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.CurrentQPS
}

func (s *Stats) GetPeakQPS() int64 {
	s.RLock()
	defer s.RUnlock()
	return s.PeakQPS
}

func (s *Stats) GetCacheHitRate() float64 {
	s.RLock()
	defer s.RUnlock()
	if s.TotalQueries == 0 {
		return 0
	}
	return float64(s.CacheHits) / float64(s.TotalQueries) * 100
}

func (s *Stats) GetLatencyStats() LatencyStats {
	s.RLock()
	defer s.RUnlock()
	return LatencyStats{
		Average: s.DNSLatency.Average(),
		P95:     s.DNSLatency.Percentile(0.95),
	}
}
