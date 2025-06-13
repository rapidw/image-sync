package config

import (
	"fmt"
	"io/ioutil"

	"gopkg.in/yaml.v3"
)

// AuthConfig 存储仓库认证信息
type AuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Token    string `yaml:"token"`
}

// RegistryConfig 存储仓库地址和认证信息
type RegistryConfig struct {
	URL      string     `yaml:"url"`
	Type     string     `yaml:"type,omitempty"`    // 仓库类型: dockerhub, harbor, generic等
	ApiURL   string     `yaml:"api_url,omitempty"` // API地址，用于dockerhub类型的自定义API URL
	Auth     AuthConfig `yaml:"auth,omitempty"`
	Insecure bool       `yaml:"insecure,omitempty"`
}

// ImageMapping 定义镜像映射关系
type ImageMapping struct {
	Source      string `yaml:"source"`
	Destination string `yaml:"destination"`
}

// Config 总体配置结构
type Config struct {
	SourceRegistry      RegistryConfig `yaml:"sourceRegistry"`
	DestinationRegistry RegistryConfig `yaml:"destinationRegistry"`
	Images              []ImageMapping `yaml:"images"`
	Schedule            string         `yaml:"schedule"` // Cron表达式
}

// LoadConfig 从文件加载配置
func LoadConfig(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("无法读取配置文件: %v", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("无法解析配置文件: %v", err)
	}

	// 验证配置有效性
	if err := validateConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

// validateConfig 验证配置有效性
func validateConfig(config *Config) error {
	// 检查源注册表
	if config.SourceRegistry.URL == "" {
		return fmt.Errorf("源注册表 URL 不能为空")
	}

	// 检查目标注册表
	if config.DestinationRegistry.URL == "" {
		return fmt.Errorf("目标注册表 URL 不能为空")
	}

	// 检查镜像映射
	if len(config.Images) == 0 {
		return fmt.Errorf("至少需要一个镜像映射")
	}

	// 检查调度表达式
	if config.Schedule == "" {
		return fmt.Errorf("调度表达式不能为空")
	}

	return nil
}

// SaveDefaultConfig 保存默认配置
func SaveDefaultConfig(path string) error {
	defaultConfig := Config{
		SourceRegistry: RegistryConfig{
			URL: "https://registry.example.com",
			Auth: AuthConfig{
				Username: "username",
				Password: "password",
			},
			Insecure: false,
		},
		DestinationRegistry: RegistryConfig{
			URL: "https://registry.example.org",
			Auth: AuthConfig{
				Username: "username",
				Password: "password",
			},
			Insecure: false,
		},
		Images: []ImageMapping{
			{
				Source:      "nginx",
				Destination: "backup/nginx",
			},
			{
				Source:      "redis",
				Destination: "backup/redis",
			},
		},
		Schedule: "0 0 * * *", // 每天午夜执行
	}

	data, err := yaml.Marshal(defaultConfig)
	if err != nil {
		return fmt.Errorf("无法序列化默认配置: %v", err)
	}

	if err := ioutil.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("无法写入默认配置文件: %v", err)
	}

	return nil
}
