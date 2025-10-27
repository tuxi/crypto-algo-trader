package service

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"log"
)

// Logger 是全局日志接口
// 在其他模块中使用：service.Logger.Info("New order placed", zap.String("order_id", id))
var Logger *zap.Logger

// InitLogger 初始化高性能的 Zap 日志
func InitLogger() {
	// 配置 Zap 日志
	config := zap.NewProductionConfig()

	// 格式化时间
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.TimeKey = "time"

	// 如果需要写入文件，可以修改 OutputPaths:
	// config.OutputPaths = []string{"stdout", "log/app.log"}

	var err error
	Logger, err = config.Build()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
}
