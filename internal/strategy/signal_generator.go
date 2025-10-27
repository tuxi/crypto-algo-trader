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

	// 假设当前为持仓状态，检查平仓信号
	if currentPosition.Direction != model.DirFlat {
		// 传递 MarketState 给平仓函数 (用于检查策略是否应提前退出)
		return sg.generateCloseSignal(currentState, currentPosition, m5Data, kline.Close)
	}

	return model.Signal{Action: model.ActionNone}
}

// generateOpenSignal 核心策略逻辑：根据状态生成开仓信号
func (sg *SignalGenerator) generateOpenSignal(
	state model.MarketState,
	m5Data *ta.TAData,
	currentPrice float64,
) model.Signal {
	// ATR 已经计算在 m5Data 中
	if m5Data == nil {
		sg.logger.Debug("ATR data not available for risk calculation.")
		return model.Signal{Action: model.ActionNone}
	}

	lastATR := m5Data.ATR

	// 1. 策略 A: 强趋势状态 (Strong Trend) -> 追随趋势
	if state == model.StateStrongUpTrend {
		// 信号：价格突破 M5 MA20，且 RSI 修正后再次向上
		if currentPrice > m5Data.MA {
			dir := model.DirLong
			// 使用默认的 2.0 ATR 止损乘数 (传入 0.0 表示使用默认值)
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, lastATR, 0.0)

			// 2. 风险计算
			riskSignal.Action = model.ActionOpen
			riskSignal.Symbol = m5Data.Symbol
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Up Trend: MA Breakout Confirmation"
			sg.logger.Infof("SIGNAL: OPEN %s (State: %s). Size: %.4f, SL: %.4f, TP: %.4f",
				dir, state, riskSignal.PositionSize, riskSignal.StopLossPrice, riskSignal.TakeProfitPrice)
			return riskSignal
		}
	} else if state == model.StateStrongDownTrend {
		// 信号：价格突破 M5 MA20 向下
		if currentPrice < m5Data.MA {
			dir := model.DirShort
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, lastATR, 0.0)
			// 2. 风险计算
			riskSignal.Symbol = m5Data.Symbol
			riskSignal.Action = model.ActionOpen
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Strong Down Trend: MA Breakout Confirmation"
			sg.logger.Infof("SIGNAL: OPEN %s (State: %s). Size: %.4f, SL: %.4f, TP: %.4f",
				dir, state, riskSignal.PositionSize, riskSignal.StopLossPrice, riskSignal.TakeProfitPrice)
			return riskSignal
		}
	}

	// 2. 策略 B: 低波动震荡 (Low Vol Ranging) -> 网格/低买高卖
	if state == model.StateLowVolRanging {
		// 示例信号：价格触及 M5 BBands 下轨 (Long) 或上轨 (Short)
		// 简化：如果价格低于下轨，且 RSI < 50
		if currentPrice < m5Data.BBandsDn && m5Data.RSI < 50 {
			dir := model.DirLong
			// 使用更紧密的止损因子 0.7
			riskSignal := sg.calculateRiskAndSize(dir, currentPrice, m5Data.ATR, 0.7) // 止损因子 0.7
			riskSignal.Action = model.ActionOpen
			riskSignal.Symbol = m5Data.Symbol
			riskSignal.Direction = dir
			riskSignal.SourceState = state
			riskSignal.Reason = "Low Vol Ranging: BBands DN Bounce"
			sg.logger.Infof("SIGNAL: OPEN %s (State: %s). Size: %.4f, SL: %.4f, TP: %.4f (ATR Multiplier: 0.7)",
				dir, state, riskSignal.PositionSize, riskSignal.StopLossPrice, riskSignal.TakeProfitPrice)
			return riskSignal
		}
		// ... Short 信号逻辑类似
	}

	return model.Signal{Action: model.ActionNone}
}

// calculateRiskAndSize 核心风控函数：计算止损价格和仓位数量
// atrFactor 允许在不同状态下调整止损距离 (例如趋势追踪用 1.5，震荡用 0.7)
// 注意：该函数假设 model.Signal 包含了 PositionSize, StopLossPrice, TakeProfitPrice, RiskedUSD 等字段。
func (sg *SignalGenerator) calculateRiskAndSize(
	dir model.Direction,
	entryPrice float64,
	atr float64,
	atrFactor ...float64,
) model.Signal {

	// 默认使用配置中的默认值，如果未传入 atrFactor
	factor := sg.riskCfg.DefaultStopLossATRMultiplier //  RiskConfig 中有这个字段，例如 1.5
	if len(atrFactor) > 0 {
		factor = atrFactor[0]
	}

	// 假设 RiskConfig 中定义了默认的风险回报比
	defaultRRFactor := sg.riskCfg.DefaultRiskRewardRatio //  RiskConfig 中有 DefaultRiskRewardRatio

	// 1. 计算止损价格 (StopLoss Price)
	// 止损距离 (USD 价格差)
	slDistance := atr * factor

	var stopLossPrice float64
	if dir == model.DirLong {
		stopLossPrice = entryPrice - slDistance
	} else { // PosSideShort
		stopLossPrice = entryPrice + slDistance
	}

	// 确保止损价格有效 (防止价格崩盘或浮点数计算错误)
	if stopLossPrice <= 0 {
		sg.logger.Errorw("Calculated Stop Loss Price is invalid (<= 0)",
			"ATR", atr, "Factor", factor, "EntryPrice", entryPrice)
		return model.Signal{Action: model.ActionNone}
	}

	// 2. 计算本次交易的最大风险金额 (Risked USD)
	// 最大总资金 * 单笔交易最大风险暴露比例
	// 注意：我们使用 sg.riskCfg 中的配置值
	maxRisk := sg.riskCfg.MaxTotalCapital * sg.riskCfg.MaxPerTradeRisk

	// 确保风险金额大于零
	if maxRisk <= 0 {
		sg.logger.Error("Max risk amount is zero or negative. Check MaxTotalCapital and MaxPerTradeRisk settings.")
		return model.Signal{Action: model.ActionNone}
	}

	// 3. 计算仓位数量 (Position Size)
	// 仓位数量 (Size) = MaxRisk / |EntryPrice - StopLossPrice|

	// 确保分母不为零 (即止损距离有效)
	priceDifference := math.Abs(entryPrice - stopLossPrice)
	if priceDifference == 0 || slDistance == 0 {
		sg.logger.Error("Stop Loss Distance is zero, cannot calculate size.")
		return model.Signal{Action: model.ActionNone}
	}

	// 仓位数量计算
	positionSize := maxRisk / priceDifference

	// 4. 计算止盈价格 (TakeProfit Price)
	// 止盈距离 = 止损距离 * 风险回报比
	tpDistance := slDistance * defaultRRFactor // 使用默认的 1:1.5 R:R

	var takeProfitPrice float64
	if dir == model.DirLong {
		takeProfitPrice = entryPrice + tpDistance
	} else { // PosSideShort
		takeProfitPrice = entryPrice - tpDistance
	}

	// 5. 应用自适应因子 (PositionScaleFactor)
	finalPositionSize := positionSize * sg.riskCfg.PositionScaleFactor

	// 最小仓位限制检查 (避免计算结果过小导致交易失败或无意义)
	if finalPositionSize < sg.riskCfg.MinPositionSize { // sg.cfg 中有 MinPositionSize
		sg.logger.Debugf("Calculated position size (%.4f) too small. Final Size: 0.", finalPositionSize)
		return model.Signal{Action: model.ActionNone}
	}

	// 6. 构造信号
	return model.Signal{
		Timestamp:       time.Now(),
		Price:           entryPrice,
		Direction:       dir,
		RiskedUSD:       maxRisk,
		PositionSize:    finalPositionSize, // 最终仓位
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
	maxEquity := sg.executor.GetMaxEquity()
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

// generateCloseSignal 检查当前持仓是否应该被策略性平仓 (非SL/TP平仓)。
func (sg *SignalGenerator) generateCloseSignal(
	marketState model.MarketState, // 当前的市场宏观阶段
	currentPosition *model.Position, // 当前的持仓信息
	m5Data *ta.TAData, // 最新的 TA 数据
	currentPrice float64,
) model.Signal {

	// 如果没有持仓，理论上不应该进入这个函数，但作为安全检查
	if currentPosition.Direction == model.DirFlat {
		return model.Signal{Action: model.ActionNone}
	}

	// 假设 m5Data 和 TA 已经就绪
	if m5Data == nil {
		return model.Signal{Action: model.ActionNone}
	}

	// 获取最新的 RSI 和 MACD 柱状图
	lastRSI := m5Data.RSI
	lastMACDHist := m5Data.MACDHist[len(m5Data.MACDHist)-1]

	isCloseSignal := false
	reason := ""

	// --- 策略性平仓逻辑分流 ---

	// 1. **趋势策略退出逻辑 (StateStrongUp/DownTrend)**
	// 退出条件：趋势反转或力度衰竭。
	if currentPosition.Direction == model.DirLong {
		// 多头持仓的平仓条件：
		//   a. 市场进入低波动震荡 (StateLowVolRanging)：趋势结束，转为收割
		//   b. MACD 柱状图从正转负：趋势动能反转
		if marketState == model.StateLowVolRanging || (lastMACDHist < 0 && m5Data.MACDHist[len(m5Data.MACDHist)-2] >= 0) {
			isCloseSignal = true
			reason = "Trend exhaustion/reversal detected."
		}

	} else if currentPosition.Direction == model.DirShort {
		// 空头持仓的平仓条件：
		//   a. 市场进入低波动震荡 (StateLowVolRanging)：趋势结束，转为收割
		//   b. MACD 柱状图从负转正：趋势动能反转
		if marketState == model.StateLowVolRanging || (lastMACDHist > 0 && m5Data.MACDHist[len(m5Data.MACDHist)-2] <= 0) {
			isCloseSignal = true
			reason = "Trend exhaustion/reversal detected."
		}
	}

	// 2. **震荡策略退出逻辑 (StateLowVolRanging)**
	// 退出条件：价格回到中轴区，或波动性突然增大。
	if currentPosition.SourceState == model.StateLowVolRanging { // Position 结构中记录了开仓时的状态
		// 策略性退出：RSI 回到中轴 (50附近)
		if lastRSI > 45 && lastRSI < 55 {
			// 只有当交易有盈利时才执行这种策略性平仓（避免在回撤中频繁平仓）
			// ⚠️ 检查盈亏需要 Executor 提供 Position.UnrealizedPnL 字段，这里先简化为仅看指标。
			isCloseSignal = true
			reason = "Mean reversion success: RSI returned to equilibrium."
		}
	}

	// --- 3. 构造平仓信号 ---
	if isCloseSignal {
		sg.logger.Warnf("SIGNAL: CLOSE %s position. Reason: %s", currentPosition.Direction, reason)

		return model.Signal{
			Action:       model.ActionClose,
			Symbol:       currentPosition.InstID,
			PositionSize: 0.0, // 0.0 表示平掉所有持仓（默认行为）
			Price:        currentPrice,
			Reason:       reason,
		}
	}

	return model.Signal{Action: model.ActionNone}
}
