package main

import (
	"context"
	"crypto-algo-trader/internal/api"
	executor "crypto-algo-trader/internal/executor"
	"crypto-algo-trader/internal/model"
	"crypto-algo-trader/internal/service"
	"crypto-algo-trader/internal/strategy"
	"crypto-algo-trader/pkg/ta"
	"fmt"
	"go.uber.org/zap"
	"os"
)

func main() {
	service.InitLogger()
	defer service.Logger.Sync()

	configPath := "config"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		service.Logger.Fatal("Configuration directory 'config/' not found. Please create it.")
	}
	cfg := service.LoadConfig(configPath)

	// 1. 收集所有要订阅的 Symbol
	var symbols []string
	for _, instanceCfg := range cfg.Instances {
		symbols = append(symbols, instanceCfg.Symbol)
	}

	// 2. 初始化单个 Connector (连接器只负责连接和收集所有数据)
	connector := api.NewConnector(cfg.Exchange.WSURL, symbols)

	// 3. 启动 Connector
	go connector.Start()

	// 4. 为每个交易实例启动一个隔离的业务 Goroutine
	for instanceName, instanceCfg := range cfg.Instances {

		service.Logger.Info(fmt.Sprintf("Exchange: %s, Symbol: %s", instanceName, instanceCfg.Symbol))

		go func(name string, instance service.InstanceConfig) {
			// 使用专用的 logger
			instanceLogger := service.Logger.With(zap.String("Instance", name), zap.String("Symbol", instance.Symbol))
			instanceLogger.Info("Starting isolated trading pipeline...")

			// Ticker Input: 使用 Connector 的统一输出通道
			tickerInputChan := connector.GetTickerChannel()

			// Data Engine: 消费统一通道，但只处理自己的 Symbol
			dataEngine := model.NewDataEngine(tickerInputChan, instance.Symbol)

			// 初始化 SimulatorExecutor (注入 Ticker 源)
			// SimConfig 包含初始资金、杠杆
			simConfig := &executor.SimulatorConfig{
				InitialCapital: 10000.00, // 从配置中读取
				Leverage:       10,       // 合约默认杠杆
			}
			// 注入 DataEngine 的 Ticker 广播通道
			simulatorExecutor := executor.NewSimulatorExecutor(
				simConfig,
				dataEngine.GetBroadcasterTickerChannel(), // Ticker 源
				instanceLogger,
			)
			// 启动 SimulatorExecutor 的内部 Goroutine (实时监控 PnL 和止损)
			go simulatorExecutor.StartMonitor()

			// 初始化 TA, StateMachine, SignalGenerator
			taClient := ta.NewTACalculator(instanceLogger)
			stateMachine := strategy.NewStateMachine(taClient, &instance.Strategy)
			signalGenerator := strategy.NewSignalGenerator(taClient, stateMachine, &instance.Risk, instanceLogger)

			// 初始化交易执行器 (L3)
			// 构造 Okx Executor 所需的配置 (使用 executor.OkxConfig 结构)
			//okxConfig := &executor.OkxConfig{
			//	Symbol:          instance.Symbol,
			//	APIKey:          cfg.Exchange.APIKey,
			//	SecretKey:       cfg.Exchange.SecretKey,
			//	Passphrase:      cfg.Exchange.Passphrase,
			//	RESTURL:         cfg.Exchange.RESTURL,
			//	MaxTotalCapital: instance.Risk.MaxTotalCapital,
			//}
			//okxExecutor := executor.NewOkxExecutor(okxConfig, service.Logger)

			// 启动 DataEngine
			go dataEngine.Start()

			// 启动主循环 (消费 KLine，驱动决策和执行)
			klineChan := dataEngine.GetKlineChannel()
			for kline := range klineChan {
				// A: 更新指标
				taClient.UpdateKLine(kline)
				// B: 状态机检查状态
				stateMachine.CheckAndTransition(kline)

				// C: 获取当前持仓
				currentPosition, _ := simulatorExecutor.GetCurrentPosition(context.Background())

				// D: 信号生成检查
				signal := signalGenerator.GenerateSignal(kline, currentPosition)

				// E: 执行器执行信号
				if signal.Action != model.ActionNone {
					instanceLogger.Info("!!! NEW TRADING SIGNAL !!!", zap.String("Signal", signal.String()))
					simulatorExecutor.ExecuteSignal(context.Background(), signal)
				}
			}
		}(instanceName, instanceCfg)
	}

	// 保持主 Goroutine 不退出
	select {}
}
