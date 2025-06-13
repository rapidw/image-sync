package scheduler

import (
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/sync"
)

// Scheduler 定时任务调度器
type Scheduler struct {
	cron   *cron.Cron
	syncer *sync.Syncer
	config *config.Config
}

// NewScheduler 创建定时任务调度器
func NewScheduler(cfg *config.Config) (*Scheduler, error) {
	syncer, err := sync.NewSyncer(cfg)
	if err != nil {
		return nil, fmt.Errorf("初始化同步器失败: %v", err)
	}

	scheduler := &Scheduler{
		cron:   cron.New(cron.WithSeconds()),
		syncer: syncer,
		config: cfg,
	}

	return scheduler, nil
}

// Start 启动定时任务
func (s *Scheduler) Start() error {
	// 添加定时同步任务
	_, err := s.cron.AddFunc(s.config.Schedule, func() {
		now := time.Now().Format("2006-01-02 15:04:05")
		log.Printf("[%s] 开始执行定时同步任务", now)
		s.syncer.SyncImages()
		log.Printf("[%s] 定时同步任务完成", now)
	})

	if err != nil {
		return fmt.Errorf("添加定时任务失败: %v", err)
	}

	// 启动定时任务
	s.cron.Start()
	log.Printf("定时任务已启动，调度表达式: %s", s.config.Schedule)
	
	return nil
}

// Stop 停止定时任务
func (s *Scheduler) Stop() {
	if s.cron != nil {
		s.cron.Stop()
		log.Println("定时任务已停止")
	}
}

// RunOnce 立即执行一次同步任务
func (s *Scheduler) RunOnce() error {
	log.Println("开始执行手动同步任务")
	err := s.syncer.SyncImages()
	if err != nil {
		return fmt.Errorf("同步任务执行失败: %v", err)
	}
	log.Println("手动同步任务完成")
	return nil
}
