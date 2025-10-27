package strategy

import (
	"context"
	"crypto-algo-trader/internal/executor"

	"crypto-algo-trader/internal/model"
	"crypto-algo-trader/internal/service"
	"crypto-algo-trader/pkg/ta"
	"math"
	"time"

	"go.uber.org/zap"
)

// SignalGenerator 负责根据市场状态和实时数据生成交易信号
type SignalGenerator struct {
	taClient *ta.TACalculator
	state    *StateMachine
	riskCfg  *service.RiskConfig
	logger   *zap.SugaredLogger

	executor executor.Executor
}

// NewSignalGenerator 初始化信号生成器
func NewSignalGenerator(taClient *ta.TACalculator, state *StateMachine, riskCfg *service.RiskConfig, logger *zap.SugaredLogger) *SignalGenerator {
	return &SignalGenerator{
		taClient: taClient,
		state:    state,
		riskCfg:  riskCfg,
		logger:   logger,
	}
}

// GenerateSignal 根据最新的 K 线和当前持仓，生成一个交易信号。
// 它是策略的核心决策入口。
func (sg *SignalGenerator) GenerateSignal(
	kline model.KLine,
	currentPosition *model.Position,
) model.Signal {

	// ----------------------------------------------------------------------
	// 1. 【策略自适应和风控参数调整】(低频、高重要性)
	// ----------------------------------------------------------------------

	// 我们约定使用 1h K 线收盘时来触发宏观策略调整。
	isHourlyKLine := kline.Interval == "1h"
	// 检查是否是每小时的 00 分钟 (即 1h K 线收盘)，或者您原有的 10 分钟间隔
	isAdaptationTime := isHourlyKLine && kline.EndTime.Minute() == 0

	// 如果您坚持使用 5m K 线来做调整，则使用以下逻辑：
	// isAdaptationTime := kline.Interval == "5m" && kline.EndTime.Minute()%10 == 0

	if isAdaptationTime {

		// --- (1.1 获取实时数据) ---
		// 注意：这里需要假设您已经实现了 GetMaxEquity 和 GetBalance (即 GetEquity)
		currentEquity, errEquity := sg.executor.GetBalance(context.Background())
		maxEquity := sg.executor.GetMaxEquity()
		records, errRecords := sg.executor.GetTradeHistory()

		if errEquity != nil || errRecords != nil {
			sg.logger.Errorf("ADAPTATION ERROR: Failed to get executor data. Equity error: %v, Records error: %v", errEquity, errRecords)
			// 即使获取数据失败，也不应该阻止信号生成
		} else if len(records) > 10 { // 至少有 10 笔交易才能有效分析

			// --- (1.2 计算核心指标) ---
			recentLossCount := sg.calculateRecentLosses(records)
			currentDrawdown := (maxEquity - currentEquity) / maxEquity

			// --- (1.3 核心自适应逻辑 - 简化版) ---

			// 优先级 1: 紧急防御 (Drawdown 突破风控线)
			if currentDrawdown >= model.MaxAllowedDrawdown { // 假设 MaxAllowedDrawdown = 0.15
				// 发生严重回撤，紧急收缩仓位。
				sg.riskCfg.PositionScaleFactor = model.MinScaleFactor // 假设 MinScaleFactor = 0.3
				sg.logger.Warnf("ADAPT: EMERGENCY DRAWDOWN (%.2f%%)! Factor set to MINIMUM (%.2f)",
					currentDrawdown*100, sg.riskCfg.PositionScaleFactor)
			} else if recentLossCount >= model.LossStreakThreshold { // 假设 LossStreakThreshold = 3
				// 短期防御：连续亏损，收缩仓位。
				sg.riskCfg.PositionScaleFactor *= 0.85
				// 确保不低于最小值
				if sg.riskCfg.PositionScaleFactor < model.MinScaleFactor {
					sg.riskCfg.PositionScaleFactor = model.MinScaleFactor
				}
				sg.logger.Warnf("ADAPT: Loss streak (%d) detected. Factor reduced to %.2f",
					recentLossCount, sg.riskCfg.PositionScaleFactor)
			} else if recentLossCount == 0 && currentDrawdown < (model.MaxAllowedDrawdown/2) && sg.riskCfg.PositionScaleFactor < model.MaxScaleFactor {
				// 进攻性调整：连续盈利且回撤小，缓慢增加风险敞口。
				sg.riskCfg.PositionScaleFactor *= 1.05
				// 确保不高于最大值
				if sg.riskCfg.PositionScaleFactor > model.MaxScaleFactor {
					sg.riskCfg.PositionScaleFactor = model.MaxScaleFactor // 假设 MaxScaleFactor = 1.5
				}
				sg.logger.Infof("ADAPT: Strong performance. Factor increased to %.2f", sg.riskCfg.PositionScaleFactor)
			}

			// 确保最终因子在边界内 (这一步在上面的逻辑中已隐式包含，但保留显式检查是最佳实践)
			sg.riskCfg.PositionScaleFactor = math.Max(model.MinScaleFactor, math.Min(model.MaxScaleFactor, sg.riskCfg.PositionScaleFactor))

		} else {
			sg.logger.Debugf("Not enough data (%d records) for strategy adaptation.", len(records))
		}
	}

	// ----------------------------------------------------------------------
	// 2. 【核心信号生成逻辑】(高频、低延迟)
	// ----------------------------------------------------------------------

	// M5 周期作为信号生成的频率 (原逻辑不变)
	if kline.Interval != "5m" {
		return model.Signal{Action: model.ActionNone}
	}

	// 确保所有指标就绪 (原逻辑不变)
	m5Data, err := sg.taClient.GetTAData("5m")
	if err != nil {
		sg.logger.Debug("M5 TA not ready for signal check")
		return model.Signal{Action: model.ActionNone}
	}

	currentState := sg.state.GetCurrentState()

	// 假设当前为 FLAT 仓位，尝试开仓信号 (原逻辑不变)
	if currentPosition.Direction == model.DirFlat {
		// 注意：sg.generateOpenSignal 内部必须使用 sg.riskCfg.PositionScaleFactor 来计算仓位大小！
		return sg.generateOpenSignal(currentState, m5Data, kline.Close)
	}

	// 假设当前为持仓状态，检查平仓/风控信号 (原逻辑不变)
	// TODO: 实现平仓/止盈止损逻辑

	return model.Signal{Action: model.ActionNone}
}

// generateOpenSignal 核心策略逻辑：根据状态生成开仓信号
func (sg *SignalGenerator) generateOpenSignal(state model.MarketState, m5Data *ta.TAData, currentPrice float64) model.Signal {

	// 1. 策略 A: 强趋势状态 (Strong Trend) -> 追随趋势
	if state == model.StateStrongUpTrend {
		// 示例信号：价格突破 M5 MA20，且 RSI 修正后再次向上
		if m5Data.Close[len(m5Data.Close)-1] > m5Data.MA {
			dir := model.DirLong
			// 2. 风险计算
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR)
			riskSignal.Action = model.ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Up Trend: MA Breakout Confirmation"
			return riskSignal
		}
	} else if state == model.StateStrongDownTrend {
		// 示例信号：价格突破 M5 MA20 向下
		if m5Data.Close[len(m5Data.Close)-1] < m5Data.MA {
			dir := model.DirShort
			// 2. 风险计算
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR)
			riskSignal.Action = model.ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Down Trend: MA Breakout Confirmation"
			return riskSignal
		}
	}

	// 2. 策略 B: 低波动震荡 (Low Vol Ranging) -> 网格/低买高卖
	if state == model.StateLowVolRanging {
		// 示例信号：价格触及 M5 BBands 下轨 (Long) 或上轨 (Short)
		// 简化：如果价格低于下轨，且 RSI < 50
		if currentPrice < m5Data.BBandsDn && m5Data.RSI < 50 {
			dir := model.DirLong
			// 使用更紧密的止损因子
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR, 0.7) // 止损因子 0.7
			riskSignal.Action = model.ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Low Vol Ranging: BBands DN Bounce"
			return riskSignal
		}
		// ... Short 信号逻辑类似
	}

	return model.Signal{Action: model.ActionNone}
}

// calculateRiskAndSize 核心风控函数：计算止损价格和仓位数量
// atrFactor 允许在不同状态下调整止损距离 (例如趋势追踪用 1.5，震荡用 0.7)
func (sg *SignalGenerator) calculateRiskAndSize(dir model.Direction, entryPrice float64, atr float64, atrFactor ...float64) model.Signal {

	factor := 1.5 // 默认使用 1.5 倍 ATR 作为止损距离
	if len(atrFactor) > 0 {
		factor = atrFactor[0]
	}

	// 1. 计算止损价格 (StopLoss Price)
	// 止损距离 (USD 价格差)
	slDistance := atr * factor

	var stopLossPrice float64
	if dir == model.DirLong {
		stopLossPrice = entryPrice - slDistance
	} else { // DirShort
		stopLossPrice = entryPrice + slDistance
	}

	// 确保止损价格有效
	if stopLossPrice <= 0 {
		sg.logger.Error("Calculated Stop Loss Price is invalid (<= 0)", zap.Float64("ATR", atr))
		return model.Signal{Action: model.ActionNone}
	}

	// 2. 计算本次交易的最大风险金额 (Risked USD)
	// 最大总资金 * 单笔交易最大风险暴露比例
	maxRisk := sg.riskCfg.MaxTotalCapital * sg.riskCfg.MaxPerTradeRisk

	// 3. 计算仓位数量 (Position Size)
	// 仓位数量 = 最大风险 / 每单位币的损失 (EntryPrice - StopLossPrice)
	// 仓位数量 (Size) = MaxRisk / |EntryPrice - StopLossPrice|

	// 注意：风控计算是基于现货/永续合约的保证金交易，忽略杠杆的影响
	// (杠杆只影响保证金，不影响止损距离和仓位风险计算)

	// 确保分母不为零
	priceDifference := math.Abs(entryPrice - stopLossPrice)
	if priceDifference == 0 {
		sg.logger.Error("Stop Loss Distance is zero, cannot calculate size.")
		return model.Signal{Action: model.ActionNone}
	}

	positionSize := maxRisk / priceDifference

	// 4. 计算止盈价格 (TakeProfit Price)
	// 假设使用固定的风险回报比 R:R = 1:1.5
	rrFactor := 1.5
	tpDistance := slDistance * rrFactor

	var takeProfitPrice float64
	if dir == model.DirLong {
		takeProfitPrice = entryPrice + tpDistance
	} else { // DirShort
		takeProfitPrice = entryPrice - tpDistance
	}

	// 5. 构造信号
	return model.Signal{
		Timestamp:       time.Now(),
		Price:           entryPrice,
		RiskedUSD:       maxRisk,
		PositionSize:    positionSize,
		StopLossPrice:   stopLossPrice,
		TakeProfitPrice: takeProfitPrice,
	}
}

const LookbackTrades = 10 // 只看最近 10 笔交易

// calculateRecentLosses 计算最近交易中的最大连续亏损次数。
// 假设 records 已经是按时间顺序排列的 (最新在尾部)。
func (sg *SignalGenerator) calculateRecentLosses(records []*model.TradeRecord) int {
	if len(records) == 0 {
		return 0
	}

	// 只看最近 N 笔交易
	startIndex := len(records) - LookbackTrades
	if startIndex < 0 {
		startIndex = 0
	}

	maxConsecutiveLosses := 0
	currentConsecutiveLosses := 0

	// 从旧到新遍历
	for i := startIndex; i < len(records); i++ {
		record := records[i]

		// 盈亏计算: 已实现盈亏 - 总手续费
		netPnL := record.RealizedPnL - record.Fee

		if netPnL < 0 {
			// 亏损
			currentConsecutiveLosses++
		} else {
			// 盈利或平手，中断连续亏损
			currentConsecutiveLosses = 0
		}

		// 更新最大连续亏损次数
		if currentConsecutiveLosses > maxConsecutiveLosses {
			maxConsecutiveLosses = currentConsecutiveLosses
		}
	}

	return maxConsecutiveLosses
}

// adaptStrategy 根据历史绩效动态调整 PositionScaleFactor
func (sg *SignalGenerator) adaptStrategy(records []*model.TradeRecord, currentEquity float64) {

	if len(records) < 10 {
		// 数据太少，不进行调整
		return
	}

	// --- 1. 计算核心指标 ---

	// (A) 连续亏损次数 (使用我们之前实现的函数)
	recentLosses := sg.calculateRecentLosses(records)

	// (B) 实时回撤 (简化计算：最大净值 - 当前净值)
	// 实际实现需要一个函数来计算历史最大权益 (Peak Equity)
	maxEquity := sg.GetMaxEquity(records)
	currentDrawdown := (maxEquity - currentEquity) / maxEquity

	// --- 2. 调整逻辑：优先级从高到低 ---

	// 优先级 1: 紧急防御 (Drawdown 突破风控线)
	if currentDrawdown >= model.MaxAllowedDrawdown {
		// 发生严重回撤，紧急收缩仓位，直到最小，以保护本金。
		sg.riskCfg.PositionScaleFactor = model.MinScaleFactor
		sg.logger.Warnf("ADAPT: EMERGENCY DRAWDOWN (%.2f%%)! Factor set to MINIMUM (%.2f)",
			currentDrawdown*100, model.MinScaleFactor)
		return // 紧急情况，直接返回
	}

	// 优先级 2: 短期防御 (连续亏损)
	if recentLosses >= model.LossStreakThreshold {
		// 策略在短期内适应不良，收缩仓位，降低风险，等待市场风格切换。
		sg.riskCfg.PositionScaleFactor *= 0.85 // 每次减少 15%
		if sg.riskCfg.PositionScaleFactor < model.MinScaleFactor {
			sg.riskCfg.PositionScaleFactor = model.MinScaleFactor
		}
		sg.logger.Warn("ADAPT: Loss streak (%d) detected. Factor reduced to %.2f",
			recentLosses, sg.riskCfg.PositionScaleFactor)
		return
	}

	// 优先级 3: 进攻性调整 (连续盈利且回撤小)
	if recentLosses == 0 && currentDrawdown < (model.MaxAllowedDrawdown/2) {
		// 连续盈利且回撤在安全范围内，开始缓慢增加风险敞口以追求更高收益。
		sg.riskCfg.PositionScaleFactor *= 1.05 // 每次增加 5%
		if sg.riskCfg.PositionScaleFactor > model.MaxScaleFactor {
			sg.riskCfg.PositionScaleFactor = model.MaxScaleFactor
		}
		sg.logger.Info("ADAPT: Strong performance. Factor increased to %.2f",
			sg.riskCfg.PositionScaleFactor)
	}

	// 最后，确保因子在边界内
	sg.riskCfg.PositionScaleFactor = math.Max(model.MinScaleFactor, math.Min(model.MaxScaleFactor, sg.riskCfg.PositionScaleFactor))
}

// 伪代码: SimulatorExecutor.getMaxEquity(records)
// 此函数需要访问 SimulatorExecutor 内部记录的历史净值序列。
func (e *SignalGenerator) GetMaxEquity(records []*model.TradeRecord) float64 {
	// 遍历 equityHistory，返回最大值
	return 0
}
