package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
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

	"strconv"

	"net/http/pprof"
	"runtime"

	"github.com/go-redis/redis/v8"
	"github.com/miekg/dns"
)

type Histogram interface {
	Observe(float64)
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
	return sum / float64(len(h.values))
}

func (h *SimpleHistogram) Percentile(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.values) == 0 {
		return 0
	}

	// 创建副本并排序
	sorted := make([]float64, len(h.values))
	copy(sorted, h.values)
	sort.Float64s(sorted)

	// 计算百分位数
	index := int(float64(len(sorted)-1) * p)
	return sorted[index]
}

type DNSServer struct {
	cache     *DNSCache
	geoFilter *GeoFilter
	upstream  map[string][]Resolver
	stats     *Stats
	blocker   *Blocker
	sync.Mutex
}

type DNSCache struct {
	client *redis.Client
	ttl    time.Duration
	stale  time.Duration
	ctx    context.Context
	stats  struct {
		staleHits  int64
		normalHits int64
		misses     int64
	}
}

func NewDNSCache(client *redis.Client, ttl time.Duration) *DNSCache {
	return &DNSCache{
		client: client,
		ttl:    ttl,
		stale:  10 * time.Minute,
		ctx:    context.Background(),
	}
}

func (c *DNSCache) Set(key string, msg *dns.Msg) error {
	log.Printf("设置缓存: %s", key)

	packed, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("打包DNS消息失败: %v", err)
	}

	// 根据 TTL 动态调整缓存时间
	minTTL := uint32(math.MaxUint32)
	for _, rr := range msg.Answer {
		if rr.Header().Ttl < minTTL {
			minTTL = rr.Header().Ttl
		}
	}

	cacheDuration := time.Duration(minTTL) * time.Second
	if cacheDuration > c.ttl {
		cacheDuration = c.ttl
	}

	now := time.Now()
	expireAt := now.Add(cacheDuration)

	pipe := c.client.Pipeline()
	pipe.HSet(c.ctx, "dns:"+key,
		"data", packed,
		"expire_at", expireAt.Unix(),
	)
	pipe.ExpireAt(c.ctx, "dns:"+key, now.Add(cacheDuration+c.stale))

	_, err = pipe.Exec(c.ctx)
	return err
}

func (c *DNSCache) Get(key string) (*dns.Msg, error) {
	// 加本地内存缓存层
	type cacheEntry struct {
		msg      *dns.Msg
		expireAt time.Time
		isStale  bool
	}

	var localCache sync.Map

	// 先查本地缓存
	if entry, ok := localCache.Load(key); ok {
		e := entry.(*cacheEntry)
		if time.Now().Before(e.expireAt) {
			return e.msg.Copy(), nil
		}
	}

	// 再查 Redis
	result, err := c.client.HGetAll(c.ctx, "dns:"+key).Result()
	if err != nil {
		if err == redis.Nil {
			atomic.AddInt64(&c.stats.misses, 1)
			log.Printf("缓存未命中: %s", key)
			return nil, fmt.Errorf("缓存未命中")
		}
		log.Printf("Redis错误: %v", err)
		return nil, err
	}

	data, ok := result["data"]
	if !ok {
		return nil, fmt.Errorf("缓存数据无效")
	}

	expireAtStr, ok := result["expire_at"]
	if !ok {
		return nil, fmt.Errorf("缓存数据无效")
	}

	expireAt, err := strconv.ParseInt(expireAtStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("缓存数据无效")
	}

	isStale := time.Now().Unix() > expireAt

	msg := new(dns.Msg)
	if err := msg.Unpack([]byte(data)); err != nil {
		c.client.Del(c.ctx, "dns:"+key)
		return nil, fmt.Errorf("解析缓存数据失败: %v", err)
	}

	if isStale {
		atomic.AddInt64(&c.stats.staleHits, 1)
		log.Printf("过期缓存命中: %s", key)
		msg.Extra = append(msg.Extra, &dns.TXT{
			Hdr: dns.RR_Header{
				Name:   "stale-cache",
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    0,
			},
			Txt: []string{"stale"},
		})
	} else {
		atomic.AddInt64(&c.stats.normalHits, 1)
		log.Printf("缓存命中: %s", key)
	}

	return msg, nil
}

func (c *DNSCache) SaveToFile(filename string) error {
	return nil
}

func (c *DNSCache) LoadFromFile(filename string) error {
	return nil
}

func (c *DNSCache) Close() error {
	return nil
}

func (c *DNSCache) Clean() error {
	pattern := "dns:*"
	var cursor uint64
	for {
		var keys []string
		var err error
		keys, cursor, err = c.client.Scan(c.ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}

		for _, key := range keys {
			result, err := c.client.HGetAll(c.ctx, key).Result()
			if err != nil {
				continue
			}

			expireAtStr, ok := result["expire_at"]
			if !ok {
				c.client.Del(c.ctx, key)
				continue
			}

			expireAt, err := strconv.ParseInt(expireAtStr, 10, 64)
			if err != nil || time.Now().Unix() > expireAt+int64(c.stale.Seconds()) {
				c.client.Del(c.ctx, key)
			}
		}

		if cursor == 0 {
			break
		}
	}
	return nil
}

type Resolver interface {
	Resolve(request *dns.Msg) (*dns.Msg, error)
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

func (s *DNSServer) selectResolver(resolvers []Resolver) Resolver {
	// 使用加权轮询选择解析器
	var totalWeight float64
	weights := make([]float64, len(resolvers))

	for i, resolver := range resolvers {
		if doh, ok := resolver.(*DOHResolver); ok {
			// 计算权重：基于响应时间的倒数
			latency := s.stats.getAverageLatency(doh.endpoint)
			if latency == 0 {
				latency = 1 // 避免除以零
			}
			weight := 1.0 / latency
			weights[i] = weight
			totalWeight += weight
		}
	}

	// 如果没有权重信息，使用随机选择
	if totalWeight == 0 {
		return resolvers[rand.Intn(len(resolvers))]
	}

	// 根据权重选择解析器
	choice := rand.Float64() * totalWeight
	var sumWeight float64
	for i, weight := range weights {
		sumWeight += weight
		if choice <= sumWeight {
			return resolvers[i]
		}
	}

	// 保险起见，返回最后一个
	return resolvers[len(resolvers)-1]
}

// 使用 sync.Pool 来复用 DNS 消息对象
var msgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}

func (s *DNSServer) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	const maxRetries = 3
	var lastErr error

	for i := 0; i < maxRetries; i++ {
		response, err := s.tryResolve(r)
		if err == nil {
			w.WriteMsg(response)
			return
		}
		lastErr = err
		time.Sleep(time.Duration(i*100) * time.Millisecond) // 退避重试
	}

	log.Printf("解析失败(重试%d次): %v", maxRetries, lastErr)
	// ... 错误处理 ...
}

func (s *DNSServer) handleDNSRequestWithResolvers(w dns.ResponseWriter, r *dns.Msg, resolvers []Resolver) {
	if len(resolvers) == 0 {
		m := new(dns.Msg)
		m.SetReply(r)
		m.Rcode = dns.RcodeServerFailure
		w.WriteMsg(m)
		return
	}

	question := r.Question[0]
	cacheKey := fmt.Sprintf("%s:%d", question.Name, question.Qtype)
	qType := dns.TypeToString[question.Qtype]

	// 选择一个解析器
	resolver := s.selectResolver(resolvers)
	response, err := resolver.Resolve(r)
	if err != nil {
		// 更新失败统计
		if doh, ok := resolver.(*DOHResolver); ok {
			atomic.AddInt32(&doh.failCount, 1)
			doh.lastFail = time.Now()
		}
		// 如果还有其他解析器可用，递归调用
		remaining := make([]Resolver, 0, len(resolvers)-1)
		for _, r := range resolvers {
			if r != resolver {
				remaining = append(remaining, r)
			}
		}
		s.handleDNSRequestWithResolvers(w, r, remaining)
		return
	}

	result := formatDNSResult(response)
	s.stats.addLog(question.Name, "Query", "Success", qType, result)
	log.Printf("解析成功: %s (%s: %s)", question.Name, qType, result)
	s.cache.Set(cacheKey, response)
	w.WriteMsg(response)
}

func formatDNSResult(msg *dns.Msg) string {
	var results []string
	for _, answer := range msg.Answer {
		switch rr := answer.(type) {
		case *dns.A:
			results = append(results, rr.A.String())
		case *dns.AAAA:
			results = append(results, rr.AAAA.String())
		case *dns.CNAME:
			results = append(results, rr.Target)
		case *dns.MX:
			results = append(results, fmt.Sprintf("%d %s", rr.Preference, rr.Mx))
		case *dns.TXT:
			results = append(results, strings.Join(rr.Txt, " "))
		default:
			results = append(results, answer.String())
		}
	}
	if len(results) == 0 {
		return "无结果"
	}
	return strings.Join(results, ", ")
}

func (s *DNSServer) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	s.handleDNSRequest(w, r)
}

func init() {
	// 添加 Redis 目录到 DLL 搜索路径
	exePath, err := os.Executable()
	if err == nil {
		redisDir := filepath.Join(filepath.Dir(exePath), "redis")
		os.Setenv("PATH", redisDir+";"+os.Getenv("PATH"))
	}
}

func main() {
	// 设置全局panic处理
	defer func() {
		if r := recover(); r != nil {
			log.Printf("程序发生严重错误: %v", r)
			// 打印堆栈信息
			debug.PrintStack()
		}
	}()

	logFile, err := os.OpenFile("dns.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
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

	fmt.Println("=== DNS服务启动 ===")
	log.Println("=== DNS服务器启动 ===")

	fmt.Println("[1/5] 加载配置文件...")
	config, err := loadConfig("config.json")
	if err != nil {
		fmt.Println("❌ 配置文件加载失败:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	fmt.Println("✓ 配置文件加载完成")

	fmt.Println("[2/5] 加载域名列表...")
	geoData, err := ioutil.ReadFile(config.DomainFile)
	if err != nil {
		fmt.Printf("❌ 域名列表文 %s 加载失败: %v\n", config.DomainFile, err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}

	geoFilter, err := NewGeoFilter(geoData)
	if err != nil {
		fmt.Println("❌ GeoFilter初始化失败:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	fmt.Println("✓ 域名列表加载完成")

	fmt.Println("[3/5] 初始化域名拦截器...")
	blocker, err := NewBlocker(config.Block.Enabled, config.Block.File)
	if err != nil {
		fmt.Printf("❌ 域名拦截器初始化失败: %v\n", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	if config.Block.Enabled {
		fmt.Println("✓ 域名拦截器初始化完成")
	} else {
		fmt.Println("✓ 域名拦截功能已禁用")
	}

	fmt.Println("[4/5] 启动Redis服务...")
	redisManager, err := NewRedisManager(6379)
	if err != nil {
		fmt.Printf("❌ Redis理器初始化: %v\n", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}

	if err := redisManager.Start(); err != nil {
		fmt.Printf("❌ Redis服务启动失败: %v\n", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	fmt.Println("✓ Redis服务启动完成")

	// 使用 Redis 客户端初始化缓存
	cache := NewDNSCache(redisManager.GetClient(), time.Duration(config.CacheTTL)*time.Second)
	fmt.Printf("✓ DNS缓存初始化成 (TTL: %d秒)\n", config.CacheTTL)

	fmt.Println("测试Redis存...")
	testKey := "test:dns:example.com"
	testValue := []byte("test-data")

	if err := cache.client.Set(cache.ctx, testKey, testValue, time.Minute).Err(); err != nil {
		fmt.Printf("Redis写入测试失败: %v\n", err)
	} else {
		fmt.Println("Redis写入测试成功")

		if val, err := cache.client.Get(cache.ctx, testKey).Result(); err != nil {
			fmt.Printf("Redis读取测试失败: %v\n", err)
		} else if string(val) == string(testValue) {
			fmt.Println("Redis读取测试成功")
		} else {
			fmt.Printf("Redis数据不匹配: 望 %s, 实际 %s\n", testValue, val)
		}
	}

	server := &DNSServer{
		cache:     cache,
		geoFilter: geoFilter,
		upstream:  make(map[string][]Resolver),
		stats: func() *Stats {
			stats := NewStats()
			stats.cache = cache // 确保设置缓���引用
			return stats
		}(),
		blocker: blocker,
	}

	for category, endpoints := range config.Upstream {
		resolvers := make([]Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = NewDOHResolver(endpoint)
		}
		server.upstream[category] = resolvers
	}

	// 尝试加载历史统计数据
	if err := server.stats.LoadStats(); err != nil {
		log.Printf("加载历史统计数据失败: %v", err)
	}

	// 启动健康检查
	server.startHealthCheck()

	fmt.Println("[5/5] 启动Web服务器和DNS服务器...")
	httpServer := startStatsServer(server.stats, cache, 8080)
	fmt.Println("✓ 统计Web服务器启动完成")

	dnsServer := &dns.Server{
		Addr:    ":53",
		Net:     "udp",
		Handler: server,
	}

	// 创建错误通道
	errChan := make(chan error, 1)

	// DNS服务器启动
	go func() {
		if err := dnsServer.ListenAndServe(); err != nil {
			errChan <- fmt.Errorf("DNS服务器错误: %v", err)
		}
	}()

	// HTTP服务器启动
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("HTTP服务器错误: %v", err)
		}
	}()

	// 系统托盘初始化
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("系统托盘发生错误: %v", r)
			}
		}()
		initSysTray()
	}()

	// 启动健康检查
	server.startHealthCheck()

	// 创建信号通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	// 创建退出通道
	exitChan := GetExitChan()

	// 添加定期保存统计数据
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := server.stats.SaveStats(); err != nil {
					log.Printf("保存统计数据失败: %v", err)
				}
			case <-exitChan:
				return
			}
		}
	}()

	// 主循环
	select {
	case <-exitChan:
		log.Println("收到退出信号")
	case <-sigChan:
		log.Println("收到系统信号")
	case err := <-errChan:
		log.Printf("服务发生错误: %v", err)
	}

	// 优雅关闭
	log.Println("开始关闭服务...")

	// 创建一个带超时的context
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 使用WaitGroup等待所有服务关闭
	var wg sync.WaitGroup

	// 关闭HTTP服务器
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP服务器关闭错误: %v", err)
		}
	}()

	// 关闭DNS服务器
	wg.Add(1)
	go func() {
		defer wg.Done()
		dnsServer.Shutdown()
	}()

	// 关闭Redis
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := redisManager.Stop(); err != nil {
			log.Printf("Redis关闭错误: %v", err)
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
		log.Println("所有服务已正常关闭")
	case <-ctx.Done():
		log.Println("服务关闭超时")
	}

	// 保存最终统计数据
	if err := server.stats.SaveStats(); err != nil {
		log.Printf("最终统计数据保存失败: %v", err)
	}

	log.Println("程序退出")
	os.Exit(0)
}

func startStatsServer(stats *Stats, cache *DNSCache, port int) *http.Server {
	// 创建自定义的文件服务器
	fileServer := http.FileServer(http.Dir("static"))

	// 包装处理函数以添加正确的 MIME 类型
	staticHandler := func(w http.ResponseWriter, r *http.Request) {
		// 移除 "/static/" 前缀
		path := strings.TrimPrefix(r.URL.Path, "/static/")

		// 根据文件扩展名设置正确的 Content-Type
		ext := filepath.Ext(path)
		switch ext {
		case ".ttf":
			w.Header().Set("Content-Type", "font/ttf")
		case ".css":
			w.Header().Set("Content-Type", "text/css")
		case ".js":
			w.Header().Set("Content-Type", "application/javascript")
		}

		fileServer.ServeHTTP(w, r)
	}

	// 注册处理函数
	http.Handle("/static/", http.StripPrefix("/static/", http.HandlerFunc(staticHandler)))

	// API 路由
	http.HandleFunc("/api/stats", handleStats(stats))
	http.HandleFunc("/api/logs", handleLogs(stats))

	// 页面路由
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "static/index.html")
			return
		}
	})

	http.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/logs.html")
	})

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: nil,
	}

	return server
}

func handleStats(stats *Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		stats.RLock()
		defer stats.RUnlock()

		log.Printf("Handling stats request from %s", r.RemoteAddr)

		// 获取域名统计
		topDomains := stats.getTopDomains(false, 50)
		topBlocked := stats.getTopDomains(true, 50)

		// 详细的统计信息日志
		log.Printf("Stats summary: Queries=%d, Hits=%d, CN=%d, Foreign=%d, Blocked=%d",
			stats.TotalQueries,
			stats.CacheHits,
			stats.CNQueries,
			stats.ForeignQueries,
			stats.BlockedQueries)

		// 计算缓存命中率
		var hitRate float64
		if stats.TotalQueries > 0 {
			hitRate = float64(stats.CacheHits) / float64(stats.TotalQueries) * 100
		}

		data := map[string]interface{}{
			"all_time_queries": stats.AllTimeQueries,
			"currentQPS":       stats.CurrentQPS,
			"peakQPS":          stats.PeakQPS,
			"uptime":           formatDuration(time.Since(stats.StartTime)),
			"startTime":        stats.StartTime.Format("2006-01-02 15:04:05"),
			"hitRate":          hitRate,
			"cacheHits":        stats.CacheHits,
			"staleHits":        stats.cache.stats.staleHits,
			"normalHits":       stats.cache.stats.normalHits,
			"cacheMisses":      stats.cache.stats.misses,
			"totalQueries":     stats.TotalQueries,
			"cnQueries":        stats.CNQueries,
			"foreignQueries":   stats.ForeignQueries,
			"failedQueries":    stats.FailedQueries,
			"blockedQueries":   stats.BlockedQueries,
			"topDomains":       topDomains,
			"topBlocked":       topBlocked,
			"dns_latency": map[string]float64{
				"avg": stats.DNSLatency.Average(),
				"p50": stats.DNSLatency.Percentile(0.5),
				"p95": stats.DNSLatency.Percentile(0.95),
				"p99": stats.DNSLatency.Percentile(0.99),
			},
			"upstream_stats": stats.getUpstreamStats(),
		}

		// 打印完整的响应数据（仅在调试时使用）
		if jsonData, err := json.MarshalIndent(data, "", "  "); err == nil {
			log.Printf("Full response data: %s", string(jsonData))
		}

		if err := json.NewEncoder(w).Encode(data); err != nil {
			log.Printf("Error encoding response: %v", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
}

func handleLogs(stats *Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

// 异步刷新缓存
func (s *DNSServer) refreshCache(r *dns.Msg, cacheKey string) {
	// 防止并发刷新同一个key
	lockKey := "refresh:" + cacheKey
	ok, err := s.cache.client.SetNX(s.cache.ctx, lockKey, "1", 30*time.Second).Result()
	if err != nil || !ok {
		return // 已经有其他协程在刷新
	}
	defer s.cache.client.Del(s.cache.ctx, lockKey)

	question := r.Question[0]
	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		resolvers = s.upstream["cn"]
	} else {
		resolvers = s.upstream["foreign"]
	}

	resolver := s.selectResolver(resolvers)
	response, err := resolver.Resolve(r)
	if err != nil {
		log.Printf("刷新缓存失败: %s: %v", question.Name, err)
		return
	}

	if err := s.cache.Set(cacheKey, response); err != nil {
		log.Printf("更新缓存失败: %s: %v", question.Name, err)
	} else {
		log.Printf("缓存已刷新: %s", question.Name)
	}
}

func (s *DNSServer) warmupCache(domains []string) {
	for _, domain := range domains {
		msg := new(dns.Msg)
		msg.SetQuestion(dns.Fqdn(domain), dns.TypeA)

		go func(m *dns.Msg) {
			if _, err := s.handleDNSRequestForCache(m); err != nil {
				log.Printf("缓存预热失败 %s: %v", domain, err)
			}
		}(msg)
	}
}

func (s *DNSServer) handleDNSRequestForCache(r *dns.Msg) (*dns.Msg, error) {
	question := r.Question[0]
	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		resolvers = s.upstream["cn"]
	} else {
		resolvers = s.upstream["foreign"]
	}

	resolver := s.selectResolver(resolvers)
	return resolver.Resolve(r)
}

// 添加结构体日志
type LogEntry struct {
	Time      time.Time     `json:"time"`
	Level     string        `json:"level"`
	Message   string        `json:"message"`
	Domain    string        `json:"domain,omitempty"`
	QueryType string        `json:"query_type,omitempty"`
	Latency   time.Duration `json:"latency,omitempty"`
	Error     string        `json:"error,omitempty"`
}

func (s *DNSServer) logQuery(entry LogEntry) {
	data, _ := json.Marshal(entry)
	log.Println(string(data))
}

func validateConfig(config *Config) error {
	if config.CacheTTL <= 0 {
		return fmt.Errorf("缓存TTL必须大于0")
	}

	if len(config.Upstream) == 0 {
		return fmt.Errorf("必须配置至少一个上游DNS服务器")
	}

	// 验证上游服务器配置
	for category, servers := range config.Upstream {
		if len(servers) == 0 {
			return fmt.Errorf("类别 %s 必须配置至少一个服务器", category)
		}
	}

	return nil
}

func (s *DNSServer) cleanup() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		// 清理过期缓存
		if err := s.cache.Clean(); err != nil {
			log.Printf("缓存清理失败: %v", err)
		}

		// 清理统计数据
		s.stats.cleanup()

		// 重置计数器
		atomic.StoreInt64(&s.stats.LastHourQueries, 0)
	}
}

func (s *DNSServer) reloadConfig() error {
	config, err := loadConfig("config.json")
	if err != nil {
		return err
	}

	s.Lock()
	defer s.Unlock()

	// 更新配置
	s.upstream = make(map[string][]Resolver)
	for category, endpoints := range config.Upstream {
		resolvers := make([]Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = NewDOHResolver(endpoint)
		}
		s.upstream[category] = resolvers
	}

	return nil
}

func initDebug(mux *http.ServeMux) {
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// 添加运行时统计
	mux.HandleFunc("/debug/stats", func(w http.ResponseWriter, r *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		json.NewEncoder(w).Encode(m)
	})
}

func (s *DNSServer) healthCheck() error {
	// 检查Redis连接
	if err := s.cache.client.Ping(s.cache.ctx).Err(); err != nil {
		return fmt.Errorf("Redis连接异常: %v", err)
	}

	// 检查上游服务器
	for category, resolvers := range s.upstream {
		for _, resolver := range resolvers {
			if err := s.checkResolver(resolver); err != nil {
				log.Printf("%s解析器异常: %v", category, err)
			}
		}
	}

	return nil
}

func (s *DNSServer) handleQuery(r *dns.Msg) (*dns.Msg, error) {
	question := r.Question[0]
	qType := dns.TypeToString[question.Qtype]
	cacheKey := question.Name + ":" + qType

	// 录域名访问
	s.stats.recordDomainAccess(question.Name, false)

	// 检查是否需要拦截
	if s.blocker != nil && s.blocker.IsBlocked(question.Name) {
		s.stats.incrementBlocked()
		s.stats.recordDomainAccess(question.Name, true)
		s.stats.addLog(question.Name, "Block", "Blocked", qType, "blocked")
		return s.blocker.BlockResponse(r), nil
	}

	// 查询缓存
	cached, err := s.cache.Get(cacheKey)
	if err == nil {
		return cached, nil
	}

	// 选择上游服务器并解析
	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		resolvers = s.upstream["cn"]
	} else {
		resolvers = s.upstream["foreign"]
	}

	resolver := s.selectResolver(resolvers)
	response, err := resolver.Resolve(r)
	if err != nil {
		return nil, err
	}

	// 缓存结果
	if err := s.cache.Set(cacheKey, response); err != nil {
		log.Printf("设置缓存失败: %v", err)
	}

	return response, nil
}

func (s *DNSServer) tryResolve(r *dns.Msg) (*dns.Msg, error) {
	start := time.Now()
	defer func() {
		s.stats.DNSLatency.Observe(time.Since(start).Seconds())
	}()

	if len(r.Question) == 0 {
		return nil, fmt.Errorf("无效的DNS请求")
	}

	question := r.Question[0]
	domain := question.Name
	qType := dns.TypeToString[question.Qtype]
	cacheKey := fmt.Sprintf("%s:%d", question.Name, question.Qtype)

	s.stats.incrementTotal()
	log.Printf("收到DNS查询: %s (%s)", domain, qType)

	// 记录域名访问
	s.stats.recordDomainAccess(domain, false)

	// 检查是否需要拦截
	if s.blocker != nil && s.blocker.IsBlocked(domain) {
		s.stats.incrementBlocked()
		s.stats.recordDomainAccess(domain, true)
		s.stats.addLog(domain, "Block", "Blocked", qType, "已拦截")
		log.Printf("域名已拦截: %s", domain)
		return s.blocker.BlockResponse(r), nil
	}

	// 查询缓存
	cached, err := s.cache.Get(cacheKey)
	if err == nil {
		s.stats.incrementCacheHits()
		status := "Hit"
		// 检查是否是过期缓存
		isStale := false
		for _, rr := range cached.Extra {
			if txt, ok := rr.(*dns.TXT); ok && txt.Hdr.Name == "stale-cache" {
				isStale = true
				break
			}
		}
		if isStale {
			status = "Stale"
			// 异步刷新缓存
			go s.refreshCache(r, cacheKey)
		}
		result := formatDNSResult(cached)
		s.stats.addLog(domain, "Cache", status, qType, result)
		log.Printf("命中缓存[Redis]: %s (%s: %s)", domain, qType, result)
		response := cached.Copy()
		response.Id = r.Id
		return response, nil
	}

	// 选择上游服务器
	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(domain) {
		s.stats.incrementCN()
		resolvers = s.upstream["cn"]
		log.Printf("使用国内DNS服务器解析: %s", domain)
	} else {
		s.stats.incrementForeign()
		resolvers = s.upstream["foreign"]
		log.Printf("使用国外DNS服务器解析: %s", domain)
	}

	// 选择解析器并解析
	resolver := s.selectResolver(resolvers)
	resolveStart := time.Now()
	response, err := resolver.Resolve(r)
	resolveLatency := time.Since(resolveStart)

	if err != nil {
		s.stats.incrementFailed()
		s.stats.addLog(domain, "Error", "Failed", qType, err.Error())
		return nil, err
	}

	// 缓存结果
	result := formatDNSResult(response)
	s.stats.addLog(domain, "Query", "Success", qType, result)
	log.Printf("解析成功: %s (%s: %s)", domain, qType, result)
	if err := s.cache.Set(cacheKey, response); err != nil {
		log.Printf("设置缓存失败: %v", err)
	}

	// 记录上游服务器使用情况和延迟
	if dohResolver, ok := resolver.(*DOHResolver); ok {
		if _, ok := s.stats.UpstreamLatency[dohResolver.endpoint]; !ok {
			s.stats.UpstreamLatency[dohResolver.endpoint] = NewSimpleHistogram()
		}
		s.stats.UpstreamLatency[dohResolver.endpoint].Observe(resolveLatency.Seconds())
		s.stats.UpstreamUsage[dohResolver.endpoint]++
	}

	return response, nil
}

func (s *DNSServer) checkResolver(resolver Resolver) error {
	m := new(dns.Msg)
	m.SetQuestion("www.google.com.", dns.TypeA)
	_, err := resolver.Resolve(m)
	return err
}

func (s *DNSServer) startHealthCheck() {
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for range ticker.C {
			if err := s.healthCheck(); err != nil {
				log.Printf("健康检查失败: %v", err)
			}
		}
	}()
}
