package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/miekg/dns"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

type DNSServer struct {
	cache     *DNSCache
	geoFilter *GeoFilter
	upstream  map[string][]Resolver
	stats     *Stats
	blocker   *Blocker
}

type DNSCache struct {
	entries sync.Map
	ttl     time.Duration
}

type CacheEntry struct {
	msg      *dns.Msg
	expireAt time.Time
}

type Resolver interface {
	Resolve(request *dns.Msg) (*dns.Msg, error)
}

type Config struct {
	CacheTTL    int                    `json:"cache_ttl"`
	Upstream    map[string][]string    `json:"upstream"`
	DomainFile  string                 `json:"domain_file"`
	Block       struct {
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
	question := r.Question[0]
	cacheKey := question.Name + string(question.Qtype)
	qType := dns.TypeToString[question.Qtype]  // 获取查询类型字符串

	s.stats.incrementTotal()

	// 检查是否需要拦截
	if s.blocker.IsBlocked(question.Name) {
		s.stats.incrementBlocked()
		s.stats.addLog(question.Name, "Block", "Blocked", qType, "已拦截")
		log.Printf("域名已拦截: %s", question.Name)
		w.WriteMsg(s.blocker.BlockResponse(r))
		return
	}

	// 尝试从缓存获取
	if cached, ok := s.cache.Get(cacheKey); ok {
		s.stats.incrementCacheHits()
		result := formatDNSResult(cached)  // 格式化DNS结果
		s.stats.addLog(question.Name, "Cache", "Hit", qType, result)
		log.Printf("命中缓存: %s (%s: %s)", question.Name, qType, result)
		response := cached.Copy()
		response.Id = r.Id
		w.WriteMsg(response)
		return
	}

	// 根据域名判断使用哪个上游
	var resolvers []Resolver
	if s.geoFilter.IsDomainCN(question.Name) {
		s.stats.incrementCN()
		resolvers = s.upstream["cn"]
		for _, resolver := range resolvers {
			if response, err := resolver.Resolve(r); err == nil {
				result := formatDNSResult(response)
				s.stats.addLog(question.Name, "CN", "Query", qType, result)
				log.Printf("解析成功: %s (%s: %s)", question.Name, qType, result)
				s.cache.Set(cacheKey, response)
				w.WriteMsg(response)
				return
			}
		}
	} else {
		s.stats.incrementForeign()
		resolvers = s.upstream["foreign"]
		for _, resolver := range resolvers {
			if response, err := resolver.Resolve(r); err == nil {
				result := formatDNSResult(response)
				s.stats.addLog(question.Name, "Foreign", "Query", qType, result)
				log.Printf("解析成功: %s (%s: %s)", question.Name, qType, result)
				s.cache.Set(cacheKey, response)
				w.WriteMsg(response)
				return
			}
		}
	}

	// 所有解析器都失败时返回服务器错误
	s.stats.incrementFailed()
	s.stats.addLog(question.Name, "Error", "Failed", qType, "解析失败")
	log.Printf("所有解析器都失败: %s", question.Name)
	m := new(dns.Msg)
	m.SetRcode(r, dns.RcodeServerFailure)
	w.WriteMsg(m)
}

// 添加一个辅助函数来格式化DNS结果
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

// 实现 dns.Handler 接口
func (s *DNSServer) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	s.handleDNSRequest(w, r)
}

func main() {
	// 将日志输出到UTF-8编码的文件
	logFile, err := os.OpenFile("dns.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		fmt.Println("无法创建日志文件:", err)
		return
	}
	defer logFile.Close()

	// 写入UTF-8 BOM
	if stat, err := logFile.Stat(); err == nil && stat.Size() == 0 {
		logFile.Write([]byte{0xEF, 0xBB, 0xBF})
	}

	log.SetOutput(logFile)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	fmt.Println("=== DNS服务器启动 ===")
	log.Println("=== DNS服务器启动 ===")
	
	fmt.Println("[1/4] 加载配置文件...")
	config, err := loadConfig("config.json")
	if err != nil {
		fmt.Println("❌ 配置文件加载失败:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	fmt.Println("✓ 配置文件加载完成")

	fmt.Println("[2/4] 加载域名列表...")
	// 加载域名列表
	geoData, err := ioutil.ReadFile(config.DomainFile)
	if err != nil {
		fmt.Printf("❌ 域名列表文件 %s 加载失败: %v\n", config.DomainFile, err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		return
	}
	
	// 初始化 GeoFilter
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

	// 初始化服务器
	server := &DNSServer{
		cache:     NewDNSCache(time.Duration(config.CacheTTL) * time.Second),
		geoFilter: geoFilter,
		upstream:  make(map[string][]Resolver),
		stats:     NewStats(),
		blocker:   blocker,
	}

	// 初始化上游解析器
	for category, endpoints := range config.Upstream {
		resolvers := make([]Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = NewDOHResolver(endpoint)
		}
		server.upstream[category] = resolvers
	}

	fmt.Println("[3/5] 启动统计Web服务器...")
	go startStatsServer(server.stats, 8080)
	fmt.Println("✓ 统计Web服务器启动完成")

	fmt.Println("[4/5] 启动DNS服务器...")
	// 启动DNS服务器
	dnsServer := &dns.Server{
		Addr:    ":53",
		Net:     "udp",
		Handler: server,
	}

	// 在新的 goroutine 中启动 DNS 服务器
	go func() {
		if err := dnsServer.ListenAndServe(); err != nil {
			fmt.Printf("❌ DNS服务器启动失败: %s\n", err.Error())
			os.Exit(1)
		}
	}()
	fmt.Println("✓ DNS服务器启动完成")

	fmt.Println("[5/5] 初始化系统托盘...")
	// 创建一个通道用于同步退出
	exitChan := make(chan struct{})

	// 启动系统托盘
	go func() {
		fmt.Println("✓ 系统托盘初始化完成")
		fmt.Println("===================")
		fmt.Println("监听端口: :53 (UDP)")
		fmt.Println("统计页面: http://localhost:8080")
		fmt.Println("===================")
		initSysTray()
		close(exitChan) // 当托盘退出时，通知主程序退出
	}()

	// 等待退出信号
	<-exitChan

	// 优雅关闭
	fmt.Println("正在关闭服务器...")
	dnsServer.Shutdown()
	fmt.Println("服务器已关闭")
} 