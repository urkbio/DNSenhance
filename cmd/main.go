package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"html/template"

	"dnsenhance/internal/cache"
	"dnsenhance/internal/dnspkg"
	"dnsenhance/internal/geo"
	"dnsenhance/internal/statspkg"
	"dnsenhance/internal/tray"

	"github.com/miekg/dns"
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type Histogram interface {
	Observe(float64)
}

type SimpleHistogram struct {
	values []float64
	mu     sync.Mutex
}

func (h *SimpleHistogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.values = append(h.values, value)
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
	return sum / float64(len(h.values))
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

type DNSServer struct {
	cache     *cache.DNSCache
	geoFilter *geo.Filter
	upstream  map[string][]dnspkg.Resolver
	stats     *statspkg.Stats
	blocker   *dnspkg.Blocker
	running   int32
	sync.Mutex
}

type Config struct {
	CacheTTL   int                 `json:"cache_ttl"`
	CacheFile  string              `json:"cache_file"`
	Upstream   map[string][]string `json:"upstream"`
	DomainFile string              `json:"domain_file"`
	Block      struct {
		Enabled bool   `json:"enabled"`
		File    string `json:"file"`
	} `json:"block"`
}

func loadConfig(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var config Config
	err = json.Unmarshal(data, &config)
	if err != nil {
		return nil, err
	}

	return &config, nil
}

func (s *DNSServer) selectResolver(resolvers []dnspkg.Resolver) dnspkg.Resolver {
	// 简单随机选择
	return resolvers[rand.Intn(len(resolvers))]
}

// 使用 sync.Pool 来复用 DNS 消息对象
var msgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}

func (s *DNSServer) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	if atomic.LoadInt32(&s.running) == 0 {
		return
	}

	if len(r.Question) == 0 {
		return
	}

	question := r.Question[0]
	var resolvers []dnspkg.Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		resolvers = s.upstream["cn"]
		atomic.AddInt64(&s.stats.CNQueries, 1)
	} else {
		resolvers = s.upstream["foreign"]
		atomic.AddInt64(&s.stats.ForeignQueries, 1)
	}

	s.handleDNSRequestWithResolvers(w, r, resolvers)
}

func (s *DNSServer) handleDNSRequestWithResolvers(w dns.ResponseWriter, r *dns.Msg, resolvers []dnspkg.Resolver) {
	if len(r.Question) == 0 {
		return
	}

	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		s.stats.DNSLatency.Observe(duration)
	}()

	// 更新总查询计数
	s.stats.IncrementTotal()

	question := r.Question[0]
	domain := strings.TrimSuffix(question.Name, ".")
	qtype := dns.TypeToString[question.Qtype]

	// 记录域名访问
	s.stats.RecordDomainAccess(domain, false)

	// 检查是否命中缓存
	cached, isStale, err := s.cache.GetWithStale(question.Name)
	if err == nil && cached != nil {
		w.WriteMsg(cached)
		if !isStale {
			s.stats.IncrementCacheHits()
			s.stats.AddLog(domain, "Unknown", "Cache Hit", qtype, formatDNSResult(cached))
		} else {
			s.stats.AddLog(domain, "Unknown", "Stale Cache Hit", qtype, formatDNSResult(cached))
			// 异步更新过期缓存
			go s.updateStaleCache(question.Name, r, resolvers)
		}
		return
	}

	// 选择解析器
	resolver := s.selectResolver(resolvers)
	msg, err := resolver.Resolve(r)

	if err != nil {
		s.stats.IncrementFailed()
		s.stats.AddLog(domain, "Unknown", "Failed", qtype, err.Error())
		
		// 如果解析失败，尝试使用过期缓存
		if cached != nil {
			w.WriteMsg(cached)
			s.stats.AddLog(domain, "Unknown", "Using Stale Cache", qtype, formatDNSResult(cached))
			return
		}
		
		// 构造错误响应
		m := new(dns.Msg)
		m.SetRcode(r, dns.RcodeServerFailure)
		w.WriteMsg(m)
		return
	}

	// 确定查询类型（国内/国外）
	queryType := "Foreign"
	if s.geoFilter.IsDomainCN(domain) {
		queryType = "CN"
		s.stats.IncrementCN()
	} else {
		s.stats.IncrementForeign()
	}

	// 添加到缓存
	_ = s.cache.Set(question.Name, msg)
	
	// 记录日志
	s.stats.AddLog(domain, queryType, "Success", qtype, formatDNSResult(msg))

	// 发送响应
	w.WriteMsg(msg)
}

// updateStaleCache 异步更新过期缓存
func (s *DNSServer) updateStaleCache(domain string, r *dns.Msg, resolvers []dnspkg.Resolver) {
	resolver := s.selectResolver(resolvers)
	msg, err := resolver.Resolve(r)
	if err == nil && msg != nil {
		_ = s.cache.Set(domain, msg)
	}
}

func formatDNSResult(msg *dns.Msg) string {
	if msg == nil || len(msg.Answer) == 0 {
		return "no answer"
	}

	var results []string
	for _, ans := range msg.Answer {
		switch rr := ans.(type) {
		case *dns.A:
			results = append(results, rr.A.String())
		case *dns.AAAA:
			results = append(results, rr.AAAA.String())
		case *dns.CNAME:
			results = append(results, rr.Target)
		case *dns.MX:
			results = append(results, rr.Mx)
		case *dns.TXT:
			results = append(results, strings.Join(rr.Txt, " "))
		default:
			results = append(results, ans.String())
		}
	}
	return strings.Join(results, ", ")
}

func (s *DNSServer) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("DNS处理发生错误: %v\n堆栈: %s", rec, debug.Stack())
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeServerFailure)
			w.WriteMsg(m)
		}
	}()

	s.handleDNSRequest(w, r)
}

func init() {
	rand.Seed(time.Now().UnixNano())
	// 创建必要的目录
	dirs := []string{
		"configs",
		"data",
		"logs",
		"web/templates",
		"web/static",
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Printf("创建目录失败 %s: %v", dir, err)
		}
	}
}

func setupHandlers(mux *http.ServeMux, stats *statspkg.Stats) {
    // 静态文件服务
    fileServer := http.FileServer(http.Dir("web/static"))
    mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

    // 注册路由
    mux.HandleFunc("/api/stats", func(w http.ResponseWriter, r *http.Request) {
        latencyStats := stats.GetLatencyStats()
        timeSeriesData := stats.GetTimeSeriesData()
        
        log.Printf("Time series data count: %d", len(timeSeriesData))
        if len(timeSeriesData) > 0 {
            log.Printf("Latest data point: %+v", timeSeriesData[len(timeSeriesData)-1])
        }
        
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
            TopDomains:      stats.GetTopDomains(10),
            BlockedDomains:  stats.GetBlockedDomains(10),
            AvgLatency:      latencyStats.Average,
            P95Latency:      latencyStats.P95,
            TimeSeriesData:  timeSeriesData,
        }

        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(response); err != nil {
            log.Printf("Error encoding response: %v", err)
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
    })
    
    mux.HandleFunc("/api/logs", handleLogs(stats))
    // 处理主页
    mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
        if r.URL.Path != "/" {
            http.NotFound(w, r)
            return
        }
        tmpl, err := template.ParseFiles("web/templates/index.html")
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        tmpl.Execute(w, nil)
    })

    // 处理日志页面
    mux.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
        tmpl, err := template.ParseFiles("web/templates/logs.html")
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        tmpl.Execute(w, nil)
    })
}

func main() {
	// 设置全局panic处理
	defer func() {
		if r := recover(); r != nil {
			errMsg := fmt.Sprintf("程序发生严重错误:\n%v\n\n堆栈信息:\n%s", r, debug.Stack())
			log.Printf(errMsg)
			ShowErrorDialog("程序崩溃", errMsg)
			os.Exit(1)
		}
	}()

	// 创建日志目录
	if err := os.MkdirAll("logs", 0755); err != nil {
		fmt.Printf("创建日志目录失败: %v\n", err)
		return
	}

	logFile, err := os.OpenFile("logs/dns.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("无法创建日志文件:", err)
		return
	}
	defer logFile.Close()

	if stat, err := logFile.Stat(); err == nil && stat.Size() == 0 {
		logFile.Write([]byte{0xEF, 0xBB, 0xBF})
	}

	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// 加载配置
	config, err := loadConfig("configs/config.json")
	if err != nil {
		log.Printf("加载配置失败: %v", err)
		ShowErrorDialog("配置错误", fmt.Sprintf("加载配置失败:\n%v", err))
		return
	}

	fmt.Println("[1/4] 初始化DNS缓存...")
	// 创建内存缓存
	dnsCache := cache.New(time.Duration(config.CacheTTL) * time.Second)

	fmt.Println("[2/4] 初始化统计模块...")
	stats := statspkg.NewStats(dnsCache)
	if err := stats.LoadStats(); err != nil {
		log.Printf("加载统计数据失败: %v", err)
	}
	stats.StartTimeSeriesRecording()
	fmt.Println("✓ 统计模块初始化完成")

	fmt.Println("[3/4] 初始化GeoIP模块...")
	geoData, err := ioutil.ReadFile(filepath.Join("configs", config.DomainFile))
	if err != nil {
		log.Fatalf("读取域名分流配置失败: %v", err)
	}
	geoFilter, err := geo.NewFilter(geoData)
	if err != nil {
		log.Fatalf("初始化GeoIP过滤器失败: %v", err)
	}
	fmt.Println("✓ GeoIP模块初始化完成")

	fmt.Println("[4/4] 初始化DNS服务器...")
	// 创建DNS服务器
	server := &DNSServer{
		cache:     dnsCache,
		geoFilter: geoFilter,
		upstream:  make(map[string][]dnspkg.Resolver),
		stats:     stats,
		running:   1,
	}

	// 初始化上游解析器
	for category, endpoints := range config.Upstream {
		resolvers := make([]dnspkg.Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = dnspkg.NewDOHResolver(endpoint)
		}
		server.upstream[category] = resolvers
	}

	// 设置缓存预更新回调
	dnsCache.OnPreUpdate = func(domain string) (*dns.Msg, error) {
		// 构造DNS查询
		m := new(dns.Msg)
		m.SetQuestion(domain, dns.TypeA)
		
		// 根据域名选择解析器
		var resolvers []dnspkg.Resolver
		if server.geoFilter.IsDomainCN(domain) {
			resolvers = server.upstream["cn"]
		} else {
			resolvers = server.upstream["foreign"]
		}
		
		// 选择解析器并执行查询
		resolver := server.selectResolver(resolvers)
		return resolver.Resolve(m)
	}

	fmt.Println("✓ DNS缓存初始化完成")

	fmt.Println("[4/4] 启动服务...")
	// 启动DNS服务器
	go func() {
		dns.HandleFunc(".", server.ServeDNS)
		dnsServer := &dns.Server{Addr: ":53", Net: "udp"}
		log.Printf("DNS服务器启动在 :53")
		if err := dnsServer.ListenAndServe(); err != nil {
			log.Fatalf("启动DNS服务器失败: %v", err)
		}
	}()

	// 启动统计服务器
	mux := http.NewServeMux()
	setupHandlers(mux, stats)
	statsServer := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	go func() {
		if err := statsServer.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("HTTP服务器错误: %v", err)
		}
	}()

	fmt.Println("✓ 所有服务启动完成")
	fmt.Println("\n现在你可以：")
	fmt.Println("1. 访问 http://localhost:8080 查看Web界面")
	fmt.Println("2. 将设备DNS服务器设置为本机IP")
	fmt.Println("\n按Ctrl+C停止服务")

	// 启动系统托盘
	go func() {
		tray.InitSysTray()
	}()

	// 创建信号通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// 创建退出通道
	exitChan := tray.GetExitChan()

	// 主循环
	select {
	case <-exitChan:
		log.Println("收到退出信号")
	case sig := <-sigChan:
		log.Printf("收到信号: %v", sig)
	}

	// 优雅关闭
	fmt.Println("\n正在关闭服务...")
	atomic.StoreInt32(&server.running, 0)

	// 使用 WaitGroup 等待所有服务关闭
	var wg sync.WaitGroup

	// 关闭HTTP服务器
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := statsServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP服务器关闭错误: %v", err)
		}
	}()

	// 等待所有服务关闭或超时
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("所有服务已关闭")
	case <-time.After(10 * time.Second):
		log.Println("服务关闭超时")
	}

	log.Println("程序退出")
	os.Exit(0)
}

func handleLogs(stats *statspkg.Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		logs := stats.GetLogs()

		// 为每条日志添加额外的格式化信息
		type LogEntry struct {
			Time      string `json:"time"`
			Domain    string `json:"domain"`
			Type      string `json:"type"`
			Status    string `json:"status"`
			QType     string `json:"qtype"`
			Result    string `json:"result"`
			Latency   string `json:"latency"`
			Source    string `json:"source"`
			TimeAgo   string `json:"time_ago"`
		}

		formattedLogs := make([]LogEntry, 0, len(logs))
		for _, log := range logs {
			timeAgo := time.Since(log.Time)
			timeAgoStr := ""
			switch {
			case timeAgo < time.Minute:
				timeAgoStr = fmt.Sprintf("%d秒前", int(timeAgo.Seconds()))
			case timeAgo < time.Hour:
				timeAgoStr = fmt.Sprintf("%d分钟前", int(timeAgo.Minutes()))
			case timeAgo < 24*time.Hour:
				timeAgoStr = fmt.Sprintf("%d小时前", int(timeAgo.Hours()))
			default:
				timeAgoStr = fmt.Sprintf("%d天前", int(timeAgo.Hours()/24))
			}

			entry := LogEntry{
				Time:      log.Time.Format("2006-01-02 15:04:05"),
				Domain:    log.Domain,
				Type:      log.Type,
				Status:    log.Status,
				QType:     log.QType,
				Result:    log.Result,
				TimeAgo:   timeAgoStr,
			}
			formattedLogs = append(formattedLogs, entry)
		}

		json.NewEncoder(w).Encode(formattedLogs)
	}
}

func getTimeSeries(stats *statspkg.Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats.RLock()
		defer stats.RUnlock()

		timeSeriesData := stats.GetTimeSeriesData()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(timeSeriesData)
	}
}

func formatDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
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

// 添加错误对话框函数
func ShowErrorDialog(title, message string) {
	cmd := exec.Command("cmd", "/c", "echo", message, "&", "pause")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Run()
}
