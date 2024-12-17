package main

import (
	"github.com/miekg/dns"
	"time"
)

func NewDNSCache(ttl time.Duration) *DNSCache {
	return &DNSCache{
		ttl: ttl,
	}
}

func (c *DNSCache) Get(key string) (*dns.Msg, bool) {
	if entry, exists := c.entries.Load(key); exists {
		cacheEntry := entry.(CacheEntry)
		if time.Now().Before(cacheEntry.expireAt) {
			return cacheEntry.msg, true
		}
		c.entries.Delete(key)
	}
	return nil, false
}

func (c *DNSCache) Set(key string, msg *dns.Msg) {
	c.entries.Store(key, CacheEntry{
		msg:      msg,
		expireAt: time.Now().Add(c.ttl),
	})
} 