package sync

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/registry"
	"github.com/rapidw/image-sync/util"
)

// Syncer 镜像同步器
type Syncer struct {
	sourceRegistry      *registry.RegistryClient
	destinationRegistry *registry.RegistryClient
	config              *config.Config
}

// NewSyncer 创建一个新的同步器
func NewSyncer(cfg *config.Config) (*Syncer, error) {
	sourceRegistry, err := registry.NewRegistryClient(cfg.SourceRegistry)
	if err != nil {
		return nil, fmt.Errorf("初始化源注册表客户端错误: %v", err)
	}

	destinationRegistry, err := registry.NewRegistryClient(cfg.DestinationRegistry)
	if err != nil {
		return nil, fmt.Errorf("初始化目标注册表客户端错误: %v", err)
	}

	return &Syncer{
		sourceRegistry:      sourceRegistry,
		destinationRegistry: destinationRegistry,
		config:              cfg,
	}, nil
}

// SyncImages 同步所有配置的镜像
func (s *Syncer) SyncImages() error {
	ctx := context.Background()

	// 根据模式选择不同的同步策略
	switch s.config.Mode {
	case "project":
		return s.syncProject(ctx)
	case "image", "":
		return s.syncImageMappings(ctx)
	default:
		return fmt.Errorf("不支持的同步模式: %s", s.config.Mode)
	}
}

// syncImageMappings 同步镜像映射模式（原有逻辑）
func (s *Syncer) syncImageMappings(ctx context.Context) error {
	// 遇到第一个错误就立即终止整个同步过程
	for _, imageMapping := range s.config.Images {
		if err := s.syncImage(ctx, imageMapping); err != nil {
			return fmt.Errorf("同步镜像 %s 到 %s 失败: %v", imageMapping.Source, imageMapping.Destination, err)
		}
	}

	log.Printf("所有镜像同步完成")
	return nil
}

// syncProject 同步项目模式
func (s *Syncer) syncProject(ctx context.Context) error {
	log.Printf("==========================================================================")
	log.Printf("开始执行项目模式同步，共有 %d 个项目需要同步", len(s.config.Projects))

	totalSuccessRepos := 0
	totalRepoCount := 0

	// 遍历每个项目映射
	for i, projectMapping := range s.config.Projects {
		log.Printf("++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++")
		log.Printf("开始同步第 %d/%d 个项目: %s → %s", i+1, len(s.config.Projects),
			projectMapping.Source, projectMapping.Destination)

		// 获取Harbor项目下的所有仓库
		repositories, err := s.sourceRegistry.GetHarborProjectRepositories(projectMapping.Source)
		if err != nil {
			log.Printf("获取Harbor项目 %s 仓库列表失败: %v", projectMapping.Source, err)
			continue // 继续处理下一个项目
		}

		if len(repositories) == 0 {
			log.Printf("项目 %s 下没有仓库需要同步", projectMapping.Source)
			continue
		}

		log.Printf("在项目 %s 中找到 %d 个仓库需要同步", projectMapping.Source, len(repositories))
		totalRepoCount += len(repositories)

		// 为每个仓库创建映射并同步
		projectSuccessRepos := 0
		for _, repo := range repositories {
			// 提取仓库名（去掉源项目前缀）
			repoName := strings.TrimPrefix(repo, projectMapping.Source+"/")

			imageMapping := config.ImageMapping{
				Source:      repo,                                        // 源：sourceProject/repoName
				Destination: projectMapping.Destination + "/" + repoName, // 目标：destProject/repoName
			}

			if err := s.syncImage(ctx, imageMapping); err != nil {
				log.Printf("同步仓库 %s 失败: %v", repo, err)
				// 在项目模式下，单个仓库失败不中断整个同步过程，继续同步其他仓库
				continue
			}
			projectSuccessRepos++
			totalSuccessRepos++
		}

		log.Printf("项目 %s 同步完成，成功处理 %d/%d 个仓库",
			projectMapping.Source, projectSuccessRepos, len(repositories))
	}

	log.Printf("==========================================================================")
	log.Printf("所有项目同步完成，总计成功处理 %d/%d 个仓库", totalSuccessRepos, totalRepoCount)
	return nil
}

// syncImage 同步单个镜像映射配置
func (s *Syncer) syncImage(ctx context.Context, mapping config.ImageMapping) error {
	log.Printf("==========================================================================")
	log.Printf("开始同步镜像 %s 到 %s", mapping.Source, mapping.Destination)

	// 统一使用过滤器逻辑，无过滤器时使用".*"作为默认过滤器
	filters := s.config.TagFilter
	if len(filters) == 0 {
		filters = []string{".*"} // 无过滤器时使用匹配所有标签的正则表达式
		log.Printf("没有配置标签过滤器，使用默认过滤器 '.*' 匹配所有标签")
	} else {
		// log.Printf("使用配置的过滤器，为每个过滤器分别获取最新标签")
	}

	return s.syncImageWithMultipleFilters(ctx, mapping, filters)
}

// syncImageWithMultipleFilters 为每个过滤器分别同步最新标签
func (s *Syncer) syncImageWithMultipleFilters(ctx context.Context, mapping config.ImageMapping, filters []string) error {
	// 为每个过滤器分别获取最新标签
	filterResults, err := s.sourceRegistry.GetNewestTagsForEachFilter(mapping.Source, filters)
	if err != nil {
		if errors.Is(err, registry.ErrNoMatchingTags) {
			log.Printf("跳过镜像 %s: %v", mapping.Source, err)
			return nil
		}
		return fmt.Errorf("获取源镜像过滤标签失败: %v", err)
	}

	log.Printf("找到 %d 个过滤器匹配的标签需要同步", len(filterResults))

	// 为每个匹配的标签执行同步
	successCount := 0
	for filter, versionInfo := range filterResults {
		log.Printf("--------------------------------------------------------------------------")
		log.Printf("处理过滤器 '%s' 的标签: %s (时间: %s)", filter, versionInfo.Tag, util.FormatTimeForLog(versionInfo.Timestamp))

		// 检查目标仓库是否存在该具体标签
		if s.destinationRegistry.ImageExists(mapping.Destination, versionInfo.Tag) {
			log.Printf("目标仓库中已存在镜像 %s:%s，标签已是最新状态", mapping.Destination, versionInfo.Tag)
			successCount++ // 已存在的标签也视为成功同步
			continue
		}

		log.Printf("目标仓库中不存在镜像 %s:%s，开始同步", mapping.Destination, versionInfo.Tag)
		if err := s.syncTag(ctx, mapping.Source, versionInfo.Tag, mapping.Destination, versionInfo.Tag); err != nil {
			log.Printf("同步标签 %s 失败: %v", versionInfo.Tag, err)
			continue
		}
		successCount++
	}

	log.Printf("镜像 %s 同步完成，成功同步 %d/%d 个标签", mapping.Source, successCount, len(filterResults))
	return nil
}

// syncTag 同步单个标签
func (s *Syncer) syncTag(ctx context.Context, sourceImage, sourceTag, destImage, destTag string) error {
	log.Printf("开始同步镜像&标签 %s:%s 到 %s:%s", sourceImage, sourceTag, destImage, destTag)

	// 执行镜像拉取和推送
	err := registry.PullAndPushImage(ctx, s.sourceRegistry, s.destinationRegistry, sourceImage, sourceTag, destImage, destTag)
	if err != nil {
		return fmt.Errorf("镜像同步失败: %v", err)
	}

	log.Printf("镜像同步成功: %s:%s -> %s:%s", sourceImage, sourceTag, destImage, destTag)
	return nil
}
