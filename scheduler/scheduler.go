package scheduler

import (
	"fmt"
	"log"
	"time"

	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/sync"
	"github.com/rapidw/image-sync/util"
	"github.com/robfig/cron/v3"
)

// Scheduler 定时任务调度器
type Scheduler struct {
	cron     *cron.Cron
	syncer   *sync.Syncer
	config   *config.Config
	stopChan chan bool
	parser   cron.Parser
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
	startTime := time.Now()
	log.Printf("[%s] 启动时立即执行同步任务", util.FormatTimeForLog(startTime))
	err := s.syncer.SyncImages()
	endTime := time.Now()
	if err != nil {
		log.Printf("启动时同步失败: %v", err)
	} else {
		log.Printf("[%s] 启动时同步任务完成", util.FormatTimeForLog(endTime))
	}

	// 解析cron表达式获取下次执行间隔
	schedule, err := s.parser.Parse(s.config.Schedule)
	if err != nil {
		return fmt.Errorf("解析cron表达式失败: %v", err)
	}

	// 启动自定义调度器，传入实际的同步开始时间
	go s.customScheduler(schedule, startTime)

	log.Printf("定时任务已启动，调度表达式: %s (间隔模式)", s.config.Schedule)

	return nil
}

// customScheduler 自定义调度器，基于间隔而不是绝对时间
func (s *Scheduler) customScheduler(schedule cron.Schedule, lastRunStart time.Time) {
	// 计算调度间隔
	interval := s.calculateInterval(schedule)
	
	for {
		// 计算下次同步开始时间
		nextStartTime := lastRunStart.Add(interval)
		waitDuration := nextStartTime.Sub(time.Now())
		
		// 如果计算的等待时间为负数（可能因为同步任务执行时间过长），立即执行
		if waitDuration < 0 {
			waitDuration = 0
		}
		
		log.Printf("下次同步将在 %s 后执行 (即 %s)",
			waitDuration.String(),
			util.FormatTimeForLog(nextStartTime))

		// 等待到下次执行时间或停止信号
		select {
		case <-time.After(waitDuration):
			// 执行同步任务
			now := time.Now()
			log.Printf("[%s] 开始执行定时同步任务", util.FormatTimeForLog(now))
			err := s.syncer.SyncImages()
			endTime := time.Now()
			if err != nil {
				log.Printf("定时同步失败: %v", err)
			} else {
				log.Printf("[%s] 定时同步任务完成", util.FormatTimeForLog(endTime))
			}
			// 更新上次同步开始时间
			lastRunStart = now

		case <-s.stopChan:
			log.Println("收到停止信号，退出调度器")
			return
		}
	}
}

// calculateInterval 计算cron表达式的实际间隔时间
func (s *Scheduler) calculateInterval(schedule cron.Schedule) time.Duration {
	// 使用一个基准时间来计算间隔
	baseTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	nextTime1 := schedule.Next(baseTime)
	nextTime2 := schedule.Next(nextTime1)
	return nextTime2.Sub(nextTime1)
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
