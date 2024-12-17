package main

import (
	"strings"
)

type GeoFilter struct {
	domains map[string]bool
}

func NewGeoFilter(geofileData []byte) (*GeoFilter, error) {
	filter := &GeoFilter{
		domains: make(map[string]bool),
	}

	lines := strings.Split(string(geofileData), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 跳过空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 确保域名以点开头
		if !strings.HasPrefix(line, ".") {
			line = "." + line
		}
		filter.domains[line] = true
	}

	// 添加一些常见的中国域名作为补充
	commonCNDomains := []string{
		".cn",
		".com.cn",
		".org.cn",
		".net.cn",
		".gov.cn",
		".edu.cn",
		".ac.cn",
	}

	for _, domain := range commonCNDomains {
		filter.domains[domain] = true
	}

	return filter, nil
}

func (f *GeoFilter) IsDomainCN(domain string) bool {
	domain = strings.ToLower(domain)
	// 移除末尾的点
	domain = strings.TrimSuffix(domain, ".")
	
	// 检查域名是否以已知的中国域名后缀结尾
	for knownDomain := range f.domains {
		if strings.HasSuffix(domain, knownDomain) {
			return true
		}
	}
	
	return false
} 