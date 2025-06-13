package sync

import (
	"context"
	"fmt"
	"log"

	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/registry"
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

	// 遇到第一个错误就立即终止整个同步过程
	for _, imageMapping := range s.config.Images {
		if err := s.syncImage(ctx, imageMapping); err != nil {
			return fmt.Errorf("同步镜像 %s 到 %s 失败: %v", imageMapping.Source, imageMapping.Destination, err)
		}
	}

	log.Printf("所有镜像同步完成")
	return nil
}

// syncImage 同步单个镜像映射配置
func (s *Syncer) syncImage(ctx context.Context, mapping config.ImageMapping) error {
	log.Printf("开始同步镜像 %s 到 %s", mapping.Source, mapping.Destination)

	// 获取源镜像的最新标签
	sourceNewestTag, sourceTime, err := s.sourceRegistry.GetNewestTag(mapping.Source)
	if err != nil {
		return fmt.Errorf("获取源镜像最新标签失败: %v", err)
	}

	log.Printf("源镜像最新标签: %s, 上传时间: %v", sourceNewestTag, sourceTime)

	// 检查目标仓库是否存在该镜像
	if !s.destinationRegistry.RepositoryExists(mapping.Destination) {
		log.Printf("目标仓库中不存在镜像 %s，将从源仓库复制最新版本", mapping.Destination)
		return s.syncImageFromScratch(ctx, mapping)
	}

	// 目标镜像存在，获取其最新标签进行比较
	destNewestTag, destTime, err := s.destinationRegistry.GetNewestTag(mapping.Destination)
	if err != nil {
		// 如果获取目标镜像标签失败，可能是临时错误，尝试从头同步
		log.Printf("获取目标镜像标签失败: %v，尝试从头同步", err)
		return s.syncImageFromScratch(ctx, mapping)
	}

	log.Printf("目标镜像最新标签: %s, 上传时间: %v", destNewestTag, destTime)

	// 比较时间戳决定是否需要同步
	if sourceTime.After(destTime) {
		log.Printf("源镜像 %s 比目标镜像 %s 更新，开始同步新版本", sourceNewestTag, destNewestTag)
		return s.syncTag(ctx, mapping.Source, sourceNewestTag, mapping.Destination, sourceNewestTag)
	} else if sourceTime.Equal(destTime) {
		log.Printf("源镜像和目标镜像版本一致 (标签: %s)，无需同步", sourceNewestTag)
	} else {
		log.Printf("目标镜像 %s 比源镜像 %s 更新（这通常不应该发生），无需同步", destNewestTag, sourceNewestTag)
	}

	return nil
}

// syncTag 同步单个标签
func (s *Syncer) syncTag(ctx context.Context, sourceImage, sourceTag, destImage, destTag string) error {
	log.Printf("开始同步镜像 %s:%s 到 %s:%s", sourceImage, sourceTag, destImage, destTag)

	// 执行镜像拉取和推送
	err := registry.PullAndPushImage(ctx, s.sourceRegistry, s.destinationRegistry, sourceImage, sourceTag, destImage, destTag)
	if err != nil {
		return fmt.Errorf("镜像同步失败: %v", err)
	}

	log.Printf("镜像同步成功: %s:%s -> %s:%s", sourceImage, sourceTag, destImage, destTag)
	return nil
}

// checkImageExists 检查镜像在目标仓库中是否存在
func (s *Syncer) checkImageExists(imageName string) bool {
	// 尝试获取镜像的最新标签，如果失败则说明镜像不存在
	_, _, err := s.destinationRegistry.GetNewestTag(imageName)
	return err == nil
}

// syncImageFromScratch 当目标仓库没有镜像时，从源仓库复制最新版本
func (s *Syncer) syncImageFromScratch(ctx context.Context, mapping config.ImageMapping) error {
	log.Printf("目标仓库中没有镜像 %s，准备从源仓库复制", mapping.Destination)

	// 获取源镜像的最新标签
	sourceNewestTag, sourceTime, err := s.sourceRegistry.GetNewestTag(mapping.Source)
	if err != nil {
		return fmt.Errorf("获取源镜像最新标签失败: %v", err)
	}

	log.Printf("发现源镜像最新标签: %s (时间: %v)", sourceNewestTag, sourceTime)
	log.Printf("开始将源镜像 %s:%s 复制到目标仓库 %s:%s", mapping.Source, sourceNewestTag, mapping.Destination, sourceNewestTag)

	// 执行同步
	return s.syncTag(ctx, mapping.Source, sourceNewestTag, mapping.Destination, sourceNewestTag)
}
