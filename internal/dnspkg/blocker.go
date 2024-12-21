package dnspkg

import (
	"bufio"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
)

type Blocker struct {
	Domains map[string]bool
	Enabled bool
}

func NewBlocker(enabled bool, blockFile string) (*Blocker, error) {
	blocker := &Blocker{
		Domains: make(map[string]bool),
		Enabled: enabled,
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
		if domain == "" || strings.HasPrefix(domain, "#") {
			continue
		}
		if !strings.HasSuffix(domain, ".") {
			domain = domain + "."
		}
		blocker.Domains[domain] = true
	}

	return blocker, scanner.Err()
}

func (b *Blocker) IsBlocked(domain string) bool {
	if !b.Enabled {
		return false
	}
	return b.Domains[domain]
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

// ... 其他方法的实现
