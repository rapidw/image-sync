package scheduler

import (
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/sync"
	"github.com/rapidw/image-sync/util"
)

// Scheduler 定时任务调度器
type Scheduler struct {
	cron       *cron.Cron
	syncer     *sync.Syncer
	config     *config.Config
	stopChan   chan bool
	parser     cron.Parser
}

// NewScheduler 创建定时任务调度器
func NewScheduler(cfg *config.Config) (*Scheduler, error) {
	syncer, err := sync.NewSyncer(cfg)
	if err != nil {
		return nil, fmt.Errorf("初始化同步器失败: %v", err)
	}

	scheduler := &Scheduler{
		cron:     cron.New(cron.WithSeconds()),
		syncer:   syncer,
		config:   cfg,
		stopChan: make(chan bool),
		parser:   cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor),
	}

	return scheduler, nil
}

// Start 启动定时任务
func (s *Scheduler) Start() error {
	// 立即执行一次同步
	log.Printf("[%s] 启动时立即执行同步任务", util.FormatTimeForLog(time.Now()))
	err := s.syncer.SyncImages()
	if err != nil {
		log.Printf("启动时同步失败: %v", err)
	} else {
		log.Printf("[%s] 启动时同步任务完成", util.FormatTimeForLog(time.Now()))
	}

	// 解析cron表达式获取下次执行间隔
	schedule, err := s.parser.Parse(s.config.Schedule)
	if err != nil {
		return fmt.Errorf("解析cron表达式失败: %v", err)
	}

	// 启动自定义调度器
	go s.customScheduler(schedule)
	
	log.Printf("定时任务已启动，调度表达式: %s (间隔模式)", s.config.Schedule)
	
	return nil
}

// customScheduler 自定义调度器，基于间隔而不是绝对时间
func (s *Scheduler) customScheduler(schedule cron.Schedule) {
	lastRun := time.Now()
	
	for {
		// 计算下次执行时间（基于上次执行时间 + 间隔）
		nextRun := schedule.Next(lastRun)
		duration := nextRun.Sub(lastRun)
		
		log.Printf("下次同步将在 %s 后执行 (即 %s)", 
			duration.String(), 
			util.FormatTimeForLog(time.Now().Add(duration)))
		
		// 等待到下次执行时间或停止信号
		select {
		case <-time.After(duration):
			// 执行同步任务
			now := time.Now()
			log.Printf("[%s] 开始执行定时同步任务", util.FormatTimeForLog(now))
			err := s.syncer.SyncImages()
			if err != nil {
				log.Printf("定时同步失败: %v", err)
			} else {
				log.Printf("[%s] 定时同步任务完成", util.FormatTimeForLog(now))
			}
			lastRun = now
			
		case <-s.stopChan:
			log.Println("收到停止信号，退出调度器")
			return
		}
	}
}

// Stop 停止定时任务
func (s *Scheduler) Stop() {
	if s.cron != nil {
		s.cron.Stop()
	}
	
	// 发送停止信号给自定义调度器
	select {
	case s.stopChan <- true:
	default:
	}
	
	log.Println("定时任务已停止")
}

// RunOnce 立即执行一次同步任务
func (s *Scheduler) RunOnce() error {
	log.Printf("[%s] 开始执行手动同步任务", util.FormatTimeForLog(time.Now()))
	err := s.syncer.SyncImages()
	if err != nil {
		return fmt.Errorf("同步任务执行失败: %v", err)
	}
	log.Printf("[%s] 手动同步任务完成", util.FormatTimeForLog(time.Now()))
	return nil
}
