package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/rapidw/image-sync/config"
)

// RegistryClient 包装Docker注册表客户端
type RegistryClient struct {
	authenticator authn.Authenticator
	options       []remote.Option
	registryURL   string
	registryType  string
	apiURL        string
	insecure      bool
}

// ImageInfo 存储镜像信息
type ImageInfo struct {
	Name string
	Tag  string
}

// VersionInfo 存储版本信息
type VersionInfo struct {
	Tag       string
	Timestamp time.Time
}

// DockerHubTagsResponse DockerHub API返回的标签响应
type DockerHubTagsResponse struct {
	Count    int            `json:"count"`
	Next     string         `json:"next"`
	Previous string         `json:"previous"`
	Results  []DockerHubTag `json:"results"`
}

// DockerHubTag DockerHub标签信息
type DockerHubTag struct {
	Name          string `json:"name"`
	FullSize      int64  `json:"full_size"`
	LastUpdated   string `json:"last_updated"`
	TagStatus     string `json:"tag_status"`
	TagLastPulled string `json:"tag_last_pulled"`
	TagLastPushed string `json:"tag_last_pushed"`
}

// HarborTagsResponse Harbor API返回的标签响应
type HarborTagsResponse []HarborTag

// HarborTag Harbor标签信息
type HarborTag struct {
	Name         string `json:"name"`
	Size         int64  `json:"size"`
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	PushTime     string `json:"push_time"`
	PullTime     string `json:"pull_time"`
}

// NewRegistryClient 创建新的注册表客户端
func NewRegistryClient(config config.RegistryConfig) (*RegistryClient, error) {
	url := config.URL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}

	var authenticator authn.Authenticator
	var options []remote.Option

	// 配置认证
	if config.Auth.Username != "" && config.Auth.Password != "" {
		authenticator = &authn.Basic{
			Username: config.Auth.Username,
			Password: config.Auth.Password,
		}
		log.Printf("为注册表 %s 配置了用户名/密码认证 (用户: %s)", url, config.Auth.Username)
	} else if config.Auth.Token != "" {
		authenticator = &authn.Bearer{
			Token: config.Auth.Token,
		}
		log.Printf("为注册表 %s 配置了Bearer Token认证", url)
	} else {
		authenticator = authn.Anonymous
		log.Printf("注册表 %s 使用匿名访问", url)
	}

	options = append(options, remote.WithAuth(authenticator))

	// 配置不安全连接
	if config.Insecure {
		options = append(options, remote.WithTransport(remote.DefaultTransport))
		log.Printf("为注册表 %s 启用了不安全连接（跳过TLS验证）", url)
	}

	return &RegistryClient{
		authenticator: authenticator,
		options:       options,
		registryURL:   url,
		registryType:  config.Type,
		apiURL:        config.ApiURL,
		insecure:      config.Insecure,
	}, nil
}

// getNewestTagFromDockerHubAPI 使用DockerHub API获取最新标签
func (r *RegistryClient) getNewestTagFromDockerHubAPI(image string) (string, time.Time, error) {
	// 解析镜像名称
	parts := strings.Split(image, "/")
	var namespace, repository string

	if len(parts) == 1 {
		// 如果只有一个部分，假设是library命名空间
		namespace = "library"
		repository = parts[0]
	} else if len(parts) == 2 {
		namespace = parts[0]
		repository = parts[1]
	} else {
		return "", time.Time{}, fmt.Errorf("无效的镜像名称格式: %s", image)
	}
	// 从registry URL构建API URL
	var apiURL string

	// 如果配置中明确指定了API URL，直接使用
	if r.apiURL != "" {
		apiURL = fmt.Sprintf("%s/v2/repositories/%s/%s/tags?page_size=50&ordering=last_updated",
			strings.TrimSuffix(r.apiURL, "/"), namespace, repository)
	} else if strings.Contains(r.registryURL, "hub.docker.com") ||
		strings.Contains(r.registryURL, "docker.io") ||
		r.registryURL == "https://registry-1.docker.io" {
		// 官方DockerHub
		apiURL = fmt.Sprintf("https://hub.docker.com/v2/repositories/%s/%s/tags?page_size=50&ordering=last_updated", namespace, repository)
	} else {
		// DockerHub镜像站点，尝试构建API URL
		// 将registry URL转换为对应的hub API URL
		baseURL := strings.TrimSuffix(r.registryURL, "/")

		// 移除常见的registry前缀，替换为hub API路径
		if strings.HasPrefix(baseURL, "https://registry.") {
			// 例如: https://registry.example.com -> https://hub.example.com
			baseURL = strings.Replace(baseURL, "https://registry.", "https://hub.", 1)
		} else if strings.HasPrefix(baseURL, "http://registry.") {
			baseURL = strings.Replace(baseURL, "http://registry.", "http://hub.", 1)
		} else if strings.Contains(baseURL, "/registry") {
			// 例如: https://example.com/registry -> https://example.com/hub
			baseURL = strings.Replace(baseURL, "/registry", "/hub", 1)
		} else {
			// 如果无法推断，尝试在现有URL基础上添加hub路径
			baseURL = baseURL + "/hub"
		}

		apiURL = fmt.Sprintf("%s/v2/repositories/%s/%s/tags?page_size=100&ordering=-last_updated", baseURL, namespace, repository)
	}

	log.Printf("正在调用DockerHub API获取镜像 %s 的标签信息... (API URL: %s)", image, apiURL)

	// 发送HTTP请求
	resp, err := http.Get(apiURL)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("调用DockerHub API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("DockerHub API返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("读取API响应失败: %v", err)
	}

	// 解析JSON响应
	var tagsResponse DockerHubTagsResponse
	if err := json.Unmarshal(body, &tagsResponse); err != nil {
		return "", time.Time{}, fmt.Errorf("解析API响应失败: %v", err)
	}

	if len(tagsResponse.Results) == 0 {
		return "", time.Time{}, fmt.Errorf("未找到镜像 %s 的标签", image)
	}

	// 获取所有有效标签的时间信息
	var versions []VersionInfo

	for _, tag := range tagsResponse.Results {
		// 跳过无效标签
		if tag.TagStatus != "active" {
			continue
		}

		// 解析时间，优先使用last_updated
		var timestamp time.Time
		timeStr := tag.LastUpdated
		if timeStr == "" {
			timeStr = tag.TagLastPushed
		}

		if timeStr != "" {
			// DockerHub API返回的时间格式通常是RFC3339
			if t, err := time.Parse(time.RFC3339, timeStr); err == nil {
				timestamp = t
			} else if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
				timestamp = t
			} else {
				log.Printf("警告: 无法解析标签 %s 的时间: %s", tag.Name, timeStr)
				continue
			}
		} else {
			log.Printf("警告: 标签 %s 没有时间信息", tag.Name)
			continue
		}

		versions = append(versions, VersionInfo{
			Tag:       tag.Name,
			Timestamp: timestamp,
		})
	}

	if len(versions) == 0 {
		return "", time.Time{}, fmt.Errorf("镜像 %s 没有有效的标签可供同步", image)
	}

	// 按时间排序，获取最新的（API已经按last_updated排序，但我们再次确认）
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})
	log.Printf("从DockerHub API获取到最新标签: %s (时间: %v)", versions[0].Tag, versions[0].Timestamp)
	return versions[0].Tag, versions[0].Timestamp, nil
}

// getNewestTagFromHarborAPI 使用Harbor API获取最新标签
func (r *RegistryClient) getNewestTagFromHarborAPI(image string) (string, time.Time, error) {
	// 解析镜像名称
	parts := strings.Split(image, "/")
	var project, repository string

	if len(parts) == 1 {
		// 如果只有一个部分，假设是library项目
		project = "library"
		repository = parts[0]
	} else if len(parts) == 2 {
		project = parts[0]
		repository = parts[1]
	} else {
		return "", time.Time{}, fmt.Errorf("无效的镜像名称格式: %s", image)
	}

	// 构建Harbor API URL
	var apiURL string

	// 如果配置中明确指定了API URL，直接使用
	if r.apiURL != "" {
		baseURL := strings.TrimSuffix(r.apiURL, "/")
		apiURL = fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=100&with_tag=true",
			baseURL, project, repository)
	} else {
		// 从registry URL构建API URL
		baseURL := strings.TrimSuffix(r.registryURL, "/")
		apiURL = fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=100&with_tag=true",
			baseURL, project, repository)
	}

	log.Printf("正在调用Harbor API获取镜像 %s 的标签信息... (API URL: %s)", image, apiURL)

	// 创建HTTP请求
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("创建HTTP请求失败: %v", err)
	}
	// 添加认证头（如果有）
	if r.authenticator != authn.Anonymous {
		authConfig, err := r.authenticator.Authorization()
		if err == nil {
			if authConfig.Username != "" && authConfig.Password != "" {
				// Harbor通常使用Basic认证
				req.SetBasicAuth(authConfig.Username, authConfig.Password)
			} else if authConfig.RegistryToken != "" {
				req.Header.Set("Authorization", "Bearer "+authConfig.RegistryToken)
			}
		}
	}

	// 发送HTTP请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("调用Harbor API失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("Harbor API返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("读取API响应失败: %v", err)
	}

	// 解析JSON响应 - Harbor artifacts API的响应格式
	var artifacts []struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
		PushTime string `json:"push_time"`
	}

	if err := json.Unmarshal(body, &artifacts); err != nil {
		return "", time.Time{}, fmt.Errorf("解析API响应失败: %v", err)
	}

	if len(artifacts) == 0 {
		return "", time.Time{}, fmt.Errorf("未找到镜像 %s 的artifacts", image)
	}

	// 获取所有有效标签的时间信息
	var versions []VersionInfo

	for _, artifact := range artifacts {
		// 解析推送时间
		var timestamp time.Time
		if artifact.PushTime != "" {
			// Harbor API返回的时间格式通常是RFC3339
			if t, err := time.Parse(time.RFC3339, artifact.PushTime); err == nil {
				timestamp = t
			} else if t, err := time.Parse(time.RFC3339Nano, artifact.PushTime); err == nil {
				timestamp = t
			} else {
				log.Printf("警告: 无法解析artifact的时间: %s", artifact.PushTime)
				continue
			}
		} else {
			log.Printf("警告: artifact没有推送时间信息")
			continue
		}

		// 为每个标签创建版本信息
		for _, tag := range artifact.Tags {
			versions = append(versions, VersionInfo{
				Tag:       tag.Name,
				Timestamp: timestamp,
			})
		}
	}

	if len(versions) == 0 {
		return "", time.Time{}, fmt.Errorf("镜像 %s 没有有效的标签可供同步", image)
	}

	// 按时间排序，获取最新的
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})

	log.Printf("从Harbor API获取到最新标签: %s (时间: %v)", versions[0].Tag, versions[0].Timestamp)
	return versions[0].Tag, versions[0].Timestamp, nil
}

// GetNewestTag 获取镜像的最新标签（按上传时间）
func (r *RegistryClient) GetNewestTag(image string) (string, time.Time, error) {
	// 根据仓库类型使用不同的API
	switch r.registryType {
	case "dockerhub":
		log.Printf("检测到DockerHub类型，使用API方式获取标签信息...")
		tag, timestamp, err := r.getNewestTagFromDockerHubAPI(image)
		if err != nil {
			log.Printf("DockerHub API调用失败，回退到传统方式: %v", err)
		} else {
			return tag, timestamp, nil
		}
	case "harbor":
		log.Printf("检测到Harbor类型，使用API方式获取标签信息...")
		tag, timestamp, err := r.getNewestTagFromHarborAPI(image)
		if err != nil {
			log.Printf("Harbor API调用失败，回退到传统方式: %v", err)
		} else {
			return tag, timestamp, nil
		}
	}

	// 传统方式：通过镜像清单获取
	return r.getNewestTagFromManifest(image)
}

// getNewestTagFromManifest 通过镜像清单获取最新标签（传统方式）
func (r *RegistryClient) getNewestTagFromManifest(image string) (string, time.Time, error) {
	// 构建镜像引用
	var imageRef string

	// 根据仓库类型构建引用
	if r.registryType == "dockerhub" && (strings.Contains(r.registryURL, "docker.io") || strings.Contains(r.registryURL, "registry-1.docker.io")) {
		imageRef = image
	} else {
		// 提取主机名
		host := strings.TrimPrefix(r.registryURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		imageRef = fmt.Sprintf("%s/%s", host, image)
	}

	log.Printf("正在获取镜像 %s 的标签列表...", imageRef)

	// 解析镜像引用
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	// 获取标签列表
	tags, err := remote.List(repo, r.options...)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("获取标签列表失败: %v", err)
	}

	if len(tags) == 0 {
		return "", time.Time{}, fmt.Errorf("未找到镜像 %s 的标签", image)
	}

	// 获取所有标签的上传时间
	var versions []VersionInfo

	for _, tag := range tags {
		taggedRef, err := name.ParseReference(fmt.Sprintf("%s:%s", imageRef, tag))
		if err != nil {
			log.Printf("警告: 解析标签引用 %s 失败: %v", tag, err)
			continue
		}

		// 获取镜像配置
		img, err := remote.Image(taggedRef, r.options...)
		if err != nil {
			log.Printf("警告: 获取镜像 %s:%s 失败: %v", imageRef, tag, err)
			continue
		}

		// 获取配置文件
		configFile, err := img.ConfigFile()
		if err != nil {
			log.Printf("警告: 获取镜像 %s:%s 的配置文件失败: %v", imageRef, tag, err)
			continue
		}

		// 获取创建时间
		uploadTime := configFile.Created.Time
		if uploadTime.IsZero() {
			// 如果配置文件中没有创建时间，尝试从历史记录中获取最新时间
			for _, h := range configFile.History {
				if !h.Created.IsZero() && h.Created.After(uploadTime) {
					uploadTime = h.Created.Time
				}
			}
		}

		if uploadTime.IsZero() {
			log.Printf("警告: 无法获取镜像 %s:%s 的创建时间", imageRef, tag)
			continue
		}

		versions = append(versions, VersionInfo{
			Tag:       tag,
			Timestamp: uploadTime,
		})
	}

	// 如果没有任何可用标签，返回错误
	if len(versions) == 0 {
		return "", time.Time{}, fmt.Errorf("镜像 %s 没有有效的标签可供同步", image)
	}

	// 按时间排序，获取最新的
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})

	return versions[0].Tag, versions[0].Timestamp, nil
}

// ImageExists 检查镜像是否存在
func (r *RegistryClient) ImageExists(imageName string, tag string) bool {
	// 构建镜像引用
	var imageRef string

	// 根据仓库类型构建引用
	if r.registryType == "dockerhub" && (strings.Contains(r.registryURL, "docker.io") || strings.Contains(r.registryURL, "registry-1.docker.io")) {
		imageRef = fmt.Sprintf("%s:%s", imageName, tag)
	} else {
		// 提取主机名
		host := strings.TrimPrefix(r.registryURL, "https://")
		host = strings.TrimPrefix(host, "http://")
		imageRef = fmt.Sprintf("%s/%s:%s", host, imageName, tag)
	}

	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return false
	}

	_, err = remote.Image(ref, r.options...)
	return err == nil
}

// RepositoryExists 检查仓库是否存在（检查是否有任何标签）
func (r *RegistryClient) RepositoryExists(imageName string) bool {
	// 尝试获取标签列表，如果成功且有标签则说明仓库存在
	_, _, err := r.GetNewestTag(imageName)
	return err == nil
}

// PullAndPushImage 从源注册表拉取镜像并推送到目标注册表
func PullAndPushImage(ctx context.Context, sourceRegistry, destRegistry *RegistryClient, sourceImage, sourceTag, destImage, destTag string) error {
	// 构建完整的镜像引用
	var srcImageRef, destImageRef string

	// 构建源镜像引用
	if sourceRegistry.registryType == "dockerhub" && (strings.Contains(sourceRegistry.registryURL, "docker.io") || strings.Contains(sourceRegistry.registryURL, "registry-1.docker.io")) {
		srcImageRef = fmt.Sprintf("%s:%s", sourceImage, sourceTag)
	} else {
		sourceHost := strings.TrimPrefix(sourceRegistry.registryURL, "https://")
		sourceHost = strings.TrimPrefix(sourceHost, "http://")
		srcImageRef = fmt.Sprintf("%s/%s:%s", sourceHost, sourceImage, sourceTag)
	}

	// 构建目标镜像引用
	if destRegistry.registryType == "dockerhub" && (strings.Contains(destRegistry.registryURL, "docker.io") || strings.Contains(destRegistry.registryURL, "registry-1.docker.io")) {
		destImageRef = fmt.Sprintf("%s:%s", destImage, destTag)
	} else {
		destHost := strings.TrimPrefix(destRegistry.registryURL, "https://")
		destHost = strings.TrimPrefix(destHost, "http://")
		destImageRef = fmt.Sprintf("%s/%s:%s", destHost, destImage, destTag)
	}

	log.Printf("开始拷贝镜像: %s -> %s", srcImageRef, destImageRef)
	log.Printf("源注册表: %s (类型: %s)", sourceRegistry.registryURL, sourceRegistry.registryType)
	log.Printf("目标注册表: %s (类型: %s)", destRegistry.registryURL, destRegistry.registryType)

	// 解析镜像引用
	srcRef, err := name.ParseReference(srcImageRef)
	if err != nil {
		return fmt.Errorf("解析源镜像引用失败: %v", err)
	}

	destRef, err := name.ParseReference(destImageRef)
	if err != nil {
		return fmt.Errorf("解析目标镜像引用失败: %v", err)
	}

	// 测试源镜像连接
	log.Printf("测试源镜像连接...")
	_, err = remote.Image(srcRef, sourceRegistry.options...)
	if err != nil {
		return fmt.Errorf("无法连接到源镜像: %v", err)
	}
	log.Printf("源镜像连接测试成功")

	// 使用 crane 进行镜像拷贝
	log.Printf("正在执行镜像拷贝...")

	// 获取源镜像
	img, err := remote.Image(srcRef, sourceRegistry.options...)
	if err != nil {
		return fmt.Errorf("获取源镜像失败: %v", err)
	}

	// 推送到目标注册表
	err = remote.Write(destRef, img, destRegistry.options...)
	if err != nil {
		return fmt.Errorf("推送镜像到目标注册表失败: %v", err)
	}

	log.Printf("成功同步镜像: %s:%s -> %s:%s", sourceImage, sourceTag, destImage, destTag)
	return nil
}