package cache

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/miekg/dns"
)

type DNSCache struct {
	Client *redis.Client
	TTL    time.Duration
	Stale  time.Duration
	Ctx    context.Context
	Stats  struct {
		StaleHits  int64
		NormalHits int64
		Misses     int64
	}
}

func New(client *redis.Client, ttl time.Duration) *DNSCache {
	return &DNSCache{
		Client: client,
		TTL:    ttl,
		Stale:  10 * time.Minute,
		Ctx:    context.Background(),
	}
}

func (c *DNSCache) Set(key string, msg *dns.Msg) error {
	packed, err := msg.Pack()
	if err != nil {
		return fmt.Errorf("打包DNS消息失败: %v", err)
	}

	now := time.Now()
	expireAt := now.Add(c.TTL)

	pipe := c.Client.Pipeline()
	pipe.HSet(c.Ctx, "dns:"+key,
		"msg", packed,
		"expire_at", expireAt.Unix(),
	)
	pipe.ExpireAt(c.Ctx, "dns:"+key, now.Add(c.TTL+c.Stale))

	_, err = pipe.Exec(c.Ctx)
	return err
}

func (c *DNSCache) Get(key string) (*dns.Msg, error) {
	result, err := c.Client.HGetAll(c.Ctx, "dns:"+key).Result()
	if err != nil {
		if err == redis.Nil {
			atomic.AddInt64(&c.Stats.Misses, 1)
			log.Printf("缓存未命中: %s", key)
			return nil, fmt.Errorf("缓存未命中")
		}
		log.Printf("Redis错误: %v", err)
		return nil, err
	}

	data, ok := result["msg"]
	if !ok {
		return nil, fmt.Errorf("缓存数据无效")
	}

	expireAtStr, ok := result["expire_at"]
	if !ok {
		c.Stats.Misses++
		return nil, fmt.Errorf("缓存数据不完整")
	}

	expireAt, err := strconv.ParseInt(expireAtStr, 10, 64)
	if err != nil {
		c.Stats.Misses++
		return nil, fmt.Errorf("解析过期时间失败: %v", err)
	}

	msg := new(dns.Msg)
	if err := msg.Unpack([]byte(data)); err != nil {
		c.Stats.Misses++
		c.Client.Del(c.Ctx, "dns:"+key)
		return nil, fmt.Errorf("解析DNS消息失败: %v", err)
	}

	now := time.Now().Unix()
	if now > expireAt {
		if now > expireAt+int64(c.Stale.Seconds()) {
			c.Stats.Misses++
			return nil, fmt.Errorf("缓存已过期")
		}
		// 标记为过期缓存
		c.Stats.StaleHits++
		msg.Extra = append(msg.Extra, &dns.TXT{
			Hdr: dns.RR_Header{
				Name:   "stale-cache",
				Rrtype: dns.TypeTXT,
				Class:  dns.ClassINET,
				Ttl:    0,
			},
			Txt: []string{"1"},
		})
	} else {
		c.Stats.NormalHits++
	}

	return msg, nil
}

func (c *DNSCache) Clean() error {
	pattern := "dns:*"
	var cursor uint64
	for {
		var keys []string
		var err error
		keys, cursor, err = c.Client.Scan(c.Ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}

		for _, key := range keys {
			result, err := c.Client.HGetAll(c.Ctx, key).Result()
			if err != nil {
				continue
			}

			expireAtStr, ok := result["expire_at"]
			if !ok {
				c.Client.Del(c.Ctx, key)
				continue
			}

			expireAt, err := strconv.ParseInt(expireAtStr, 10, 64)
			if err != nil || time.Now().Unix() > expireAt+int64(c.Stale.Seconds()) {
				c.Client.Del(c.Ctx, key)
			}
		}

		if cursor == 0 {
			break
		}
	}
	return nil
}

// ... 其他方法的实现
