package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"time"
)

const operatorAPI = "https://api.bringyour.com"

type authorization struct {
	endpoint string
	client   *http.Client
}

func newAuthorization() *authorization {
	return &authorization{
		endpoint: operatorAPI,
		client: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 7 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   7 * time.Second,
				ResponseHeaderTimeout: 12 * time.Second,
				IdleConnTimeout:       30 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (a *authorization) request(method, path, bearer, stage string, args, result any) error {
	var body io.Reader
	if args != nil {
		data, err := json.Marshal(args)
		if err != nil {
			return fmt.Errorf("%s：无法准备请求", stage)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.endpoint+path, body)
	if err != nil {
		return fmt.Errorf("%s：无法创建请求", stage)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := a.client.Do(req)
	if err != nil {
		var dns *net.DNSError
		var network net.Error
		switch {
		case errors.As(err, &dns):
			return fmt.Errorf("%s：无法解析 api.bringyour.com", stage)
		case errors.As(err, &network) && network.Timeout():
			return fmt.Errorf("%s：连接 URnetwork 官方接口超时", stage)
		default:
			return fmt.Errorf("%s：无法与 URnetwork 官方接口建立 HTTPS 连接", stage)
		}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s：URnetwork 限制了请求频率（HTTP 429），请稍后再启动", stage)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("%s：URnetwork 返回 HTTP %d", stage, res.StatusCode)
	}
	if result == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (128<<10)+1))
	defer clear(data)
	if err != nil || len(data) > 128<<10 || json.Unmarshal(data, result) != nil {
		return fmt.Errorf("%s：URnetwork 返回的数据无法读取", stage)
	}
	return nil
}

func (a *authorization) check() error {
	return a.request(http.MethodGet, "/network/provider-locations", "", "连通性检查", nil, nil)
}

func (a *authorization) createGuest() (string, error) {
	var result struct {
		Network struct {
			JWT string `json:"by_jwt"`
		} `json:"network"`
		Verification *struct{} `json:"verification_required"`
		Error        *struct{} `json:"error"`
	}
	// The current operator creates an instant account when no sign-in method is supplied.
	if err := a.request(http.MethodPost, "/auth/network-create", "", "开通访客身份", map[string]bool{"terms": true}, &result); err != nil {
		return "", err
	}
	if result.Error != nil || result.Verification != nil || result.Network.JWT == "" {
		return "", errors.New("开通访客身份：URnetwork 没有返回可用的账号凭证")
	}
	return result.Network.JWT, nil
}

func (a *authorization) registerGuest(jwt string) (string, error) {
	var result struct {
		JWT      string `json:"by_client_jwt"`
		ClientID string `json:"client_id"`
		Error    *struct {
			ClientLimit     bool `json:"client_limit_exceeded"`
			UpgradeRequired bool `json:"upgrade_required"`
		} `json:"error"`
	}
	args := map[string]string{"description": "URnetwork OneClick local proxy", "device_spec": "windows " + runtime.GOARCH}
	if err := a.request(http.MethodPost, "/network/auth-client", jwt, "注册代理设备", args, &result); err != nil {
		return "", err
	}
	if result.Error != nil {
		if result.Error.ClientLimit || result.Error.UpgradeRequired {
			return "", errors.New("注册代理设备：已达到 URnetwork 的设备或套餐上限")
		}
		return "", errors.New("注册代理设备：URnetwork 拒绝了设备注册")
	}
	if !validID(result.ClientID) || deviceID(result.JWT) != result.ClientID {
		return "", errors.New("注册代理设备：URnetwork 返回的设备凭证与设备不匹配")
	}
	return result.JWT, nil
}
