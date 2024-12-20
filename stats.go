package main

import (
	"container/ring"
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
	qpsCounter      *ring.Ring       // 用于计算QPS的计数器
	DNSLatency      *SimpleHistogram // 添加这一行
}

func NewStats() *Stats {
	return &Stats{
		logs:       ring.New(1000), // 最近1000条查询记录
		qpsCounter: ring.New(60),   // 最近60秒的查询数
		StartTime:  time.Now(),
		DNSLatency: NewSimpleHistogram(), // 初始化 DNSLatency
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
