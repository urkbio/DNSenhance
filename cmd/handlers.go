package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dnsenhance/internal/statspkg"
)

func statsHandler(stats *statspkg.Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data := map[string]interface{}{
			"totalQueries":   stats.TotalQueries,
			"allTimeQueries": stats.AllTimeQueries,
			"cacheHits":      stats.CacheHits,
			"cnQueries":      stats.CNQueries,
			"foreignQueries": stats.ForeignQueries,
			"failedQueries":  stats.FailedQueries,
			"blockedQueries": stats.BlockedQueries,
			"currentQPS":     stats.CurrentQPS,
			"peakQPS":        stats.PeakQPS,
			"uptime":         formatUptime(time.Since(stats.StartTime)),
			"startTime":      stats.StartTime.Format("2006-01-02 15:04:05"),
			"hitRate":        calculateHitRate(stats),
			"topDomains":     getTopDomains(stats, 50),
			"topBlocked":     getTopBlocked(stats, 50),
			"dns_latency": map[string]float64{
				"avg": stats.DNSLatency.Average(),
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
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

func getTopDomains(stats *statspkg.Stats, limit int) []map[string]interface{} {
	return stats.GetTopDomains(false, limit)
}

func getTopBlocked(stats *statspkg.Stats, limit int) []map[string]interface{} {
	return stats.GetTopDomains(true, limit)
}
