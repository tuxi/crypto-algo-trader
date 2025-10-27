package strategy

import (
	"crypto-algo-trader/internal/model"
	"crypto-algo-trader/internal/service"
	"crypto-algo-trader/pkg/ta"
	"sync"

	"go.uber.org/zap"
)

// 市场状态常量
type MarketState string

const (
	// 趋势模式 (Up or Down)
	StateStrongUpTrend   MarketState = "STRONG_UP_TREND"
	StateStrongDownTrend MarketState = "STRONG_DOWN_TREND"

	// 震荡模式
	StateHighVolRanging MarketState = "HIGH_VOL_RANGING" // 高波动震荡 (大网格/强套利)
	StateLowVolRanging  MarketState = "LOW_VOL_RANGING"  // 低波动震荡 (微幅剥头皮/超密网格)

	// 初始状态
	StateInitial MarketState = "INITIALIZING"
)

// StateMachine 结构体
type StateMachine struct {
	mu           sync.RWMutex
	CurrentState MarketState
	taClient     *ta.TACalculator
	Config       *service.StrategyConfig
	// 状态转换阈值 (可以从配置文件加载)
	TrendThreshold  float64 // 判断趋势强度的阈值，例如 H1 RSI 超过 60/40
	ATRVolThreshold float64 // 判断高/低波动的 ATR 绝对值阈值
}

// NewStateMachine 初始化状态机
func NewStateMachine(taClient *ta.TACalculator, cfg *service.StrategyConfig) *StateMachine {
	// 假设从配置或默认值初始化阈值
	return &StateMachine{
		CurrentState:    StateInitial,
		taClient:        taClient,
		Config:          cfg,
		TrendThreshold:  60.0,   // RSI 超过 60 视为潜在强势
		ATRVolThreshold: 0.0005, // 0.05% 的 ATR 阈值 (根据交易对和周期调整)
	}
}

// CheckAndTransition 是状态机驱动的核心函数
// 它主要由 H1 K线驱动，因为 H1 是我们策略切换的主要周期
func (sm *StateMachine) CheckAndTransition(kline model.KLine) {
	if kline.Interval != "1h" {
		// 状态机只由 H1 K 线驱动，忽略其他周期
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 1. 获取 H1 和 H4 的指标数据 (用于趋势过滤)
	h1Data, err := sm.taClient.GetTAData("1h")
	if err != nil {
		service.Logger.Warn("H1 TA Data not ready, skipping state transition.")
		return
	}
	h4Data, err := sm.taClient.GetTAData("4h")
	// H4 不就绪时，我们仍然可以根据 H1 判断，但会降低趋势确认的强度

	newState := sm.CurrentState

	// --- A. 趋势判断：检查是否为 STRONG_TREND ---
	isUpTrend, isDownTrend := sm.checkStrongTrend(h1Data, h4Data)

	if isUpTrend {
		newState = StateStrongUpTrend
	} else if isDownTrend {
		newState = StateStrongDownTrend
	} else {
		// --- B. 非趋势状态：归类为震荡模式 (消除 Idle) ---
		newState = sm.determineRangingMode(h1Data)
	}

	// --- C. 状态切换与日志记录 ---
	if newState != sm.CurrentState {
		service.Logger.Info(
			"!!! State Transition !!!",
			zap.String("From", string(sm.CurrentState)),
			zap.String("To", string(newState)),
			zap.Float64("H1_RSI", h1Data.RSI),
			zap.Float64("H1_ATR", h1Data.ATR),
		)
		sm.CurrentState = newState
	}
}

// checkStrongTrend 结合多周期指标判断强趋势
func (sm *StateMachine) checkStrongTrend(h1Data *ta.TAData, h4Data *ta.TAData) (isUpTrend bool, isDownTrend bool) {

	// 趋势条件 1: H1 均线排列确认 (FastMA > SlowMA)
	// 假设 FastMA=5, SlowMA=20 (从 Config.Trend 获取)
	h1TrendConfirm := h1Data.Close[len(h1Data.Close)-1] > h1Data.MA // 价格在 MA20 之上 (简化)

	// 趋势条件 2: H1 动量确认 (RSI > 60)
	h1Momentum := h1Data.RSI >= sm.TrendThreshold

	// 趋势条件 3 (过滤): H4 周期趋势一致性 (避免逆势)
	h4TrendConfirm := true // 默认允许
	if h4Data != nil && len(h4Data.Close) > 0 {
		// H4 价格必须在 H4 MA之上 (进一步简化判断)
		h4TrendConfirm = h4Data.Close[len(h4Data.Close)-1] > h4Data.MA
	}

	// 强上涨趋势：H1趋势确认 且 H1动量强 且 H4趋势不冲突
	isUpTrend = h1TrendConfirm && h1Momentum && h4TrendConfirm

	// 强下跌趋势：逻辑相反
	h1TrendDownConfirm := h1Data.Close[len(h1Data.Close)-1] < h1Data.MA
	h1DownMomentum := h1Data.RSI <= (100 - sm.TrendThreshold) // RSI <= 40

	isDownTrend = h1TrendDownConfirm && h1DownMomentum && !h4TrendConfirm // H4 趋势向下

	return isUpTrend, isDownTrend
}

// determineRangingMode 根据 H1 ATR 确定震荡模式
func (sm *StateMachine) determineRangingMode(h1Data *ta.TAData) MarketState {

	// 我们需要将 ATR 转换为百分比，例如 ATR / Price
	latestPrice := h1Data.Close[len(h1Data.Close)-1]

	// 检查价格是否有效，防止除以零
	if latestPrice == 0 {
		return StateLowVolRanging // 异常情况，先进入保守模式
	}

	// 计算 H1 周期下的百分比波动率
	percentATR := h1Data.ATR / latestPrice

	if percentATR >= sm.ATRVolThreshold {
		// 波动率高于阈值，进入高波动模式
		return StateHighVolRanging
	}

	// 波动率低，进入低波动模式（剥头皮）
	return StateLowVolRanging
}

// GetCurrentState 供信号生成器查询当前状态
func (sm *StateMachine) GetCurrentState() MarketState {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.CurrentState
}
