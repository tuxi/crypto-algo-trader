package executor

import "go.uber.org/zap"

// OkxConfig 定义 Okx 执行器所需的全部配置
type OkxConfig struct {
	Symbol          string
	APIKey          string
	SecretKey       string
	Passphrase      string
	RESTURL         string
	MaxTotalCapital float64
}

// OkxExecutor 结构体不变，使用新的 OkxConfig
type OkxExecutor struct {
	cfg    *OkxConfig // 使用执行器包内的配置结构
	logger *zap.SugaredLogger
}

// NewOkxExecutor 签名不变
func NewOkxExecutor(cfg *OkxConfig, logger *zap.SugaredLogger) *OkxExecutor {
	return &OkxExecutor{cfg: cfg, logger: logger}
}
