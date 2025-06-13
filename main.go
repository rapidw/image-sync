package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/rapidw/image-sync/config"
	"github.com/rapidw/image-sync/scheduler"
)

func main() {
	var (
		configPath  string
		genConfig   bool
		runOnce     bool
		showHelp    bool
		showVersion bool
	)

	// 定义命令行参数
	flag.StringVar(&configPath, "config", "config.yaml", "配置文件路径")
	flag.StringVar(&configPath, "c", "config.yaml", "配置文件路径 (简写)")
	flag.BoolVar(&genConfig, "gen-config", false, "生成默认配置文件")
	flag.BoolVar(&genConfig, "g", false, "生成默认配置文件 (简写)")
	flag.BoolVar(&runOnce, "run-once", false, "只运行一次，不启动定时任务")
	flag.BoolVar(&runOnce, "r", false, "只运行一次，不启动定时任务 (简写)")
	flag.BoolVar(&showHelp, "help", false, "显示帮助信息")
	flag.BoolVar(&showHelp, "h", false, "显示帮助信息 (简写)")
	flag.BoolVar(&showVersion, "version", false, "显示版本信息")
	flag.BoolVar(&showVersion, "v", false, "显示版本信息 (简写)")

	// 自定义使用说明
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Docker镜像同步工具\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		fmt.Fprintf(os.Stderr, "  -c, --config <path>     配置文件路径 (默认: config.yaml)\n")
		fmt.Fprintf(os.Stderr, "  -g, --gen-config        生成默认配置文件\n")
		fmt.Fprintf(os.Stderr, "  -r, --run-once          只运行一次，不启动定时任务\n")
		fmt.Fprintf(os.Stderr, "  -h, --help              显示帮助信息\n")
		fmt.Fprintf(os.Stderr, "  -v, --version           显示版本信息\n")
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "Examples:\n")
		fmt.Fprintf(os.Stderr, "  %s -c myconfig.yaml     # 使用指定配置文件\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --gen-config         # 生成默认配置文件\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -r                   # 只运行一次同步\n", os.Args[0])
	}

	flag.Parse()

	// 显示帮助信息
	if showHelp {
		flag.Usage()
		return
	}

	// 显示版本信息
	if showVersion {
		fmt.Println("Docker Image Sync Tool v1.0.0")
		fmt.Println("基于containers/image/v5库的Docker镜像同步工具")
		return
	}

	// 生成默认配置文件
	if genConfig {
		absPath, err := filepath.Abs(configPath)
		if err != nil {
			log.Fatalf("无法获取配置文件绝对路径: %v", err)
		}

		log.Printf("生成默认配置文件: %s", absPath)
		if err := config.SaveDefaultConfig(absPath); err != nil {
			log.Fatalf("生成默认配置文件失败: %v", err)
		}

		log.Printf("默认配置文件已保存到: %s", absPath)
		return
	} // 加载配置文件
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		log.Fatalf("加载配置文件失败: %v", err)
	}

	// 创建调度器
	sched, err := scheduler.NewScheduler(cfg)
	if err != nil {
		log.Fatalf("创建调度器失败: %v", err)
	}

	// 如果只运行一次
	if runOnce {
		log.Println("运行单次同步任务...")
		err := sched.RunOnce()
		if err != nil {
			log.Fatalf("同步失败: %v", err)
		}
		log.Println("同步完成!")
		return
	} else {
		// 启动调度器
		log.Println("运行调度器任务...")
		if err := sched.Start(); err != nil {
			log.Fatalf("启动调度器失败: %v", err)
		}
		// 等待信号以优雅退出
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		log.Println("接收到退出信号，正在关闭...")
		sched.Stop()
		log.Println("已退出")
	}
}
