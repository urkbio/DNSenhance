package main

import (
	"github.com/miekg/dns"
	"encoding/base64"
	"io/ioutil"
	"net/http"
	"fmt"
	"time"
)

type DOHResolver struct {
	endpoint string
	client   *http.Client
}

func NewDOHResolver(endpoint string) *DOHResolver {
	return &DOHResolver{
		endpoint: endpoint,
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				DisableCompression:  true,
			},
		},
	}
}

func (r *DOHResolver) Resolve(request *dns.Msg) (*dns.Msg, error) {
	packed, err := request.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack request failed: %v", err)
	}

	b64 := base64.RawURLEncoding.EncodeToString(packed)
	url := fmt.Sprintf("%s?dns=%s", r.endpoint, b64)
	
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request failed: %v", err)
	}
	
	req.Header.Set("accept", "application/dns-message")
	
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := ioutil.ReadAll(resp.Body)
		return nil, fmt.Errorf("bad status code: %d, body: %s", resp.StatusCode, string(body))
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %v", err)
	}

	response := new(dns.Msg)
	err = response.Unpack(body)
	if err != nil {
		return nil, fmt.Errorf("unpack response failed: %v", err)
	}

	return response, nil
}