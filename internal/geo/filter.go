package geo

import (
	"bufio"
	"bytes"
	"strings"
)

type Filter struct {
	domains map[string]bool
}

func NewFilter(data []byte) (*Filter, error) {
	f := &Filter{
		domains: make(map[string]bool),
	}

	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		domain := strings.TrimSpace(scanner.Text())
		// 跳过空行和注释
		if domain == "" || strings.HasPrefix(domain, "#") {
			continue
		}
		// 确保域名以点结尾
		if !strings.HasSuffix(domain, ".") {
			domain = domain + "."
		}
		f.domains[domain] = true
	}

	return f, scanner.Err()
}

// IsDomainCN 检查域名是否在中国域名列表中
func (f *Filter) IsDomainCN(domain string) bool {
	// 确保域名以点结尾
	if !strings.HasSuffix(domain, ".") {
		domain = domain + "."
	}

	// 检查完整域名
	if f.domains[domain] {
		return true
	}

	// 检查父域名
	parts := strings.Split(domain, ".")
	for i := 1; i < len(parts)-1; i++ {
		parentDomain := strings.Join(parts[i:], ".")
		if f.domains[parentDomain] {
			return true
		}
	}

	return false
}
