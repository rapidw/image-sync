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

// ProjectMapping 定义项目映射关系
type ProjectMapping struct {
	Source      string `yaml:"source"`      // 源项目名称
	Destination string `yaml:"destination"` // 目标项目名称
}

// Config 总体配置结构
type Config struct {
	Mode                string           `yaml:"mode,omitempty"`     // 同步模式: image, project
	Projects            []ProjectMapping `yaml:"projects,omitempty"` // 项目映射列表（project模式使用）
	SourceRegistry      RegistryConfig   `yaml:"sourceRegistry"`
	DestinationRegistry RegistryConfig   `yaml:"destinationRegistry"`
	Images              []ImageMapping   `yaml:"images"`
	TagFilter           []string         `yaml:"tagFilter,omitempty"` // 标签过滤器，正则表达式列表
	Schedule            string           `yaml:"schedule"`            // Cron表达式
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
	// 设置默认模式
	if config.Mode == "" {
		config.Mode = "image"
	}

	// 验证模式
	if config.Mode != "image" && config.Mode != "project" {
		return fmt.Errorf("模式必须是 'image' 或 'project'")
	}

	// 检查源注册表
	if config.SourceRegistry.URL == "" {
		return fmt.Errorf("源注册表 URL 不能为空")
	}

	// 检查目标注册表
	if config.DestinationRegistry.URL == "" {
		return fmt.Errorf("目标注册表 URL 不能为空")
	}

	// project模式的特殊验证
	if config.Mode == "project" {
		if config.SourceRegistry.Type != "harbor" {
			return fmt.Errorf("project模式要求源注册表类型必须是harbor")
		}
		if config.DestinationRegistry.Type != "harbor" {
			return fmt.Errorf("project模式要求目标注册表类型必须是harbor")
		}
		if len(config.Projects) == 0 {
			return fmt.Errorf("project模式需要指定至少一个项目映射")
		}

		// 验证每个项目映射
		for i, project := range config.Projects {
			if project.Source == "" {
				return fmt.Errorf("第%d个项目映射的源项目名称不能为空", i+1)
			}
			// 如果没有指定目标项目名称，使用源项目名称
			if project.Destination == "" {
				config.Projects[i].Destination = project.Source
			}
		}
	}

	// image模式需要检查镜像映射
	if config.Mode == "image" {
		if len(config.Images) == 0 {
			return fmt.Errorf("image模式至少需要一个镜像映射")
		}
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
		Mode: "image", // 默认为image模式
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
		TagFilter: []string{
			"^v?\\d+\\.\\d+\\.\\d+$", // 匹配语义化版本标签，如 1.0.0 或 v1.0.0
			"^latest$",               // 匹配 latest 标签
			"^\\d+\\.\\d+$",          // 匹配简化版本号，如 1.0
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
