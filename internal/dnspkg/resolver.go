package dnspkg

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/miekg/dns"
)

type Resolver interface {
	Resolve(request *dns.Msg) (*dns.Msg, error)
}

type DOHResolver struct {
	Endpoint  string
	Client    *http.Client
	FailCount int32
	LastFail  time.Time
}

func NewDOHResolver(endpoint string) *DOHResolver {
	return &DOHResolver{
		Endpoint: endpoint,
		Client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (r *DOHResolver) Resolve(request *dns.Msg) (*dns.Msg, error) {
	packed, err := request.Pack()
	if err != nil {
		return nil, fmt.Errorf("打包DNS消息失败: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, r.Endpoint, bytes.NewReader(packed))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")

	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP请求返回错误状态码: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	response := new(dns.Msg)
	if err := response.Unpack(body); err != nil {
		return nil, fmt.Errorf("解析DNS响应失败: %v", err)
	}

	return response, nil
}
