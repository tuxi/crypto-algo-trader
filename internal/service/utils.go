package service

import (
	"fmt"
	"strconv"
	"time"
)

func StringToFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

func StringToInt64(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

// 将 time.Duration  原(1h0m0s或者1m0s)格式化为标准的 K 线周期字符串，如 "1m", "5m", "1h"
func FormatInterval(d time.Duration) string {
	// 优先处理小时 (h)
	if d >= time.Hour && d%time.Hour == 0 {
		hours := d / time.Hour
		return fmt.Sprintf("%dh", hours)
	}

	// 接着处理分钟 (m)
	if d >= time.Minute && d%time.Minute == 0 {
		minutes := d / time.Minute
		return fmt.Sprintf("%dm", minutes)
	}

	// 接着处理秒 (s)
	if d >= time.Second && d%time.Second == 0 {
		seconds := d / time.Second
		return fmt.Sprintf("%ds", seconds)
	}

	// 默认或无法识别的，返回原始 Duration 的 String()，但通常应该避免这种情况
	return d.String()
}

// 将 K 线周期字符串解析为 time.Duration
// 例如 "1m" -> 1*time.Minute
func ParseIntervalDuration(s string) (time.Duration, error) {
	// 简单的解析逻辑，匹配末尾的 'm', 'h', 'd' 等
	if len(s) < 2 {
		return 0, fmt.Errorf("invalid interval format: %s", s)
	}

	unit := s[len(s)-1:]
	valueStr := s[:len(s)-1]

	var unitDuration time.Duration
	switch unit {
	case "m":
		unitDuration = time.Minute
	case "h":
		unitDuration = time.Hour
	case "d":
		unitDuration = 24 * time.Hour
	default:
		return 0, fmt.Errorf("unsupported interval unit: %s", unit)
	}

	var value int
	_, err := fmt.Sscanf(valueStr, "%d", &value)
	if err != nil {
		return 0, fmt.Errorf("invalid interval value: %s", valueStr)
	}

	return time.Duration(value) * unitDuration, nil
}
