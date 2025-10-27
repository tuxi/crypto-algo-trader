package model

import (
	"crypto-algo-trader/internal/service"
	"go.uber.org/zap"
	"math"
	"sync"
	"time"
)

// DataEngine 负责接收 Ticker，聚合 K 线，并发送给策略层
type DataEngine struct {
	tickerChan  chan Ticker
	klineChan   chan KLine
	aggregators map[string]*KlineAggregator // 存储不同周期的聚合器
	intervals   []time.Duration             // 我们要聚合的所有周期
	symbol      string

	forwardTickerChan chan Ticker // 用于将 Ticker 转发给所有 Aggregator

	broadcasterTickerChan chan Ticker // <-- Ticker 广播通道
}

// NewDataEngine 创建并初始化 DataEngine
func NewDataEngine(tickerChan chan Ticker, symbol string) *DataEngine {
	// 定义我们需要的 K 线周期
	intervals := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		1 * time.Hour,
		4 * time.Hour,
	}

	de := &DataEngine{
		tickerChan:        tickerChan,
		klineChan:         make(chan KLine, 100),
		aggregators:       make(map[string]*KlineAggregator),
		intervals:         intervals,
		symbol:            symbol,
		forwardTickerChan: make(chan Ticker, 1000), // 更大的转发缓冲区
	}

	// 初始化所有周期的聚合器，并传入转发 Channel
	for _, interval := range intervals {
		intervalStr := service.FormatInterval(interval)
		// 每个聚合器接收同一个转发通道作为输入
		agg := NewKlineAggregator(symbol, intervalStr, de.klineChan, de.tickerChan)
		de.aggregators[intervalStr] = agg
	}

	return de
}

// Start 启动数据处理循环
func (de *DataEngine) Start() {
	service.Logger.Info("Data Engine started, monitoring ticker stream...")

	// 启动所有 K 线聚合器的 Run 循环
	for _, agg := range de.aggregators {
		go agg.Run()
	}

	// 主循环：接收原始 Ticker，过滤并转发到转发 Channel
	for ticker := range de.tickerChan {
		// 核心过滤逻辑：只处理与本 DataEngine 实例 Symbol 匹配的数据
		if ticker.Symbol != de.symbol {
			continue
		}

		// 转发给 KLine 聚合器
		// Ticker 属于本实例，转发给 K 线聚合器
		select {
		case de.forwardTickerChan <- ticker:
			// 成功发送
		default:
			service.Logger.Warn("Forward Ticker Channel Full! Dropping Ticker.",
				zap.String("Symbol", de.symbol), zap.Int64("TS", ticker.Timestamp))
		}

		// 转发给 Ticker 广播通道
		select {
		case de.broadcasterTickerChan <- ticker:
			// 发送成功
		default:
		}
	}
}

// 供 SimulatorExecutor 等需要实时 Ticker 的组件使用
func (de *DataEngine) GetBroadcasterTickerChannel() chan Ticker {
	return de.broadcasterTickerChan
}

// GetKlineChannel 供策略层调用以获取 K 线数据流
func (de *DataEngine) GetKlineChannel() chan KLine {
	return de.klineChan
}

// KlineAggregator K 线聚合器 (用于根据 Tiker 聚合特定周期和 Symbol 的 K 线)
type KlineAggregator struct {
	mu       sync.Mutex
	Symbol   string      // 所属交易对
	Interval string      // 聚合周期，如 "1m", "5m"
	Current  KLine       // 正在构建的当前 K 线
	OutChan  chan KLine  // K 线输出通道 (DataEngine 的 klineChan)
	inChan   chan Ticker // Ticker 输入通道 (DataEngine 的 forwardTickerChan)
}

// NewKlineAggregator 创建一个新的聚合器
func NewKlineAggregator(
	symbol string, // 交易对
	intervalStr string, // 聚合周期字符串，如 "1m", "5m"
	outChan chan KLine, // K 线输出通道
	inChan chan Ticker, // Ticker 输入通道
) *KlineAggregator {
	return &KlineAggregator{
		Interval: intervalStr,
		OutChan:  outChan,
		Symbol:   symbol,
		inChan:   inChan,
		Current: KLine{
			Symbol:    symbol,
			Interval:  intervalStr,
			StartTime: time.Time{}, // time.Time{} 表示零时间，即未初始化
		},
	}
}

// Run 是 KlineAggregator 的核心循环，在独立的 Goroutine 中运行。
func (agg *KlineAggregator) Run() {
	service.Logger.Info("KlineAggregator started",
		zap.String("Symbol", agg.Symbol),
		zap.String("Interval", agg.Interval))

	// 1. 启动定时器：确保在周期结束时发送 K 线，即使没有新的 Ticker
	//    这里的逻辑较为复杂，我们先使用简化版的 Ticker 驱动模式，不使用独立定时器。
	//    在 Ticker 驱动模式下，聚合器依赖 Ticker 的时间戳来判断 K 线是否完成。

	for ticker := range agg.inChan {
		if ticker.Symbol != agg.Symbol {
			continue
		}
		agg.ProcessTicker(ticker) // 在各自的 Goroutine 中处理
	}

	// 如果 inChan 关闭，退出循环
	service.Logger.Info("KlineAggregator stopped",
		zap.String("Symbol", agg.Symbol),
		zap.String("Interval", agg.Interval))
}

// ProcessTicker 负责将 Ticker 聚合到 Current KLine
func (agg *KlineAggregator) ProcessTicker(ticker Ticker) {

	agg.mu.Lock()
	defer agg.mu.Unlock()

	// 1. 计算 Ticker 应该属于哪个 K 线周期
	// 假设我们有一个工具函数来计算 K 线的起始时间
	intervalDuration := time.Duration(1) * time.Minute // 实际需要解析 agg.Interval

	// (简化：这里只处理 1m 周期，实际需要完善周期解析逻辑)
	if agg.Interval != "1m" {
		// ... (处理其他周期的起始时间计算)
	}

	// 将 Ticker 时间戳对齐到 K 线起始时间
	tickerTime := time.UnixMilli(ticker.Timestamp)
	currentKlineStart := tickerTime.Truncate(intervalDuration)

	// 2. 检查 K 线是否完成 (Close KLine)
	// 如果当前聚合器正在构建的 K 线的起始时间在 Ticker 所在的周期之前，
	// 说明之前的 K 线已完成，需要先发送。
	if !agg.Current.StartTime.IsZero() && currentKlineStart.After(agg.Current.StartTime) {

		// K 线完成，发送出去
		completedKLine := agg.Current

		// 重置 Current KLine，以 tickerTime 为基准开启新 K 线
		agg.Current = KLine{
			Symbol:    agg.Symbol,
			Interval:  agg.Interval,
			Open:      agg.Current.Close, // 新 K 线的开盘价取上一根 K 线的收盘价
			High:      ticker.Price,
			Low:       ticker.Price,
			Volume:    0,
			StartTime: currentKlineStart,
			EndTime:   currentKlineStart.Add(intervalDuration).Add(-time.Millisecond),
		}

		// 异步发送完成的 K 线
		select {
		case agg.OutChan <- completedKLine:
			// 成功发送
		default:
			service.Logger.Warn("KLine output channel full! Dropping completed KLine.",
				zap.String("Symbol", agg.Symbol), zap.String("Interval", agg.Interval))
		}
	}

	// 3. 初始化/更新当前 K 线 (Open/High/Low/Close/Volume)
	if agg.Current.StartTime.IsZero() {
		// 第一次收到 Ticker，初始化 K 线
		agg.Current = KLine{
			Symbol:    agg.Symbol,
			Interval:  agg.Interval,
			Open:      ticker.Price,
			High:      ticker.Price,
			Low:       ticker.Price,
			Volume:    0,
			StartTime: currentKlineStart,
			EndTime:   currentKlineStart.Add(intervalDuration).Add(-time.Millisecond),
		}
	}

	// 更新 OHLCV
	agg.Current.Close = ticker.Price // 最后一个 Ticker 的价格作为收盘价
	agg.Current.High = math.Max(agg.Current.High, ticker.Price)
	agg.Current.Low = math.Min(agg.Current.Low, ticker.Price)
	agg.Current.Volume += ticker.Volume // 累加交易量
}
