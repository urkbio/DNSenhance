package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

type DOHResolver struct {
	endpoint  string
	client    *http.Client
	failCount int32
	lastFail  time.Time
}

func NewDOHResolver(endpoint string) *DOHResolver {
	return &DOHResolver{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:       100,
				IdleConnTimeout:    90 * time.Second,
				DisableCompression: true,
			},
		},
		lastFail: time.Now(),
	}
}

func (r *DOHResolver) Resolve(request *dns.Msg) (*dns.Msg, error) {
	packed, err := request.Pack()
	if err != nil {
		return nil, fmt.Errorf("打包DNS请求失败: %v", err)
	}

	req, err := http.NewRequest("POST", r.endpoint, bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP状态码错误: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	response := new(dns.Msg)
	if err := response.Unpack(body); err != nil {
		return nil, fmt.Errorf("解析DNS响应失败: %v", err)
	}

	return response, nil
}
