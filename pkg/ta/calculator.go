package ta

import (
	"crypto-algo-trader/internal/model"
	"fmt"
	"github.com/markcheno/go-talib"
	"sync"

	"go.uber.org/zap"
)

// TAData 存储计算指标所需的所有历史数据
type TAData struct {
	Symbol string
	Close  []float64 // 收盘价序列
	High   []float64 // 最高价序列
	Low    []float64 // 最低价序列
	Volume []float64 // 成交量序列

	// 存储最新计算出的指标值，方便外部查询
	MA       float64
	RSI      float64
	BBandsUp float64
	BBandsDn float64
	ATR      float64
	MACDHist []float64
	MACD     []float64
}

// TACalculator 负责管理所有周期的数据和指标计算
type TACalculator struct {
	mu            sync.RWMutex
	HistoryMap    map[string]*TAData // Key: K 线周期 (e.g., "1h", "15m")
	MinHistoryLen int                // 计算指标所需的最小历史长度
	Logger        *zap.SugaredLogger
}

// NewTACalculator 初始化技术指标计算器
func NewTACalculator(logger *zap.SugaredLogger) *TACalculator {
	// 假设我们所需的指标（如MA20）至少需要20根K线
	return &TACalculator{
		HistoryMap:    make(map[string]*TAData),
		MinHistoryLen: 30, // 预留安全长度
		Logger:        logger,
	}
}

// UpdateKLine 更新数据，并重新计算指标
func (tc *TACalculator) UpdateKLine(kline model.KLine) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	interval := kline.Interval

	// 初始化或获取历史数据结构
	taData, ok := tc.HistoryMap[interval]
	if !ok {
		taData = &TAData{
			Symbol: kline.Symbol,
			Close:  make([]float64, 0, 100),
			High:   make([]float64, 0, 100),
			Low:    make([]float64, 0, 100),
			Volume: make([]float64, 0, 100),
		}
		tc.HistoryMap[interval] = taData
		tc.Logger.Debug("Initialized TA history for interval", zap.String("interval", interval))
	}

	// 检查是否是新的 K 线（通常 Close price 会更新多次，完成时才算新K线）
	// 这里的逻辑简化为：如果时间戳不同，则添加新 K 线
	if len(taData.Close) > 0 && taData.Close[len(taData.Close)-1] == kline.Close {
		// 忽略重复的 K 线数据，只处理完成的 K 线
		return
	}

	// 实际项目中，需要判断是 K 线完成 (New Bar) 还是 K 线更新 (Bar Update)
	// 这里我们假设 DataEngine 传进来的是已完成的 K 线

	// 1. 更新历史数据：FIFO (先进先出) 机制
	taData.Close = append(taData.Close, kline.Close)
	taData.High = append(taData.High, kline.High)
	taData.Low = append(taData.Low, kline.Low)
	taData.Volume = append(taData.Volume, kline.Volume)

	// 保持历史数据长度，例如最多100根
	maxLen := 100
	if len(taData.Close) > maxLen {
		taData.Close = taData.Close[len(taData.Close)-maxLen:]
		taData.High = taData.High[len(taData.High)-maxLen:]
		taData.Low = taData.Low[len(taData.Low)-maxLen:]
		taData.Volume = taData.Volume[len(taData.Volume)-maxLen:]
	}

	// 2. 检查历史数据长度，进行计算
	if len(taData.Close) < tc.MinHistoryLen {
		tc.Logger.Debug("Not enough history for calculation", zap.String("interval", interval), zap.Int("len", len(taData.Close)))
		return
	}

	tc.calculate(taData)
}

// calculate 集中计算所有需要的指标
func (tc *TACalculator) calculate(taData *TAData) {
	closePrices := taData.Close

	// --- 均线 (MA 20) ---
	// 策略配置中 MA 周期可调，这里暂定为20
	maPeriod := 20
	maResult := talib.Sma(closePrices, maPeriod)
	taData.MA = maResult[len(maResult)-1] // 取最新值

	// --- 相对强弱指数 (RSI 14) ---
	rsiPeriod := 14
	rsiResult := talib.Rsi(closePrices, rsiPeriod)
	taData.RSI = rsiResult[len(rsiResult)-1]

	// --- 布林带 (BBands 20, 2) ---
	bbandsUp, _, bbandsDn := talib.BBands(closePrices, 20, 2, 2, talib.SMA)
	taData.BBandsUp = bbandsUp[len(bbandsUp)-1]
	taData.BBandsDn = bbandsDn[len(bbandsDn)-1]

	// MACD
	macd, _, hist := talib.Macd(closePrices, 12, 26, 9)
	taData.MACDHist = hist
	taData.MACD = macd

	// --- 平均真实波动范围 (ATR 14) ---
	// 注意：talib ATR 需要 High, Low, Previous Close prices
	atrResult := talib.Atr(taData.High, taData.Low, closePrices, 14)
	taData.ATR = atrResult[len(atrResult)-1]

	// 记录最新计算结果
	// tc.Logger.Debug(fmt.Sprintf("[%s] MA: %.2f, RSI: %.2f, ATR: %.4f",
	// taData.Interval, taData.MA, taData.RSI, taData.ATR))
}

// GetTAData 用于策略层查询特定周期的指标
func (tc *TACalculator) GetTAData(interval string) (*TAData, error) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	taData, ok := tc.HistoryMap[interval]
	if !ok || len(taData.Close) < tc.MinHistoryLen {
		return nil, fmt.Errorf("TA model not available or history too short for interval %s", interval)
	}
	return taData, nil
}
