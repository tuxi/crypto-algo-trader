// internal/service/config.go
package service

import (
	"log"

	"github.com/spf13/viper"
)

type InstanceConfig struct {
	Symbol   string
	Risk     RiskConfig
	Strategy StrategyConfig
}

type Config struct {
	Exchange  ExchangeConfig            `mapstructure:"Exchange"`
	Instances map[string]InstanceConfig `mapstructure:"Instances"`
}

// ExchangeConfig 定义了交易所的连接信息
type ExchangeConfig struct {
	Name       string
	APIKey     string
	SecretKey  string
	Passphrase string // Okx 独有
	WSURL      string
	RESTURL    string
}

// RiskConfig 定义了风控和交易对信息
type RiskConfig struct {
	MaxTotalCapital              float64
	MaxPerTradeRisk              float64
	FixedLeverage                int
	Symbol                       string
	QuoteCurrency                string
	PositionScaleFactor          float64 // 仓位缩放因子 实际仓位大小=基础仓位大小×PositionScaleFactor
	DefaultStopLossATRMultiplier float64
	DefaultRiskRewardRatio       float64
	MinPositionSize              float64
}

// StrategyConfig 定义了策略启动参数
type StrategyConfig struct {
	DefaultMode string
	Grid        struct {
		InitialSpacing float64
	}
	Trend struct {
		FastMA int
		SlowMA int
	}
}

// GlobalConfig 存储加载后的全局配置
var GlobalConfig Config

// LoadConfig 读取并解析配置文件
func LoadConfig(configPath string) *Config {
	// 设置配置文件的名称、类型和路径
	viper.SetConfigName("config") // 文件名是 config
	viper.SetConfigType("yaml")   // 文件类型是 yaml
	viper.AddConfigPath(configPath)

	// 查找并读取配置文件
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Fatalf("Config file not found: %s", err)
		} else {
			log.Fatalf("Error reading config file: %s", err)
		}
	}

	// 将配置绑定到结构体
	if err := viper.Unmarshal(&GlobalConfig); err != nil {
		log.Fatalf("Unable to decode config into struct: %s", err)
	}

	return &GlobalConfig
}
