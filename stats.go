package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"
	"container/ring"
	"strings"
	"encoding/json"
	"html/template"
)

type QueryLog struct {
	Time     time.Time
	Domain   string
	Type     string // "CN" 或 "Foreign"
	Status   string // "Cache Hit", "Success", "Failed"
	QType    string // "A", "AAAA", "CNAME" 等
	Result   string // IP地址或其他查询结果
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
	sync.RWMutex    `json:"-"`
	logs            *ring.Ring
	qpsCounter      *ring.Ring  // 用于计算QPS的计数器
}

func NewStats() *Stats {
	return &Stats{
		logs:       ring.New(1000),  // 最近1000条查询记录
		qpsCounter: ring.New(60),    // 最近60秒的查询数
		StartTime:  time.Now(),
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

func startStatsServer(stats *Stats, port int) {
	// 静态文件服务
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// API 路由
	http.HandleFunc("/api/stats", handleStats(stats))
	http.HandleFunc("/api/logs", handleLogs(stats))

	// 页面路由
	http.HandleFunc("/", serveFile("static/index.html"))
	http.HandleFunc("/logs", serveFile("static/logs.html"))

	go http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

func serveFile(filename string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filename)
	}
}

func handleStats(stats *Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats.RLock()
		defer stats.RUnlock()

		// 计算缓存命中率
		var hitRate float64
		if stats.TotalQueries > 0 {
			hitRate = float64(stats.CacheHits) / float64(stats.TotalQueries) * 100
		}

		data := map[string]interface{}{
			"currentQPS":      stats.CurrentQPS,
			"peakQPS":         stats.PeakQPS,
			"uptime":          formatDuration(time.Since(stats.StartTime)),
			"startTime":       stats.StartTime.Format("2006-01-02 15:04:05"),
			"hitRate":         hitRate,
			"cacheHits":       stats.CacheHits,
			"totalQueries":    stats.TotalQueries,
			"cnQueries":       stats.CNQueries,
			"foreignQueries":  stats.ForeignQueries,
			"failedQueries":   stats.FailedQueries,
			"blockedQueries":  stats.BlockedQueries,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func handleLogs(stats *Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats.RLock()
		defer stats.RUnlock()

		logs := stats.getLogs()
		var builder strings.Builder

		// 倒序显示日志，最新的在最前面
		for i := len(logs) - 1; i >= 0; i-- {
			log := logs[i]
			if log.Domain == "" {
				continue
			}
			
			builder.WriteString(fmt.Sprintf(`
				<tr class="log-row">
					<td>%s</td>
					<td class="domain">%s</td>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
					<td>%s</td>
				</tr>`,
				log.Time.Format("15:04:05"),
				template.HTMLEscapeString(log.Domain),
				log.Type,
				log.Status,
				log.QType,
				template.HTMLEscapeString(log.Result)))
		}

		// 设置必要的响应头
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		
		w.Write([]byte(builder.String()))
	}
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	
	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
} 