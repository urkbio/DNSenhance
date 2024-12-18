package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"html/template"

	"github.com/go-redis/redis/v8"
	"github.com/miekg/dns"
)

type DNSServer struct {
	cache     *DNSCache
	geoFilter *GeoFilter
	upstream  map[string][]Resolver
	stats     *Stats
	blocker   *Blocker
}

type DNSCache struct {
	client *redis.Client
	ttl    time.Duration
	ctx    context.Context
}

func NewDNSCache(client *redis.Client, ttl time.Duration) *DNSCache {
	return &DNSCache{
		client: client,
		ttl:    ttl,
		ctx:    context.Background(),
	}
}

func (c *DNSCache) Set(key string, msg *dns.Msg) error {
	entry := struct {
		Message   *dns.Msg  `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}{
		Message:   msg,
		CreatedAt: time.Now(),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	return c.client.Set(c.ctx, "dns:"+key, data, c.ttl).Err()
}

func (c *DNSCache) Get(key string) (*dns.Msg, error) {
	data, err := c.client.Get(c.ctx, "dns:"+key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("缓存未命中")
		}
		return nil, err
	}

	var entry struct {
		Message   *dns.Msg  `json:"message"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := json.Unmarshal([]byte(data), &entry); err != nil {
		return nil, err
	}

	if time.Since(entry.CreatedAt) > c.ttl {
		c.client.Del(c.ctx, "dns:"+key)
		return nil, fmt.Errorf("缓存已过期")
	}

	return entry.Message, nil
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

func (s *DNSServer) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	if len(r.Question) == 0 {
		return
	}

	question := r.Question[0]
	cacheKey := question.Name + string(question.Qtype)
	qType := dns.TypeToString[question.Qtype]

	s.stats.incrementTotal()
	log.Printf("收到DNS查询: %s (%s)", question.Name, qType)

	if s.blocker.IsBlocked(question.Name) {
		s.stats.incrementBlocked()
		s.stats.addLog(question.Name, "Block", "Blocked", qType, "已拦截")
		log.Printf("域名已拦截: %s", question.Name)
		w.WriteMsg(s.blocker.BlockResponse(r))
		return
	}

	cached, err := s.cache.Get(cacheKey)
	if err == nil {
		s.stats.incrementCacheHits()
		result := formatDNSResult(cached)
		s.stats.addLog(question.Name, "Cache", "Hit", qType, result)
		log.Printf("命中缓存[Redis]: %s (%s: %s)", question.Name, qType, result)
		response := cached.Copy()
		response.Id = r.Id
		w.WriteMsg(response)
		return
	}

	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		s.stats.incrementCN()
		resolvers = s.upstream["cn"]
		log.Printf("使用国内DNS服务器解析: %s", question.Name)
	} else {
		s.stats.incrementForeign()
		resolvers = s.upstream["foreign"]
		log.Printf("使用国外DNS服务器解析: %s", question.Name)
	}

	for _, resolver := range resolvers {
		response, err := resolver.Resolve(r)
		if err != nil {
			log.Printf("解析失败: %s (%v)", question.Name, err)
			continue
		}
		result := formatDNSResult(response)
		s.stats.addLog(question.Name, "Query", "Success", qType, result)
		log.Printf("解析成功: %s (%s: %s)", question.Name, qType, result)
		s.cache.Set(cacheKey, response)
		w.WriteMsg(response)
		return
	}

	s.stats.incrementFailed()
	s.stats.addLog(question.Name, "Error", "Failed", qType, "解析失败")
	log.Printf("所有解析器均失败: %s", question.Name)
	m := new(dns.Msg)
	m.SetReply(r)
	m.Rcode = dns.RcodeServerFailure
	w.WriteMsg(m)
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

	fmt.Println("[1/4] 加载配置文件...")
	config, err := loadConfig("config.json")
	if err != nil {
		fmt.Println("❌ 配置文件加载失败:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	fmt.Println("✓ 置文件加载完成")

	fmt.Println("[2/4] 加载域名列表...")
	geoData, err := ioutil.ReadFile(config.DomainFile)
	if err != nil {
		fmt.Printf("❌ 域名列表文件 %s 加载失败: %v\n", config.DomainFile, err)
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

	fmt.Println("[2/5] 初始化域名拦截器...")
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

	fmt.Println("[1/6] 启动Redis服务...")
	redisManager, err := NewRedisManager(6379)
	if err != nil {
		fmt.Printf("❌ Redis管理器初始化失败: %v\n", err)
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
	fmt.Printf("✓ DNS缓存初始化完成 (TTL: %d秒)\n", config.CacheTTL)

	fmt.Println("测试Redis缓存...")
	testKey := "test:key"
	testValue := "test:value"

	// 测试写入
	err = cache.client.Set(cache.ctx, testKey, testValue, time.Minute).Err()
	if err != nil {
		fmt.Printf("Redis写入测试失败: %v\n", err)
	} else {
		fmt.Println("Redis写入测试成功")
	}

	// 测试读取
	val, err := cache.client.Get(cache.ctx, testKey).Result()
	if err != nil {
		fmt.Printf("Redis读取测试失败: %v\n", err)
	} else if val == testValue {
		fmt.Println("Redis读取测试成功")
	} else {
		fmt.Printf("Redis数据不匹配: 期望 %s, 实际 %s\n", testValue, val)
	}

	server := &DNSServer{
		cache:     cache,
		geoFilter: geoFilter,
		upstream:  make(map[string][]Resolver),
		stats:     NewStats(),
		blocker:   blocker,
	}

	for category, endpoints := range config.Upstream {
		resolvers := make([]Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = NewDOHResolver(endpoint)
		}
		server.upstream[category] = resolvers
	}

	fmt.Println("[3/5] 启动统计Web服务器...")
	go startStatsServer(server.stats, cache, 8080)
	fmt.Println("✓ 统计Web服务器启动完成")

	fmt.Println("[4/5] 启动DNS服务器...")
	dnsServer := &dns.Server{
		Addr:    ":53",
		Net:     "udp",
		Handler: server,
	}

	go func() {
		if err := dnsServer.ListenAndServe(); err != nil {
			fmt.Printf("❌ DNS服务器启动失败: %s\n", err.Error())
			os.Exit(1)
		}
	}()
	fmt.Println("✓ DNS服务器启动完成")

	fmt.Println("[5/5] 初始化系统托盘...")
	exitChan := make(chan struct{})

	go func() {
		fmt.Println("✓ 系统托盘初始化完成")
		fmt.Println("===================")
		fmt.Println("监听端口: :53 (UDP)")
		fmt.Println("统计页面: http://localhost:8080")
		fmt.Println("===================")
		initSysTray()
		close(exitChan)
	}()

	// 添加定期输出 Redis 统计信息
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			// 获取 Redis 信息
			info, err := cache.client.Info(cache.ctx).Result()
			if err != nil {
				log.Printf("获取Redis统计信息失败: %v", err)
				continue
			}
			log.Printf("Redis状态: %s", info)
		}
	}()

	<-exitChan

	fmt.Println("正在关闭服务器...")
	if err := redisManager.Stop(); err != nil {
		log.Printf("停止Redis服务失败: %v", err)
	}
	dnsServer.Shutdown()
	fmt.Println("服务器已关闭")
}

func redisStatusHandler(cache *DNSCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info, err := cache.client.Info(cache.ctx).Result()
		if err != nil {
			http.Error(w, fmt.Sprintf("获��Redis状态失败: %v", err), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(info))
	}
}

func startStatsServer(stats *Stats, cache *DNSCache, port int) {
	// 静态文件服务
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// API 路由
	http.HandleFunc("/api/stats", handleStats(stats))
	http.HandleFunc("/api/logs", handleLogs(stats))
	http.HandleFunc("/api/redis", redisStatusHandler(cache))

	// 页面路由
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.ServeFile(w, r, "static/index.html")
			return
		}
		http.NotFound(w, r)
	})

	http.HandleFunc("/logs", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/logs.html")
	})

	go http.ListenAndServe(fmt.Sprintf(":%d", port), nil)
}

func handleStats(stats *Stats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		stats.RLock()
		defer stats.RUnlock()

		// 计算缓存命中率
		var hitRate float64
		if stats.TotalQueries > 0 {
			hitRate = float64(stats.CacheHits) / float64(stats.TotalQueries) * 100
		}

		data := map[string]interface{}{
			"currentQPS":     stats.CurrentQPS,
			"peakQPS":        stats.PeakQPS,
			"uptime":         formatDuration(time.Since(stats.StartTime)),
			"startTime":      stats.StartTime.Format("2006-01-02 15:04:05"),
			"hitRate":        hitRate,
			"cacheHits":      stats.CacheHits,
			"totalQueries":   stats.TotalQueries,
			"cnQueries":      stats.CNQueries,
			"foreignQueries": stats.ForeignQueries,
			"failedQueries":  stats.FailedQueries,
			"blockedQueries": stats.BlockedQueries,
		}

		json.NewEncoder(w).Encode(data)
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
