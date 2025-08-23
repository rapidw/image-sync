package util

import (
	"log"
	"time"
)

// FormatTimeForLog 将UTC时间转换为北京时间并格式化用于日志显示
func FormatTimeForLog(t time.Time) string {
	// 转换为北京时间 (UTC+8)
	beijingLocation, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		// 如果加载时区失败，手动添加8小时
		beijingTime := t.Add(8 * time.Hour)
		return beijingTime.Format("2006-01-02 15:04:05 +0800 CST")
	}

	beijingTime := t.In(beijingLocation)
	return beijingTime.Format("2006-01-02 15:04:05 -0700 MST")
}

// ParseTimestamp 解析时间戳的通用函数
func ParseTimestamp(timeStr string, tagName string) (time.Time, bool) {
	if timeStr == "" {
		return time.Time{}, false
	}

	// 尝试多种时间格式
	formats := []string{time.RFC3339, time.RFC3339Nano}

	for _, format := range formats {
		if t, err := time.Parse(format, timeStr); err == nil {
			return t, true
		}
	}

	log.Printf("警告: 无法解析标签 %s 的时间: %s", tagName, timeStr)
	return time.Time{}, false
}
