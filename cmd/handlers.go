package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dnsenhance/internal/statspkg"
)

func handleStats(stats *statspkg.Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		latencyStats := stats.GetLatencyStats()
		
		response := struct {
			TotalQueries    int64                  `json:"total_queries"`
			AllTimeQueries  int64                  `json:"all_time_queries"`
			CacheHits       int64                  `json:"cache_hits"`
			CNQueries       int64                  `json:"cn_queries"`
			ForeignQueries  int64                  `json:"foreign_queries"`
			FailedQueries   int64                  `json:"failed_queries"`
			BlockedQueries  int64                  `json:"blocked_queries"`
			LastHourQueries int64                  `json:"last_hour_queries"`
			CurrentQPS      int64                  `json:"current_qps"`
			PeakQPS         int64                  `json:"peak_qps"`
			CacheHitRate    float64                `json:"cache_hit_rate"`
			TopDomains      map[string]int64       `json:"top_domains"`
			BlockedDomains  map[string]int64       `json:"blocked_domains"`
			AvgLatency      float64                `json:"avg_latency"`
			P95Latency      float64                `json:"p95_latency"`
			TimeSeriesData  []statspkg.TimeSeriesData `json:"time_series_data"`
		}{
			TotalQueries:    stats.GetTotalQueries(),
			AllTimeQueries:  stats.GetAllTimeQueries(),
			CacheHits:       stats.GetCacheHits(),
			CNQueries:       stats.GetCNQueries(),
			ForeignQueries:  stats.GetForeignQueries(),
			FailedQueries:   stats.GetFailedQueries(),
			BlockedQueries:  stats.GetBlockedQueries(),
			LastHourQueries: stats.GetLastHourQueries(),
			CurrentQPS:      stats.GetCurrentQPS(),
			PeakQPS:         stats.GetPeakQPS(),
			CacheHitRate:    stats.GetCacheHitRate(),
			TopDomains:      getTopDomains(stats, 10),
			BlockedDomains:  getBlockedDomains(stats, 10),
			AvgLatency:      latencyStats.Average,
			P95Latency:      latencyStats.P95,
			TimeSeriesData:  stats.GetTimeSeriesData(),
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}
}

func getTopDomains(stats *statspkg.Stats, limit int) map[string]int64 {
	return stats.GetTopDomains(limit)
}

func getBlockedDomains(stats *statspkg.Stats, limit int) map[string]int64 {
	return stats.GetBlockedDomains(limit)
}

func formatUptime(d time.Duration) string {
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

func calculateHitRate(stats *statspkg.Stats) float64 {
	if stats.TotalQueries == 0 {
		return 0
	}
	return float64(stats.CacheHits) / float64(stats.TotalQueries) * 100
}
