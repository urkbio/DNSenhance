package main

import (
	"bufio"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
)

type Blocker struct {
	domains map[string]bool
	enabled bool
}

func NewBlocker(enabled bool, blockFile string) (*Blocker, error) {
	blocker := &Blocker{
		domains: make(map[string]bool),
		enabled: enabled,
	}

	if !enabled {
		return blocker, nil
	}

	file, err := os.Open(blockFile)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
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
		blocker.domains[domain] = true
	}

	return blocker, scanner.Err()
}

func (b *Blocker) IsBlocked(domain string) bool {
	if !b.enabled {
		return false
	}
	return b.domains[domain]
}

func (b *Blocker) BlockResponse(r *dns.Msg) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true

	// 根据查询类型返回不同的响应
	for _, q := range r.Question {
		switch q.Qtype {
		case dns.TypeA:
			rr := &dns.A{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    3600,
				},
				A: net.ParseIP("127.0.0.1").To4(),
			}
			m.Answer = append(m.Answer, rr)
		case dns.TypeAAAA:
			rr := &dns.AAAA{
				Hdr: dns.RR_Header{
					Name:   q.Name,
					Rrtype: dns.TypeAAAA,
					Class:  dns.ClassINET,
					Ttl:    3600,
				},
				AAAA: net.ParseIP("::1"),
			}
			m.Answer = append(m.Answer, rr)
		}
	}

	return m
}
