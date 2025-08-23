package registry

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/google/go-containerregistry/pkg/authn"
)

// HTTPClient 封装HTTP客户端，提供统一的请求方法
type HTTPClient struct {
	authenticator authn.Authenticator
	insecure      bool
	client        *http.Client
}

// NewHTTPClient 创建新的HTTP客户端
func NewHTTPClient(authenticator authn.Authenticator, insecure bool) *HTTPClient {
	client := &http.Client{}

	// 如果启用了不安全连接，跳过TLS验证
	if insecure {
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	return &HTTPClient{
		authenticator: authenticator,
		insecure:      insecure,
		client:        client,
	}
}

// Get 发送GET请求并返回响应体
func (h *HTTPClient) Get(url string) ([]byte, error) {
	return h.Request("GET", url)
}

// Request 发送HTTP请求并返回响应体
func (h *HTTPClient) Request(method, url string) ([]byte, error) {
	// 创建HTTP请求
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	// 添加认证头（如果有）
	if h.authenticator != authn.Anonymous {
		authConfig, err := h.authenticator.Authorization()
		if err == nil {
			if authConfig.Username != "" && authConfig.Password != "" {
				req.SetBasicAuth(authConfig.Username, authConfig.Password)
			} else if authConfig.RegistryToken != "" {
				req.Header.Set("Authorization", "Bearer "+authConfig.RegistryToken)
			}
		}
	}

	// 如果启用了不安全连接，记录日志
	if h.insecure {
		log.Printf("为API请求启用了不安全连接（跳过TLS验证）")
	}

	// 发送HTTP请求
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取API响应失败: %v", err)
	}

	return body, nil
}
