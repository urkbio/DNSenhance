package statspkg

import (
	"container/ring"
	"sort"
	"sync"
	"time"

	"dnsenhance/internal/cache"
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
	cache           *cache.DNSCache
	sync.RWMutex    `json:"-"`
	logs            *ring.Ring
	qpsCounter      *ring.Ring
	DNSLatency      *SimpleHistogram
	ResponseTimes   *ring.Ring
	ErrorTypes      sync.Map
	UpstreamLatency map[string]*SimpleHistogram
	DomainStats     sync.Map
	BlockedStats    sync.Map
	UpstreamUsage   map[string]int64
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

// ... 其他方法的实现
