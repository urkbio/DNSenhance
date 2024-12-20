package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"html/template"

	"strconv"

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
	// 使用加权轮询算法
	var selected Resolver
	minFails := int32(math.MaxInt32)

	for _, r := range resolvers {
		doh := r.(*DOHResolver)
		// 如果失败时间超过30秒，重置失败计数
		if time.Since(doh.lastFail) > 30*time.Second {
			atomic.StoreInt32(&doh.failCount, 0)
		}

		fails := atomic.LoadInt32(&doh.failCount)
		if fails < minFails {
			minFails = fails
			selected = r
		}
	}

	return selected
}

// 使用 sync.Pool 来复用 DNS 消息对象
var msgPool = sync.Pool{
	New: func() interface{} {
		return new(dns.Msg)
	},
}

func (s *DNSServer) handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	start := time.Now()
	defer func() {
		s.stats.DNSLatency.Observe(time.Since(start).Seconds())
	}()

	msg := msgPool.Get().(*dns.Msg)
	defer msgPool.Put(msg)

	if len(r.Question) == 0 {
		return
	}

	question := r.Question[0]
	cacheKey := fmt.Sprintf("%s:%d", question.Name, question.Qtype)
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
		s.stats.addLog(question.Name, "Cache", status, qType, result)
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
		if len(resolvers) > 1 {
			remaining := make([]Resolver, 0, len(resolvers)-1)
			for _, r := range resolvers {
				if r != resolver {
					remaining = append(remaining, r)
				}
			}
			s.handleDNSRequestWithResolvers(w, r, remaining)
			return
		}
		s.stats.incrementFailed()
		s.stats.addLog(question.Name, "Error", "Failed", qType, "解析失败")
		log.Printf("所有解析器均失败: %s", question.Name)
		m := new(dns.Msg)
		m.SetReply(r)
		m.Rcode = dns.RcodeServerFailure
		w.WriteMsg(m)
		return
	}

	result := formatDNSResult(response)
	s.stats.addLog(question.Name, "Query", "Success", qType, result)
	log.Printf("解析成功: %s (%s: %s)", question.Name, qType, result)
	if err := s.cache.Set(cacheKey, response); err != nil {
		log.Printf("设置缓存失败: %v", err)
	}
	w.WriteMsg(response)
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
	fmt.Println("✓ 域名表加载完成")

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
	fmt.Printf("✓ DNS缓存初始化成 (TTL: %d秒)\n", config.CacheTTL)

	fmt.Println("测试Redis缓存...")
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
			stats.cache = cache
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

	fmt.Println("[3/5] 启动统计Web服务器...")
	httpServer := startStatsServer(server.stats, cache, 8080)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP服务器错误: %v", err)
		}
	}()
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
	fmt.Println("✓ DNS服务器动完成")

	fmt.Println("[5/5] 初始化系统托盘...")
	exitChan := make(chan struct{})

	// 添加信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		fmt.Println("✓ 系统托盘初始化完成")
		fmt.Println("===================")
		fmt.Println("监听端口: :53 (UDP)")
		fmt.Println("统计面: http://localhost:8080")
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

	fmt.Println("正在关闭务器...")
	if err := redisManager.Stop(); err != nil {
		log.Printf("停止Redis服务失败: %v", err)
	}
	dnsServer.Shutdown()
	fmt.Println("服务器已关闭")

	// 添加信号处理
	go func() {
		<-sigChan
		fmt.Println("\n正在关闭服务器...")

		// 设置关闭超时
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// 优雅关闭 HTTP 服务器
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("HTTP服务器关闭错误: %v", err)
		}

		// 关闭 DNS 服务器
		dnsServer.Shutdown()

		// 关闭 Redis
		if err := redisManager.Stop(); err != nil {
			log.Printf("Redis关闭错误: %v", err)
		}

		close(exitChan)
	}()
}

func parseRedisInfo(info string) map[string]string {
	result := make(map[string]string)
	lines := strings.Split(info, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) == 2 {
			result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}

func redisStatusHandler(cache *DNSCache) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 获取 Redis 信息
		info := make(map[string]string)

		// 获取服务器信息
		serverInfoStr, err := cache.client.Info(cache.ctx, "server").Result()
		if err != nil {
			http.Error(w, fmt.Sprintf("获取Redis状态失败: %v", err), http.StatusInternalServerError)
			return
		}
		info["server"] = serverInfoStr

		// 获取内存信息
		memoryInfo, err := cache.client.Info(cache.ctx, "memory").Result()
		if err == nil {
			info["memory"] = memoryInfo
		}

		// 获取统计信息
		statsInfoStr, err := cache.client.Info(cache.ctx, "stats").Result()
		if err == nil {
			info["stats"] = statsInfoStr
		}

		// 获取当前缓存统计
		staleHits := atomic.LoadInt64(&cache.stats.staleHits)
		normalHits := atomic.LoadInt64(&cache.stats.normalHits)
		misses := atomic.LoadInt64(&cache.stats.misses)

		cacheStats := map[string]interface{}{
			"stale_hits":  staleHits,
			"normal_hits": normalHits,
			"misses":      misses,
		}

		// 解析 Redis INFO 输出
		memInfo := parseRedisInfo(info["memory"])
		statsInfo := parseRedisInfo(info["stats"])
		serverInfo := parseRedisInfo(info["server"])

		var usedMemory, peakMemory, totalOps, uptime int64
		var fragRatio float64
		var clients int

		if val, ok := memInfo["used_memory"]; ok {
			usedMemory, _ = strconv.ParseInt(val, 10, 64)
		}
		if val, ok := memInfo["used_memory_peak"]; ok {
			peakMemory, _ = strconv.ParseInt(val, 10, 64)
		}
		if val, ok := memInfo["mem_fragmentation_ratio"]; ok {
			fragRatio, _ = strconv.ParseFloat(val, 64)
		}
		if val, ok := statsInfo["total_commands_processed"]; ok {
			totalOps, _ = strconv.ParseInt(val, 10, 64)
		}
		if val, ok := serverInfo["uptime_in_seconds"]; ok {
			uptime, _ = strconv.ParseInt(val, 10, 64)
		}
		if val, ok := memInfo["connected_clients"]; ok {
			clients, _ = strconv.Atoi(val)
		}

		// 计算命中率
		totalRequests := staleHits + normalHits + misses
		hitRate := 0.0
		if totalRequests > 0 {
			hitRate = float64(staleHits+normalHits) / float64(totalRequests) * 100
		}

		// 计算每秒操作数
		var opsPerSec int64
		if uptime > 0 {
			opsPerSec = totalOps / uptime
		}

		// 格式化内存大小
		formatMemory := func(bytes int64) string {
			const unit = 1024
			if bytes < unit {
				return fmt.Sprintf("%d B", bytes)
			}
			div, exp := int64(unit), 0
			for n := bytes / unit; n >= unit; n /= unit {
				div *= unit
				exp++
			}
			return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTPE"[exp])
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// 使用 HTML 模板渲染
		tmpl := `
		<!DOCTYPE html>
		<html>
		<head>
			<title>Redis 状态监控</title>
			<meta charset="utf-8">
			<style>
				body { font-family: 'Roboto', sans-serif; padding: 20px; background: #f5f5f5; }
				.section { 
					background: white;
					margin-bottom: 20px;
					padding: 20px;
					border-radius: 12px;
					box-shadow: 0 4px 6px rgba(0,0,0,0.1);
				}
				.section h2 { 
					color: #2196F3;
					margin-bottom: 15px;
					padding-bottom: 10px;
					border-bottom: 2px solid #e3f2fd;
				}
				.stat-grid {
					display: grid;
					grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
					gap: 15px;
				}
				.stat-item {
					background: #f8f9fa;
					padding: 15px;
					border-radius: 8px;
					border-left: 4px solid #2196F3;
				}
				.stat-label { 
					font-weight: 500;
					color: #666;
					margin-bottom: 5px;
				}
				.stat-value { 
					font-size: 18px;
					color: #333;
				}
				.header {
					display: flex;
					justify-content: space-between;
					align-items: center;
					margin-bottom: 20px;
				}
				.refresh-btn {
					position: fixed;
					bottom: 20px;
					right: 20px;
					padding: 12px 24px;
					background: #2196F3;
					color: white;
					border: none;
					border-radius: 8px;
					cursor: pointer;
					box-shadow: 0 2px 4px rgba(0,0,0,0.1);
				}
				.refresh-btn:hover { background: #1976D2; }
				.highlight { color: #2196F3; }
				.warning { color: #f57c00; }
				.error { color: #d32f2f; }
			</style>
		</head>
		<body>
			<div class="header">
				<h1>Redis 状态监控</h1>
				<a href="/" style="text-decoration:none;color:#2196F3;">返回仪表板</a>
			</div>
			
			<div class="section">
				<h2>DNS 缓存统计</h2>
				<div class="stat-grid">
					<div class="stat-item">
						<div class="stat-label">正常命中</div>
						<div class="stat-value highlight">%d</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">过期命中</div>
						<div class="stat-value warning">%d</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">未命中</div>
						<div class="stat-value error">%d</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">命中率</div>
						<div class="stat-value">%.1f%%</div>
					</div>
				</div>
			</div>
			
			<div class="section">
				<h2>内存使用</h2>
				<div class="stat-grid">
					<div class="stat-item">
						<div class="stat-label">已用内存</div>
						<div class="stat-value">%s</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">内存峰值</div>
						<div class="stat-value">%s</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">内存碎片率</div>
						<div class="stat-value">%.2f</div>
					</div>
				</div>
			</div>
			
			<div class="section">
				<h2>性能指标</h2>
				<div class="stat-grid">
					<div class="stat-item">
						<div class="stat-label">每秒操作数</div>
						<div class="stat-value">%d ops/sec</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">连接数</div>
						<div class="stat-value">%d</div>
					</div>
					<div class="stat-item">
						<div class="stat-label">运行时间</div>
						<div class="stat-value">%s</div>
					</div>
				</div>
			</div>

			<button class="refresh-btn" onclick="location.reload()">
				刷新数据
			</button>

			<script>
				// 每30秒自动刷新
				setTimeout(() => location.reload(), 30000);
			</script>
		</body>
		</html>
		`

		html := fmt.Sprintf(tmpl,
			cacheStats["normal_hits"],
			cacheStats["stale_hits"],
			cacheStats["misses"],
			hitRate,
			formatMemory(usedMemory),
			formatMemory(peakMemory),
			fragRatio,
			opsPerSec,
			clients,
			formatDuration(time.Duration(uptime)*time.Second),
		)

		w.Write([]byte(html))
	}
}

func startStatsServer(stats *Stats, cache *DNSCache, port int) *http.Server {
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

	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: nil, // 使用默认的 DefaultServeMux
	}

	return server
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
			"staleHits":      stats.cache.stats.staleHits,
			"normalHits":     stats.cache.stats.normalHits,
			"cacheMisses":    stats.cache.stats.misses,
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

// 添加结构化日志
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
