package main

import (
	"github.com/miekg/dns"
	"log"
	"time"
	"sync"
	"io/ioutil"
	"net"
	"fmt"
	"bufio"
	"os"
	"encoding/json"
	"strings"
)

type DNSServer struct {
	cache     *DNSCache
	geoFilter *GeoFilter
	upstream  map[string][]Resolver
	server    *dns.Server
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

	fmt.Println("[2/4] 初始化上游解析器...")
	// 初始化上游解析器
	for category, endpoints := range config.Upstream {
		resolvers := make([]Resolver, len(endpoints))
		for i, endpoint := range endpoints {
			resolvers[i] = NewDOHResolver(endpoint)
		}
		server.upstream[category] = resolvers
	}
	fmt.Println("✓ 上游解析器初始化完成")

	fmt.Println("[3/4] 启动统计Web服务器...")
	startStatsServer(server.stats, 8080)
	fmt.Println("✓ 统计Web服务器启动完成，访问 http://localhost:8080 查看统计信息")

	// 启动DNS服务器
	fmt.Println("[4/4] 启动DNS服务器...")
	dns.HandleFunc(".", server.handleDNSRequest)
	
	// 检查53端口是否被占用
	listener, err := net.Listen("tcp", ":53")
	if err != nil {
		fmt.Println("❌ 53端口被占用，请检查DNS Client服务是否已关闭:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		log.Fatal("53端口被占用: ", err)
		return
	}
	listener.Close()

	// 创建UDP服务器
	udpServer := &dns.Server{Addr: ":53", Net: "udp"}
	// 创建TCP服务器
	tcpServer := &dns.Server{Addr: ":53", Net: "tcp"}

	// 在goroutine中启动TCP服务器
	go func() {
		err := tcpServer.ListenAndServe()
		if err != nil {
			log.Printf("TCP服务器错误: %v\n", err)
		}
	}()

	fmt.Println("✓ DNS服务器启动完成")
	fmt.Println("===================")
	fmt.Println("监听端口: :53 (UDP/TCP)")
	fmt.Println("统计页面: http://localhost:8080")
	fmt.Println("按Ctrl+C停止服务器")
	fmt.Println("===================")
	
	log.Println("DNS服务器启动完成，监听端口 :53 (UDP/TCP)")

	// 启动UDP服务器（主服务器）
	err = udpServer.ListenAndServe()
	if err != nil {
		fmt.Println("❌ UDP服务器启动失败:", err)
		fmt.Println("按回车键退出...")
		bufio.NewReader(os.Stdin).ReadBytes('\n')
		log.Fatal(err)
	}

	// 优雅关闭
	udpServer.Shutdown()
	tcpServer.Shutdown()
} 