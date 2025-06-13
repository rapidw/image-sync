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

	"github.com/containers/image/v5/copy"
	"github.com/containers/image/v5/docker"
	"github.com/containers/image/v5/manifest"
	"github.com/containers/image/v5/signature"
	"github.com/containers/image/v5/transports/alltransports"
	"github.com/containers/image/v5/types"
	"github.com/rapidw/image-sync/config"
)

// RegistryClient 包装Docker注册表客户端
type RegistryClient struct {
	sysCtx       *types.SystemContext
	registryURL  string
	registryType string
	apiURL       string
	insecure     bool
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

	sysCtx := &types.SystemContext{}

	// 配置认证
	if config.Auth.Username != "" && config.Auth.Password != "" {
		sysCtx.DockerAuthConfig = &types.DockerAuthConfig{
			Username: config.Auth.Username,
			Password: config.Auth.Password,
		}
		log.Printf("为注册表 %s 配置了用户名/密码认证 (用户: %s)", url, config.Auth.Username)
	} else if config.Auth.Token != "" {
		sysCtx.DockerBearerRegistryToken = config.Auth.Token
		log.Printf("为注册表 %s 配置了Bearer Token认证", url)
	} else {
		log.Printf("注册表 %s 使用匿名访问", url)
	}

	// 配置不安全连接
	if config.Insecure {
		sysCtx.DockerInsecureSkipTLSVerify = types.OptionalBoolTrue
		sysCtx.DockerDaemonInsecureSkipTLSVerify = true
		log.Printf("为注册表 %s 启用了不安全连接（跳过TLS验证）", url)
	}
	return &RegistryClient{
		sysCtx:       sysCtx,
		registryURL:  url,
		registryType: config.Type,
		apiURL:       config.ApiURL,
		insecure:     config.Insecure,
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
	if r.sysCtx.DockerAuthConfig != nil {
		// Harbor通常使用Basic认证
		req.SetBasicAuth(r.sysCtx.DockerAuthConfig.Username, r.sysCtx.DockerAuthConfig.Password)
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
	imageRef := fmt.Sprintf("docker://%s", image)

	// 解析镜像引用
	ref, err := alltransports.ParseImageName(imageRef)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	log.Printf("正在获取镜像 %s 的标签列表...", image)

	// 获取标签列表
	tags, err := docker.GetRepositoryTags(context.Background(), r.sysCtx, ref)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("获取标签列表失败: %v", err)
	}

	if len(tags) == 0 {
		return "", time.Time{}, fmt.Errorf("未找到镜像 %s 的标签", image)
	}

	// 获取所有标签的上传时间
	var versions []VersionInfo

	for _, tag := range tags {
		taggedImageRef := fmt.Sprintf("docker://%s:%s", image, tag)
		taggedRef, err := alltransports.ParseImageName(taggedImageRef)
		if err != nil {
			log.Printf("警告: 解析标签引用 %s 失败: %v", taggedImageRef, err)
			continue
		}

		// 获取镜像源
		imgSrc, err := taggedRef.NewImageSource(context.Background(), r.sysCtx)
		if err != nil {
			log.Printf("警告: 创建镜像源 %s 失败: %v", taggedImageRef, err)
			continue
		}

		// 获取清单
		manifestBlob, manifestType, err := imgSrc.GetManifest(context.Background(), nil)
		if err != nil {
			imgSrc.Close()
			log.Printf("警告: 获取清单 %s 失败: %v", taggedImageRef, err)
			continue
		}

		// 解析清单
		parsedManifest, err := manifest.FromBlob(manifestBlob, manifestType)
		if err != nil {
			imgSrc.Close()
			log.Printf("警告: 解析清单 %s 失败: %v", taggedImageRef, err)
			continue
		}

		// 尝试获取配置信息以获取创建时间
		uploadTime, err := r.getImageCreationTime(imgSrc, parsedManifest, image, tag)
		imgSrc.Close()

		if err != nil {
			log.Printf("警告: 获取镜像 %s:%s 的创建时间失败: %v", image, tag, err)
			continue
		}

		if uploadTime.IsZero() {
			return "", time.Time{}, fmt.Errorf("无法获取镜像 %s:%s 的上传时间，请检查代码中的时间提取逻辑是否适用于当前注册表", image, tag)
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

// getImageCreationTime 从镜像配置中获取创建时间
func (r *RegistryClient) getImageCreationTime(imgSrc types.ImageSource, parsedManifest manifest.Manifest, imageName, tag string) (time.Time, error) {
	// 获取配置blob
	configInfo := parsedManifest.ConfigInfo()
	if configInfo.Digest == "" {
		return time.Time{}, fmt.Errorf("清单中没有配置信息")
	}
	configBlob, _, err := imgSrc.GetBlob(context.Background(), configInfo, nil)
	if err != nil {
		return time.Time{}, fmt.Errorf("获取配置blob失败: %v", err)
	}
	defer configBlob.Close()
	// 读取配置数据
	configData, err := io.ReadAll(configBlob)
	if err != nil {
		return time.Time{}, fmt.Errorf("读取配置数据失败: %v", err)
	}

	// 解析配置JSON
	var config struct {
		Created string `json:"created"`
		History []struct {
			Created string `json:"created"`
		} `json:"history"`
	}

	if err := json.Unmarshal(configData, &config); err != nil {
		return time.Time{}, fmt.Errorf("解析配置JSON失败: %v", err)
	}

	// 尝试从配置的created字段获取时间
	if config.Created != "" {
		if t, err := time.Parse(time.RFC3339Nano, config.Created); err == nil {
			return t, nil
		} else if t, err := time.Parse(time.RFC3339, config.Created); err == nil {
			return t, nil
		}
	}

	// 如果配置中没有created字段，尝试从历史记录中获取最新时间
	var latestTime time.Time
	for _, h := range config.History {
		if h.Created != "" {
			if t, err := time.Parse(time.RFC3339Nano, h.Created); err == nil {
				if t.After(latestTime) {
					latestTime = t
				}
			} else if t, err := time.Parse(time.RFC3339, h.Created); err == nil {
				if t.After(latestTime) {
					latestTime = t
				}
			}
		}
	}

	if !latestTime.IsZero() {
		return latestTime, nil
	}

	// 打印配置信息用于调试
	configJSON, _ := json.MarshalIndent(config, "", "  ")
	log.Printf("警告: 镜像 %s:%s 的配置中没有找到有效的时间信息。配置结构: %s", imageName, tag, string(configJSON))

	return time.Time{}, fmt.Errorf("配置中没有找到有效的时间信息")
}

// ImageExists 检查镜像是否存在
func (r *RegistryClient) ImageExists(imageName string, tag string) bool {
	imageRef := fmt.Sprintf("docker://%s:%s", imageName, tag)
	ref, err := alltransports.ParseImageName(imageRef)
	if err != nil {
		return false
	}

	imgSrc, err := ref.NewImageSource(context.Background(), r.sysCtx)
	if err != nil {
		return false
	}
	defer imgSrc.Close()

	_, _, err = imgSrc.GetManifest(context.Background(), nil)
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
	// 构建完整的镜像引用，包含仓库域名
	var srcImageRef, destImageRef string
	var sourceHost, destHost string

	// 构建源镜像引用
	// 如果url以https开头，则去掉https
	if strings.HasPrefix(sourceRegistry.registryURL, "https://") {
		sourceHost = strings.TrimPrefix(sourceRegistry.registryURL, "https://")
	}
	// 如果url以http开头，则去掉http
	if strings.HasPrefix(sourceRegistry.registryURL, "http://") {
		sourceHost = strings.TrimPrefix(sourceRegistry.registryURL, "http://")
	}
	if sourceRegistry.registryType == "dockerhub" && (sourceHost == "registry-1.docker.io" || sourceHost == "docker.io") {
		// 对于官方DockerHub，使用标准格式
		srcImageRef = fmt.Sprintf("docker://%s:%s", sourceImage, sourceTag)
	} else {
		// 对于其他仓库，包含完整域名
		srcImageRef = fmt.Sprintf("docker://%s/%s:%s", sourceHost, sourceImage, sourceTag)
	}

	// 构建目标镜像引用
	if strings.HasPrefix(destRegistry.registryURL, "https://") {
		destHost = strings.TrimPrefix(destRegistry.registryURL, "https://")
	}
	// 如果url以http开头，则去掉http
	if strings.HasPrefix(destRegistry.registryURL, "http://") {
		destHost = strings.TrimPrefix(destRegistry.registryURL, "http://")
	}
	if destRegistry.registryType == "dockerhub" && (destHost == "registry-1.docker.io" || destHost == "docker.io") {
		// 对于官方DockerHub，使用标准格式
		destImageRef = fmt.Sprintf("docker://%s:%s", destImage, destTag)
	} else {
		// 对于其他仓库，包含完整域名
		destImageRef = fmt.Sprintf("docker://%s/%s:%s", destHost, destImage, destTag)
	}

	log.Printf("开始拷贝镜像: %s -> %s", srcImageRef, destImageRef)
	log.Printf("源注册表: %s (类型: %s)", sourceRegistry.registryURL, sourceRegistry.registryType)
	log.Printf("目标注册表: %s (类型: %s)", destRegistry.registryURL, destRegistry.registryType)

	// 解析镜像引用
	srcRef, err := alltransports.ParseImageName(srcImageRef)
	if err != nil {
		return fmt.Errorf("解析源镜像引用失败: %v", err)
	}

	destRef, err := alltransports.ParseImageName(destImageRef)
	if err != nil {
		return fmt.Errorf("解析目标镜像引用失败: %v", err)
	}

	// 创建策略上下文
	policyContext, err := signature.NewPolicyContext(&signature.Policy{Default: []signature.PolicyRequirement{signature.NewPRInsecureAcceptAnything()}})
	if err != nil {
		return fmt.Errorf("创建策略上下文失败: %v", err)
	}
	defer policyContext.Destroy()

	// 创建拷贝选项
	copyOptions := &copy.Options{
		SourceCtx:      sourceRegistry.sysCtx,
		DestinationCtx: destRegistry.sysCtx,
		ReportWriter:   nil, // 可以添加进度报告
	}

	// 检查源镜像认证配置
	if sourceRegistry.sysCtx.DockerAuthConfig != nil {
		log.Printf("源注册表使用认证 (用户: %s， 密码：%s)", sourceRegistry.sysCtx.DockerAuthConfig.Username, sourceRegistry.sysCtx.DockerAuthConfig.Password)
	} else {
		log.Printf("源注册表使用匿名访问")
	}

	// 检查目标镜像认证配置
	if destRegistry.sysCtx.DockerAuthConfig != nil {
		log.Printf("目标注册表使用认证  (用户: %s， 密码：%s)", destRegistry.sysCtx.DockerAuthConfig.Username, destRegistry.sysCtx.DockerAuthConfig.Password)
	} else {
		log.Printf("目标注册表使用匿名访问")
	}
	// 执行镜像拷贝
	log.Printf("正在执行镜像拷贝...")

	// 先测试源镜像连接
	log.Printf("测试源镜像连接...")
	srcImageSource, err := srcRef.NewImageSource(ctx, sourceRegistry.sysCtx)
	if err != nil {
		return fmt.Errorf("无法连接到源镜像: %v", err)
	}
	srcImageSource.Close()
	log.Printf("源镜像连接测试成功")

	// 再测试目标仓库连接
	log.Printf("测试目标仓库连接...")
	destImageDest, err := destRef.NewImageDestination(ctx, destRegistry.sysCtx)
	if err != nil {
		return fmt.Errorf("无法连接到目标仓库: %v", err)
	}
	destImageDest.Close()
	log.Printf("目标仓库连接测试成功")

	// 开始实际拷贝
	_, err = copy.Image(ctx, policyContext, destRef, srcRef, copyOptions)
	if err != nil {
		return fmt.Errorf("拷贝镜像失败: %v", err)
	}

	log.Printf("成功同步镜像: %s:%s -> %s:%s", sourceImage, sourceTag, destImage, destTag)
	return nil
}
