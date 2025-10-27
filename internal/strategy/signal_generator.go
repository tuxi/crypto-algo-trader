package strategy

import (
	"crypto-algo-trader/internal/model"
	"crypto-algo-trader/internal/service"
	"crypto-algo-trader/pkg/ta"
	"math"
	"time"

	"go.uber.org/zap"
)

// SignalGenerator 负责根据市场状态和实时数据生成交易信号
type SignalGenerator struct {
	taClient   *ta.TACalculator
	state      *StateMachine
	riskConfig *service.RiskConfig
	logger     *zap.Logger
}

// NewSignalGenerator 初始化信号生成器
func NewSignalGenerator(taClient *ta.TACalculator, state *StateMachine, riskCfg *service.RiskConfig, logger *zap.Logger) *SignalGenerator {
	return &SignalGenerator{
		taClient:   taClient,
		state:      state,
		riskConfig: riskCfg,
		logger:     logger,
	}
}

// GenerateCheck 策略核心入口：接收 K 线，判断是否生成信号
// Ticker/KLine数据流都可能触发信号生成，这里使用 M5 K 线作为主要驱动周期
func (sg *SignalGenerator) GenerateCheck(kline model.KLine, currentPosition *Position) Signal {
	// M5 周期作为信号生成的频率
	if kline.Interval != "5m" {
		return Signal{Action: ActionNone}
	}

	// 确保所有指标就绪
	m5Data, err := sg.taClient.GetTAData("5m")
	if err != nil {
		sg.logger.Debug("M5 TA not ready for signal check")
		return Signal{Action: ActionNone}
	}

	currentState := sg.state.GetCurrentState()

	// 假设当前为 FLAT 仓位，尝试开仓信号
	if currentPosition.Direction == DirFlat {
		return sg.generateOpenSignal(currentState, m5Data, kline.Close)
	}

	// 假设当前为持仓状态，检查平仓/风控信号
	// TODO: 实现平仓/止盈止损逻辑

	return Signal{Action: ActionNone}
}

// generateOpenSignal 核心策略逻辑：根据状态生成开仓信号
func (sg *SignalGenerator) generateOpenSignal(state MarketState, m5Data *ta.TAData, currentPrice float64) Signal {

	// 1. 策略 A: 强趋势状态 (Strong Trend) -> 追随趋势
	if state == StateStrongUpTrend {
		// 示例信号：价格突破 M5 MA20，且 RSI 修正后再次向上
		if m5Data.Close[len(m5Data.Close)-1] > m5Data.MA {
			dir := DirLong
			// 2. 风险计算
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR)
			riskSignal.Action = ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Up Trend: MA Breakout Confirmation"
			return riskSignal
		}
	} else if state == StateStrongDownTrend {
		// 示例信号：价格突破 M5 MA20 向下
		if m5Data.Close[len(m5Data.Close)-1] < m5Data.MA {
			dir := DirShort
			// 2. 风险计算
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR)
			riskSignal.Action = ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Down Trend: MA Breakout Confirmation"
			return riskSignal
		}
	}

	// 2. 策略 B: 低波动震荡 (Low Vol Ranging) -> 网格/低买高卖
	if state == StateLowVolRanging {
		// 示例信号：价格触及 M5 BBands 下轨 (Long) 或上轨 (Short)
		// 简化：如果价格低于下轨，且 RSI < 50
		if currentPrice < m5Data.BBandsDn && m5Data.RSI < 50 {
			dir := DirLong
			// 使用更紧密的止损因子
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR, 0.7) // 止损因子 0.7
			riskSignal.Action = ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Low Vol Ranging: BBands DN Bounce"
			return riskSignal
		}
		// ... Short 信号逻辑类似
	}

	return Signal{Action: ActionNone}
}

// calculateRiskAndSize 核心风控函数：计算止损价格和仓位数量
// atrFactor 允许在不同状态下调整止损距离 (例如趋势追踪用 1.5，震荡用 0.7)
func (sg *SignalGenerator) calculateRiskAndSize(dir Direction, entryPrice float64, atr float64, atrFactor ...float64) Signal {

	factor := 1.5 // 默认使用 1.5 倍 ATR 作为止损距离
	if len(atrFactor) > 0 {
		factor = atrFactor[0]
	}

	// 1. 计算止损价格 (StopLoss Price)
	// 止损距离 (USD 价格差)
	slDistance := atr * factor

	var stopLossPrice float64
	if dir == DirLong {
		stopLossPrice = entryPrice - slDistance
	} else { // DirShort
		stopLossPrice = entryPrice + slDistance
	}

	// 确保止损价格有效
	if stopLossPrice <= 0 {
		sg.logger.Error("Calculated Stop Loss Price is invalid (<= 0)", zap.Float64("ATR", atr))
		return Signal{Action: ActionNone}
	}

	// 2. 计算本次交易的最大风险金额 (Risked USD)
	// 最大总资金 * 单笔交易最大风险暴露比例
	maxRisk := sg.riskConfig.MaxTotalCapital * sg.riskConfig.MaxPerTradeRisk

	// 3. 计算仓位数量 (Position Size)
	// 仓位数量 = 最大风险 / 每单位币的损失 (EntryPrice - StopLossPrice)
	// 仓位数量 (Size) = MaxRisk / |EntryPrice - StopLossPrice|

	// 注意：风控计算是基于现货/永续合约的保证金交易，忽略杠杆的影响
	// (杠杆只影响保证金，不影响止损距离和仓位风险计算)

	// 确保分母不为零
	priceDifference := math.Abs(entryPrice - stopLossPrice)
	if priceDifference == 0 {
		sg.logger.Error("Stop Loss Distance is zero, cannot calculate size.")
		return Signal{Action: ActionNone}
	}

	positionSize := maxRisk / priceDifference

	// 4. 计算止盈价格 (TakeProfit Price)
	// 假设使用固定的风险回报比 R:R = 1:1.5
	rrFactor := 1.5
	tpDistance := slDistance * rrFactor

	var takeProfitPrice float64
	if dir == DirLong {
		takeProfitPrice = entryPrice + tpDistance
	} else { // DirShort
		takeProfitPrice = entryPrice - tpDistance
	}

	// 5. 构造信号
	return Signal{
		Timestamp:       time.Now(),
		Price:           entryPrice,
		RiskedUSD:       maxRisk,
		PositionSize:    positionSize,
		StopLossPrice:   stopLossPrice,
		TakeProfitPrice: takeProfitPrice,
	}
}
