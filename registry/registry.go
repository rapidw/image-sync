package registry

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/util"
)

// ErrNoMatchingTags 表示没有满足过滤器条件的标签（这不是API调用失败）
var ErrNoMatchingTags = errors.New("没有满足过滤器条件的标签")

// ImageReference 镜像引用信息
type ImageReference struct {
	Namespace  string // DockerHub的命名空间，Harbor的项目名
	Repository string // 仓库名
	FullName   string // 完整名称（namespace/repository）
}

// parseImageName 解析镜像名称，返回命名空间和仓库名
func parseImageName(image string) ImageReference {
	parts := strings.Split(image, "/")

	if len(parts) == 1 {
		// 单一部分，DockerHub默认为library命名空间，Harbor默认为library项目
		return ImageReference{
			Namespace:  "library",
			Repository: parts[0],
			FullName:   image,
		}
	} else if len(parts) == 2 {
		return ImageReference{
			Namespace:  parts[0],
			Repository: parts[1],
			FullName:   image,
		}
	} else {
		// 多级路径，取前两部分
		return ImageReference{
			Namespace:  parts[0],
			Repository: strings.Join(parts[1:], "/"),
			FullName:   image,
		}
	}
}

// buildAPIURL 构建API URL的通用函数
func (r *RegistryClient) buildAPIURL(template string, params ...interface{}) string {
	var baseURL string

	if r.apiURL != "" {
		baseURL = strings.TrimSuffix(r.apiURL, "/")
	} else {
		baseURL = r.getDefaultAPIBaseURL()
	}

	return fmt.Sprintf(template, append([]interface{}{baseURL}, params...)...)
}

// getDefaultAPIBaseURL 获取默认的API基础URL
func (r *RegistryClient) getDefaultAPIBaseURL() string {
	baseURL := strings.TrimSuffix(r.registryURL, "/")

	switch r.registryType {
	case "dockerhub":
		if strings.Contains(r.registryURL, "hub.docker.com") ||
			strings.Contains(r.registryURL, "docker.io") ||
			r.registryURL == "https://registry-1.docker.io" {
			return "https://hub.docker.com"
		}

		// DockerHub镜像站点，尝试构建API URL
		if strings.HasPrefix(baseURL, "https://registry.") {
			return strings.Replace(baseURL, "https://registry.", "https://hub.", 1)
		} else if strings.HasPrefix(baseURL, "http://registry.") {
			return strings.Replace(baseURL, "http://registry.", "http://hub.", 1)
		} else if strings.Contains(baseURL, "/registry") {
			return strings.Replace(baseURL, "/registry", "/hub", 1)
		} else {
			return baseURL + "/hub"
		}
	case "harbor":
		return baseURL
	default:
		return baseURL
	}
}

// RegistryClient 包装Docker注册表客户端
type RegistryClient struct {
	authenticator authn.Authenticator
	options       []remote.Option
	registryURL   string
	registryType  string
	apiURL        string
	insecure      bool
	httpClient    *HTTPClient
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

// HarborRepository Harbor仓库信息
type HarborRepository struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	ProjectID     int    `json:"project_id"`
	Description   string `json:"description"`
	PullCount     int64  `json:"pull_count"`
	ArtifactCount int    `json:"artifact_count"`
	CreationTime  string `json:"creation_time"`
	UpdateTime    string `json:"update_time"`
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
		// 创建跳过TLS验证的Transport
		insecureTransport := &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
		options = append(options, remote.WithTransport(insecureTransport))
		log.Printf("为注册表 %s 启用了不安全连接（跳过TLS验证）", url)
	}

	return &RegistryClient{
		authenticator: authenticator,
		options:       options,
		registryURL:   url,
		registryType:  config.Type,
		apiURL:        config.ApiURL,
		insecure:      config.Insecure,
		httpClient:    NewHTTPClient(authenticator, config.Insecure),
	}, nil
}

// GetNewestTag 获取镜像的最新标签（按上传时间）
func (r *RegistryClient) GetNewestTag(image string) (string, time.Time, error) {
	// 调用带过滤器的版本，传入空的过滤器数组
	return r.GetNewestTagWithFilter(image, nil)
}

// GetNewestTagWithFilter 获取满足过滤器条件的最新标签
func (r *RegistryClient) GetNewestTagWithFilter(image string, filters []string) (string, time.Time, error) {
	// 根据仓库类型使用不同的API
	switch r.registryType {
	case "dockerhub":
		// log.Printf("检测到DockerHub类型，使用API方式获取标签信息...")
		tag, timestamp, err := r.getNewestTagFromDockerHubAPIWithFilter(image, filters)

		if err != nil {
			// 如果是没有满足过滤器条件的标签，直接返回，不回退到传统方式
			if errors.Is(err, ErrNoMatchingTags) {
				return "", time.Time{}, err
			}
			log.Printf("DockerHub API调用失败，回退到传统方式: %v", err)
		} else {
			return tag, timestamp, nil
		}
	case "harbor":
		// log.Printf("检测到Harbor类型，使用API方式获取标签信息...")
		tag, timestamp, err := r.getNewestTagFromHarborAPIWithFilter(image, filters)

		if err != nil {
			// 如果是没有满足过滤器条件的标签，直接返回，不回退到传统方式
			if errors.Is(err, ErrNoMatchingTags) {
				return "", time.Time{}, err
			}
			log.Printf("Harbor API调用失败，回退到传统方式: %v", err)
		} else {
			return tag, timestamp, nil
		}
	}

	// 传统方式：通过镜像清单获取
	if len(filters) == 0 {
		return r.getNewestTagFromManifest(image)
	} else {
		return r.getNewestTagFromManifestWithFilter(image, filters)
	}
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

// filterTagsByRegex 根据正则表达式过滤标签
func filterTagsByRegex(tags []string, filters []string) []string {
	if len(filters) == 0 {
		return tags
	}

	// 编译正则表达式
	var regexps []*regexp.Regexp
	for _, filter := range filters {
		if re, err := regexp.Compile(filter); err == nil {
			regexps = append(regexps, re)
		} else {
			log.Printf("警告: 无法编译正则表达式 '%s': %v", filter, err)
		}
	}

	if len(regexps) == 0 {
		log.Printf("警告: 没有有效的正则表达式过滤器，返回所有标签")
		return tags
	}

	// 过滤标签
	var filteredTags []string
	for _, tag := range tags {
		for _, re := range regexps {
			if re.MatchString(tag) {
				filteredTags = append(filteredTags, tag)
				break // 匹配任意一个过滤器即可
			}
		}
	}

	log.Printf("原始标签数量: %d, 过滤后标签数量: %d", len(tags), len(filteredTags))
	return filteredTags
}

// getNewestTagFromDockerHubAPIWithFilter 使用DockerHub API获取满足过滤器的最新标签
func (r *RegistryClient) getNewestTagFromDockerHubAPIWithFilter(image string, filters []string) (string, time.Time, error) {
	// 使用新的优化方法
	allTags, err := r.getAllTagsFromDockerHubAPI(image)
	if err != nil {
		return "", time.Time{}, err
	}

	return r.findNewestMatchingTag(allTags, filters, image)
}

// findNewestMatchingTag 从标签列表中找到匹配过滤器的最新标签
func (r *RegistryClient) findNewestMatchingTag(allTags []VersionInfo, filters []string, image string) (string, time.Time, error) {
	// 提取标签名列表
	var tagNames []string
	tagTimeMap := make(map[string]time.Time)

	for _, versionInfo := range allTags {
		tagNames = append(tagNames, versionInfo.Tag)
		tagTimeMap[versionInfo.Tag] = versionInfo.Timestamp
	}

	// 应用过滤器
	filteredTags := filterTagsByRegex(tagNames, filters)

	if len(filteredTags) == 0 {
		return "", time.Time{}, fmt.Errorf("%w: 镜像 %s", ErrNoMatchingTags, image)
	}

	// 获取所有过滤后的标签的版本信息
	var versions []VersionInfo
	for _, tag := range filteredTags {
		if timestamp, exists := tagTimeMap[tag]; exists {
			versions = append(versions, VersionInfo{
				Tag:       tag,
				Timestamp: timestamp,
			})
		}
	}

	if len(versions) == 0 {
		return "", time.Time{}, fmt.Errorf("%w: 镜像 %s 没有有效的过滤标签可供同步", ErrNoMatchingTags, image)
	}

	// 按时间排序，获取最新的
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})

	return versions[0].Tag, versions[0].Timestamp, nil
}

// getNewestTagFromHarborAPIWithFilter 使用Harbor API获取满足过滤器的最新标签
func (r *RegistryClient) getNewestTagFromHarborAPIWithFilter(image string, filters []string) (string, time.Time, error) {
	// 使用新的优化方法
	allTags, err := r.getAllTagsFromHarborAPI(image)
	if err != nil {
		return "", time.Time{}, err
	}

	return r.findNewestMatchingTag(allTags, filters, image)
}

// getNewestTagFromManifestWithFilter 通过镜像清单获取满足过滤器的最新标签
func (r *RegistryClient) getNewestTagFromManifestWithFilter(image string, filters []string) (string, time.Time, error) {
	// 使用新的优化方法
	allTags, err := r.getAllTagsFromManifest(image)
	if err != nil {
		return "", time.Time{}, err
	}

	return r.findNewestMatchingTag(allTags, filters, image)
}

// GetHarborProjectRepositories 获取Harbor项目下的所有仓库列表
func (r *RegistryClient) GetHarborProjectRepositories(projectName string) ([]string, error) {
	if r.registryType != "harbor" {
		return nil, fmt.Errorf("只有harbor类型的注册表支持获取项目仓库列表")
	}

	// 构建Harbor API URL
	apiURL := r.buildAPIURL("%s/api/v2.0/projects/%s/repositories?page_size=100", projectName)

	log.Printf("正在调用Harbor API获取项目 %s 的仓库列表... (API URL: %s)", projectName, apiURL)

	// 发送HTTP请求
	body, err := r.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("调用Harbor API失败: %v", err)
	}

	// 解析JSON响应
	var repositories []HarborRepository
	if err := json.Unmarshal(body, &repositories); err != nil {
		return nil, fmt.Errorf("解析API响应失败: %v", err)
	}

	if len(repositories) == 0 {
		log.Printf("项目 %s 下没有找到任何仓库", projectName)
		return []string{}, nil
	}

	// 提取仓库名称
	var repoNames []string
	for _, repo := range repositories {
		// Harbor API返回的name字段包含完整路径（project/repository）
		// 我们需要提取仓库名称部分
		parts := strings.Split(repo.Name, "/")
		if len(parts) >= 2 {
			// 取最后一部分作为仓库名
			repoName := parts[len(parts)-1]
			repoNames = append(repoNames, fmt.Sprintf("%s/%s", projectName, repoName))
		} else {
			// 如果没有斜杠，直接使用完整名称
			repoNames = append(repoNames, repo.Name)
		}
	}

	log.Printf("在Harbor项目 %s 中找到 %d 个仓库: %v", projectName, len(repoNames), repoNames)
	return repoNames, nil
}

// GetNewestTagsForEachFilter 为每个过滤器分别获取最新标签
func (r *RegistryClient) GetNewestTagsForEachFilter(image string, filters []string) (map[string]VersionInfo, error) {
	// 如果没有过滤器，使用".*"作为默认过滤器
	if len(filters) == 0 {
		filters = []string{".*"}
	}

	log.Printf("正在获取镜像 %s 的所有标签信息", image)

	// 获取所有标签的信息
	allTagsInfo, err := r.getAllTagsWithTimestamps(image)
	if err != nil {
		return nil, fmt.Errorf("获取镜像 %s 的标签信息失败: %v", image, err)
	}

	if len(allTagsInfo) == 0 {
		return nil, fmt.Errorf("未找到镜像 %s 的任何标签", image)
	}

	log.Printf("获取到镜像 %s 的 %d 个标签，开始应用过滤器", image, len(allTagsInfo))

	result := make(map[string]VersionInfo)

	// 为每个过滤器分别处理标签
	for _, filter := range filters {
		log.Printf("正在为过滤器 '%s' 处理标签", filter)

		// 应用过滤器
		filteredTags := r.applyFilterToTags(allTagsInfo, filter)

		if len(filteredTags) == 0 {
			log.Printf("过滤器 '%s' 没有找到匹配的标签，跳过", filter)
			continue
		}

		// 从匹配的标签中选择最新的
		newestTag := r.getNewestFromVersions(filteredTags)
		result[filter] = newestTag
		log.Printf("过滤器 '%s' 匹配到最新标签: %s (时间: %s)", filter, newestTag.Tag, util.FormatTimeForLog(newestTag.Timestamp))
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("%w: 所有过滤器都没有找到匹配的标签", ErrNoMatchingTags)
	}

	return result, nil
}

// getAllTagsWithTimestamps 获取镜像的所有标签及其时间戳信息
func (r *RegistryClient) getAllTagsWithTimestamps(image string) ([]VersionInfo, error) {
	// 根据仓库类型使用不同的API
	switch r.registryType {
	case "dockerhub":
		return r.getAllTagsFromDockerHubAPI(image)
	case "harbor":
		return r.getAllTagsFromHarborAPI(image)
	default:
		// 传统方式：通过镜像清单获取
		return r.getAllTagsFromManifest(image)
	}
}

// getAllTagsFromDockerHubAPI 使用DockerHub API获取所有标签
func (r *RegistryClient) getAllTagsFromDockerHubAPI(image string) ([]VersionInfo, error) {
	imageRef := parseImageName(image)

	// 构建API URL
	apiURL := r.buildAPIURL("%s/v2/repositories/%s/%s/tags?page_size=100&ordering=last_updated",
		imageRef.Namespace, imageRef.Repository)

	// 发送HTTP请求
	body, err := r.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("调用DockerHub API失败: %v", err)
	}

	// 解析JSON响应
	var tagsResponse DockerHubTagsResponse
	if err := json.Unmarshal(body, &tagsResponse); err != nil {
		return nil, fmt.Errorf("解析API响应失败: %v", err)
	}

	if len(tagsResponse.Results) == 0 {
		return nil, fmt.Errorf("未找到镜像 %s 的标签", image)
	}

	// 转换为VersionInfo格式
	var versions []VersionInfo
	for _, tag := range tagsResponse.Results {
		if tag.TagStatus != "active" {
			continue
		}

		// 优先使用last_updated，回退到tag_last_pushed
		timeStr := tag.LastUpdated
		if timeStr == "" {
			timeStr = tag.TagLastPushed
		}

		if timestamp, ok := util.ParseTimestamp(timeStr, tag.Name); ok {
			versions = append(versions, VersionInfo{
				Tag:       tag.Name,
				Timestamp: timestamp,
			})
		}
	}

	return versions, nil
}

// getAllTagsFromHarborAPI 使用Harbor API获取所有标签
func (r *RegistryClient) getAllTagsFromHarborAPI(image string) ([]VersionInfo, error) {
	imageRef := parseImageName(image)

	// 构建Harbor API URL
	apiURL := r.buildAPIURL("%s/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=100&with_tag=true",
		imageRef.Namespace, imageRef.Repository)

	// 发送HTTP请求
	body, err := r.httpClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("调用Harbor API失败: %v", err)
	}

	// 解析JSON响应
	var artifacts []struct {
		Tags []struct {
			Name string `json:"name"`
		} `json:"tags"`
		PushTime string `json:"push_time"`
	}

	if err := json.Unmarshal(body, &artifacts); err != nil {
		return nil, fmt.Errorf("解析API响应失败: %v", err)
	}

	if len(artifacts) == 0 {
		return nil, fmt.Errorf("未找到镜像 %s 的artifacts", image)
	}

	// 转换为VersionInfo格式
	var versions []VersionInfo
	for _, artifact := range artifacts {
		if timestamp, ok := util.ParseTimestamp(artifact.PushTime, "artifact"); ok {
			for _, tag := range artifact.Tags {
				versions = append(versions, VersionInfo{
					Tag:       tag.Name,
					Timestamp: timestamp,
				})
			}
		}
	}

	return versions, nil
}

// buildImageRef 构建完整的镜像引用
func (r *RegistryClient) buildImageRef(image string) string {
	// 对于DockerHub，如果是官方registry，直接使用镜像名
	if r.registryType == "dockerhub" &&
		(strings.Contains(r.registryURL, "docker.io") ||
			strings.Contains(r.registryURL, "registry-1.docker.io")) {
		return image
	}

	// 其他情况，构建完整引用：host/image
	host := strings.TrimPrefix(r.registryURL, "https://")
	host = strings.TrimPrefix(host, "http://")
	return fmt.Sprintf("%s/%s", host, image)
}

// getAllTagsFromManifest 通过镜像清单获取所有标签（传统方式）
func (r *RegistryClient) getAllTagsFromManifest(image string) ([]VersionInfo, error) {
	imageRef := r.buildImageRef(image)

	// 解析镜像引用
	repo, err := name.NewRepository(imageRef)
	if err != nil {
		return nil, fmt.Errorf("解析镜像引用失败: %v", err)
	}

	// 获取标签列表
	tags, err := remote.List(repo, r.options...)
	if err != nil {
		return nil, fmt.Errorf("获取标签列表失败: %v", err)
	}

	if len(tags) == 0 {
		return nil, fmt.Errorf("未找到镜像 %s 的标签", image)
	}

	// 获取所有标签的时间戳
	var versions []VersionInfo
	for _, tag := range tags {
		if versionInfo, ok := r.getTagTimestamp(imageRef, tag); ok {
			versions = append(versions, versionInfo)
		}
	}

	return versions, nil
}

// getTagTimestamp 获取单个标签的时间戳信息
func (r *RegistryClient) getTagTimestamp(imageRef, tag string) (VersionInfo, bool) {
	taggedRef, err := name.ParseReference(fmt.Sprintf("%s:%s", imageRef, tag))
	if err != nil {
		log.Printf("警告: 解析标签引用 %s 失败: %v", tag, err)
		return VersionInfo{}, false
	}

	img, err := remote.Image(taggedRef, r.options...)
	if err != nil {
		log.Printf("警告: 获取镜像 %s:%s 失败: %v", imageRef, tag, err)
		return VersionInfo{}, false
	}

	configFile, err := img.ConfigFile()
	if err != nil {
		log.Printf("警告: 获取镜像 %s:%s 的配置文件失败: %v", imageRef, tag, err)
		return VersionInfo{}, false
	}

	uploadTime := configFile.Created.Time
	if uploadTime.IsZero() {
		for _, h := range configFile.History {
			if !h.Created.IsZero() && h.Created.After(uploadTime) {
				uploadTime = h.Created.Time
			}
		}
	}

	if uploadTime.IsZero() {
		log.Printf("警告: 无法获取镜像 %s:%s 的创建时间", imageRef, tag)
		return VersionInfo{}, false
	}

	return VersionInfo{
		Tag:       tag,
		Timestamp: uploadTime,
	}, true
}

// applyFilterToTags 对标签列表应用正则表达式过滤器
func (r *RegistryClient) applyFilterToTags(allTags []VersionInfo, filter string) []VersionInfo {
	re, err := regexp.Compile(filter)
	if err != nil {
		log.Printf("警告: 无法编译正则表达式 '%s': %v", filter, err)
		return nil
	}

	var filteredTags []VersionInfo
	for _, versionInfo := range allTags {
		if re.MatchString(versionInfo.Tag) {
			filteredTags = append(filteredTags, versionInfo)
		}
	}

	log.Printf("过滤器 '%s' 匹配了 %d/%d 个标签", filter, len(filteredTags), len(allTags))
	return filteredTags
}

// getNewestFromVersions 从版本列表中获取最新的版本
func (r *RegistryClient) getNewestFromVersions(versions []VersionInfo) VersionInfo {
	if len(versions) == 0 {
		return VersionInfo{}
	}

	// 按时间排序，获取最新的
	sort.Slice(versions, func(i, j int) bool {
		return versions[i].Timestamp.After(versions[j].Timestamp)
	})

	return versions[0]
}
