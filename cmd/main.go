package main

import (
	"crypto-algo-trader/internal/api"
	"crypto-algo-trader/internal/data"
	"crypto-algo-trader/internal/service"
	"crypto-algo-trader/internal/strategy"
	"crypto-algo-trader/pkg/ta"
	"fmt"
	"go.uber.org/zap"
	"os"
	"time"
)

func main() {
	// 1. 初始化基础服务
	service.InitLogger()
	defer service.Logger.Sync()

	// 2. 加载配置服务
	configPath := "config"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		service.Logger.Fatal("Configuration directory 'config/' not found. Please create it.")
	}
	cfg := service.LoadConfig(configPath)

	service.Logger.Info("System Initialization Complete.")
	service.Logger.Info(fmt.Sprintf("Exchange: %s, Symbol: %s", cfg.Exchange.Name, cfg.Risk.Symbol))

	// 3. 初始化数据层
	connector := api.NewConnector(cfg.Exchange.WSURL, cfg.Risk.Symbol)              // API 连接器
	dataEngine := data.NewDataEngine(connector.GetTickerChannel(), cfg.Risk.Symbol) // 数据引擎

	//  4.初始化策略层核心 ---
	taClient := ta.NewTACalculator(service.Logger) // 初始化指标计算器
	// 初始化状态机，传入 TA 客户端和策略配置
	stateMachine := strategy.NewStateMachine(taClient, &cfg.Strategy)

	// 5. 启动 Goroutine
	go connector.Start()  // 启动 WS 连接和 Ticker 接收
	go dataEngine.Start() // 启动 K 线聚合

	// 6. 主循环 (接收并处理 K 线数据，驱动更新)
	klineChan := dataEngine.GetKlineChannel()

	service.Logger.Info("Main loop started. Waiting for KLine data to update TA...")

	// 用于演示：定期打印H1指标
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			taData, err := taClient.GetTAData("1h")
			if err == nil {
				service.Logger.Info("1H TA Snapshot",
					zap.Float64("MA20", taData.MA),
					zap.Float64("RSI14", taData.RSI),
					zap.Float64("BBandUp", taData.BBandsUp),
				)
			} else {
				service.Logger.Debug("1H TA not ready", zap.Error(err))
			}
		}
	}()

	// 核心循环：接收 K 线，更新指标
	for kline := range klineChan {
		// 更新指标
		taClient.UpdateKLine(kline)
		// 状态及检查状态（仅 H1 K线会触发实际转换）
		stateMachine.CheckAndTransition(kline)
		// 演示：实时打印当前状态 (每收到一根 K 线都打印，会很频繁)
		// service.Logger.Debug("Current State", zap.String("State", string(stateMachine.GetCurrentState())))
	}

	// 保持主 Goroutine 不退出
	select {}

}
