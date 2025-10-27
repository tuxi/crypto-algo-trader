package executor

import (
	"context"
	"crypto-algo-trader/internal/model"
	"fmt"
	"go.uber.org/zap"
	"sync"
	"time"
)

// SimulatorConfig 模拟器配置
type SimulatorConfig struct {
	InitialCapital float64 // 初始资金
	Leverage       float64 // 杠杆倍数 (例如 10)
	FeeRate        float64 // 交易手续费率 (例如 0.0005)
}

// SimulatorPosition 模拟 Okx 的持仓数据结构
type SimulatorPosition struct {
	Symbol           string
	Side             model.Direction // Long/Short/Flat
	Size             float64         // 持仓数量
	AvgPrice         float64         // 平均开仓价格
	LiquidationPrice float64         // 强平价格 (核心风控)
	StopLossPrice    float64         // 止损价格 (由策略给出)
	TakeProfitPrice  float64         // 止盈价格 (由策略给出)
	UPL              float64         // 未实现盈亏

	EntryTime time.Time // 记录开仓时间
	EntryFee  float64   // 记录开仓手续费
}

// SimulatorExecutor 实现了 Executor 接口
type SimulatorExecutor struct {
	cfg      *SimulatorConfig
	tickerCh <-chan model.Ticker
	logger   *zap.SugaredLogger

	mu sync.RWMutex // 保护账户状态

	// 账户状态 (接近交易所的资产视图)
	balance    float64 // 账户余额 (包含已实现盈亏)
	equity     float64 // 账户净值 = 余额 + 浮动盈亏
	maxEquity  float64 // 历史最高账户净值
	marginUsed float64 // 已用保证金
	lastPrice  float64 // 实时更新的最新市场价格 (解决 ExecuteSignal 的价格依赖)

	// 持仓状态
	position *SimulatorPosition

	tradeHistory             []*model.TradeRecord // 存储所有已平仓的交易记录
	lastPriceTickerTimestamp int64                // 最新 Ticker 的时间戳 (毫秒)
}

// NewSimulatorExecutor 构造函数
func NewSimulatorExecutor(
	cfg *SimulatorConfig,
	tickerCh <-chan model.Ticker,
	logger *zap.SugaredLogger,
) *SimulatorExecutor {
	// 初始状态设置
	sim := &SimulatorExecutor{
		cfg:       cfg,
		tickerCh:  tickerCh,
		logger:    logger,
		balance:   cfg.InitialCapital,
		equity:    cfg.InitialCapital,
		maxEquity: cfg.InitialCapital,                      // <-- 初始化时，最大净值 = 初始资金
		position:  &SimulatorPosition{Side: model.DirFlat}, // 初始空仓
	}
	sim.position.Symbol = "Default" // 确保有默认Symbol

	// 假设初始价格为安全值
	sim.lastPrice = 1.0

	return sim
}

// ExecuteSignal 模拟下单和执行
func (e *SimulatorExecutor) ExecuteSignal(ctx context.Context, signal model.Signal) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	currentPrice := e.lastPrice // 使用实时监控到的最新价格

	if signal.Action == model.ActionOpen {
		// ... (开仓逻辑：计算保证金、手续费、强平价，并更新 e.position)

		requiredMargin := signal.PositionSize * currentPrice / e.cfg.Leverage
		if e.balance < requiredMargin {
			e.logger.Infof("Sim Rejected: Insufficient balance. Need: %.2f, Have: %.2f", requiredMargin, e.balance)
			return fmt.Errorf("insufficient margin")
		}

		// 扣除开仓手续费
		fee := signal.PositionSize * currentPrice * e.cfg.FeeRate
		e.balance -= fee
		e.marginUsed = requiredMargin

		// 更新持仓状态
		e.position = &SimulatorPosition{
			Symbol:           signal.Symbol,
			Side:             signal.Direction,
			Size:             signal.PositionSize,
			AvgPrice:         currentPrice,
			StopLossPrice:    signal.StopLossPrice,
			TakeProfitPrice:  signal.TakeProfitPrice,
			LiquidationPrice: e.calculateLiquidationPrice(currentPrice, signal.Direction, e.cfg.Leverage),
			EntryTime:        time.UnixMilli(e.lastPriceTickerTimestamp), // 使用最新 Ticker 时间
			EntryFee:         fee,                                        // 记录开仓手续费
		}

		e.logger.Infof("Sim ORDER FILLED (OPEN): %s %s %.4f @ %.4f. Fee: %.4f. SL: %.4f, Liq: %.4f",
			signal.Direction.String(), signal.Symbol, signal.PositionSize, currentPrice, fee, e.position.StopLossPrice, e.position.LiquidationPrice)

	} else if signal.Action == model.ActionClose && e.position.Side != model.DirFlat {
		// ... (平仓逻辑：计算已实现 PnL，扣除平仓手续费，更新 e.balance)

		// 1. 计算平仓盈亏 (PnL) 和手续费
		currentPrice := e.lastPrice
		pnl := e.calculateClosedPnL(e.position, currentPrice)
		closeFee := e.position.Size * currentPrice * e.cfg.FeeRate

		// 2. 构造交易记录
		newRecord := &model.TradeRecord{
			EntryTime:     e.position.EntryTime,
			ExitTime:      time.UnixMilli(e.lastPriceTickerTimestamp),
			Symbol:        e.position.Symbol,
			PosSide:       e.position.Side,
			EntryPrice:    e.position.AvgPrice,
			ExitPrice:     currentPrice,
			Size:          e.position.Size,
			RealizedPnL:   pnl,
			Fee:           e.position.EntryFee + closeFee,
			TriggerReason: "Signal",
		}
		e.tradeHistory = append(e.tradeHistory, newRecord)

		// 3. 更新余额，释放保证金，重置持仓
		e.balance += e.marginUsed + pnl - closeFee
		e.marginUsed = 0.0

		e.logger.Infof("Sim POSITION CLOSED: %s %s @ %.4f. Realized PnL: %.4f. New Balance: %.4f",
			e.position.Side.String(), e.position.Symbol, currentPrice, pnl, e.balance)

		// 4. 重置持仓
		e.position = &SimulatorPosition{Side: model.DirFlat}
	}

	// 每次操作后更新净值
	e.updateEquity(currentPrice)

	return nil
}

// StartMonitor 启动实时监控 Goroutine
func (e *SimulatorExecutor) StartMonitor() {
	e.logger.Info("SimulatorExecutor: Real-time PnL monitor started.")

	for ticker := range e.tickerCh {
		e.mu.Lock()

		currentPrice := ticker.Price
		e.lastPrice = currentPrice                    // 维护最新的价格供 ExecuteSignal 使用
		e.lastPriceTickerTimestamp = ticker.Timestamp // 实时更新时间戳

		// 1. 计算浮动盈亏并更新当前净值 e.equity
		e.updateEquity(currentPrice)

		// 2. 实时更新最大净值 (Max Equity) <-- 关键步骤
		if e.equity > e.maxEquity {
			e.maxEquity = e.equity
		}

		if e.position.Side != model.DirFlat {
			// 2. 检查止损 (SL) / 止盈 (TP) / 强平 (Liq) 触发
			isSLTriggered := e.checkStopLoss(currentPrice)
			isTPTriggered := e.checkTakeProfit(currentPrice)
			isLiqTriggered := e.checkLiquidation(currentPrice)

			if isSLTriggered || isTPTriggered || isLiqTriggered {

				// 模拟平仓，计算最终盈亏
				closedPnL := e.calculateClosedPnL(e.position, currentPrice)
				closeFee := e.position.Size * currentPrice * e.cfg.FeeRate

				// 构造交易记录
				triggerType := "Manual Close"
				if isSLTriggered {
					triggerType = "STOP LOSS"
				}
				if isTPTriggered {
					triggerType = "TAKE PROFIT"
				}
				if isLiqTriggered {
					triggerType = "LIQUIDATION"
				}
				if isSLTriggered {
					triggerType = "SL"
				}

				newRecord := &model.TradeRecord{
					// ... (数据填充与 ExecuteSignal 类似)
					EntryTime:     e.position.EntryTime,
					ExitTime:      time.UnixMilli(ticker.Timestamp), // 使用当前 Ticker 时间
					RealizedPnL:   closedPnL,
					TriggerReason: triggerType,
					// ...
				}
				e.tradeHistory = append(e.tradeHistory, newRecord)

				// 更新余额，释放保证金
				e.balance += e.marginUsed + closedPnL - closeFee
				e.marginUsed = 0.0

				e.logger.Infof("Sim CLOSE TRIGGERED: [%s] %s %s @ %.4f. Final PnL: %.4f. New Balance: %.4f. Equity: %.4f",
					triggerType, e.position.Side.String(), e.position.Symbol, currentPrice, closedPnL, e.balance, e.equity)

				e.position = &SimulatorPosition{Side: model.DirFlat}
			}
		}

		e.mu.Unlock()
	}
}

// calculateLiquidationPrice 计算强平价格 (简化模型，使用初始保证金率)
func (e *SimulatorExecutor) calculateLiquidationPrice(avgPrice float64, side model.Direction, leverage float64) float64 {
	if leverage <= 0 || side == model.DirFlat {
		return 0.0
	}

	// 假设初始保证金率 = 1 / 杠杆
	marginRatio := 1.0 / leverage

	// 忽略维持保证金、穿仓保障基金等复杂因素

	if side == model.DirLong {
		// 多头强平价: 价格下跌 (亏损) 导致保证金不足
		return avgPrice * (1.0 - marginRatio)
	}

	if side == model.DirShort {
		// 空头强平价: 价格上涨 (亏损) 导致保证金不足
		return avgPrice * (1.0 + marginRatio)
	}

	return 0.0
}

// calculateClosedPnL 计算已实现盈亏 (Realized PnL)
func (e *SimulatorExecutor) calculateClosedPnL(pos *SimulatorPosition, closePrice float64) float64 {
	if pos.Size == 0 || pos.Side == model.DirFlat {
		return 0.0
	}

	var pnl float64
	if pos.Side == model.DirLong {
		// 多头：平仓价高于均价则盈利
		pnl = (closePrice - pos.AvgPrice) * pos.Size
	} else { // Short
		// 空头：平仓价低于均价则盈利
		pnl = (pos.AvgPrice - closePrice) * pos.Size
	}

	return pnl
}

// updateEquity 计算浮动盈亏 (UPL) 并更新账户净值 (Equity)
func (e *SimulatorExecutor) updateEquity(currentPrice float64) {
	if e.position.Side == model.DirFlat {
		// 空仓时，净值 = 余额 (UPL = 0)
		e.equity = e.balance
		return
	}

	// 计算浮动盈亏 (Unrealized PnL)
	var upl float64
	if e.position.Side == model.DirLong {
		upl = (currentPrice - e.position.AvgPrice) * e.position.Size
	} else { // Short
		upl = (e.position.AvgPrice - currentPrice) * e.position.Size
	}
	e.position.UPL = upl
	// 更新账户净值 (Equity = Balance + UPL)
	e.equity = e.balance + upl
}

// GetTradeHistory 实现 Executor 接口
func (e *SimulatorExecutor) GetTradeHistory() ([]*model.TradeRecord, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 返回记录的副本，防止外部修改
	records := make([]*model.TradeRecord, len(e.tradeHistory))
	copy(records, e.tradeHistory)
	return records, nil
}

// internal/executor/simulator_executor.go

// checkStopLoss 检查是否触发止损
func (e *SimulatorExecutor) checkStopLoss(currentPrice float64) bool {
	// 检查是否有持仓，且设置了止损价
	if e.position.Side == model.DirFlat || e.position.StopLossPrice == 0.0 {
		return false
	}

	if e.position.Side == model.DirLong {
		// 多头止损：当前价格 <= 止损价
		// 价格下跌
		return currentPrice <= e.position.StopLossPrice
	}

	if e.position.Side == model.DirShort {
		// 空头止损：当前价格 >= 止损价
		// 价格上涨
		return currentPrice >= e.position.StopLossPrice
	}

	return false
}

// internal/executor/simulator_executor.go

// checkTakeProfit 检查是否触发止盈
func (e *SimulatorExecutor) checkTakeProfit(currentPrice float64) bool {
	// 检查是否有持仓，且设置了止盈价
	if e.position.Side == model.DirFlat || e.position.TakeProfitPrice == 0.0 {
		return false
	}

	if e.position.Side == model.DirLong {
		// 多头止盈：当前价格 >= 止盈价
		// 价格上涨
		return currentPrice >= e.position.TakeProfitPrice
	}

	if e.position.Side == model.DirShort {
		// 空头止盈：当前价格 <= 止盈价
		// 价格下跌
		return currentPrice <= e.position.TakeProfitPrice
	}

	return false
}

// internal/executor/simulator_executor.go

// checkLiquidation 检查是否触发强平
func (e *SimulatorExecutor) checkLiquidation(currentPrice float64) bool {
	// 强平价为 0.0 通常意味着没有开仓，或使用了 1 倍杠杆 (实际上 1 倍杠杆不会被强平)
	if e.position.Side == model.DirFlat || e.position.LiquidationPrice == 0.0 {
		return false
	}

	// 注意：强平价通常比止损价更接近开仓价 (即风险更大)

	if e.position.Side == model.DirLong {
		// 多头强平：当前价格 <= 强平价
		// 价格下跌
		return currentPrice <= e.position.LiquidationPrice
	}

	if e.position.Side == model.DirShort {
		// 空头强平：当前价格 >= 强平价
		// 价格上涨
		return currentPrice >= e.position.LiquidationPrice
	}

	return false
}

// GetMaxEquity 返回账户历史上的最高净值
func (e *SimulatorExecutor) GetMaxEquity() float64 {
	e.mu.RLock()
	defer e.mu.RUnlock()

	return e.maxEquity
}

// GetBalance (Executor 接口要求的方法，用于获取当前余额，可根据需求返回 balance 或 equity)
// 在策略风控中，我们更关心净值 (Equity)，因为它包含了浮动盈亏。
func (e *SimulatorExecutor) GetBalance(ctx context.Context) (float64, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 返回净值 Equity，作为策略计算回撤的基准
	return e.equity, nil
}

// GetCurrentPosition 模拟查询当前持仓
func (e *SimulatorExecutor) GetCurrentPosition(ctx context.Context) (*model.Position, error) {
	// 实际应调用 Okx API 查询持仓

	// --- 实际的 Okx API 调用占位符 ---
	// okxPosition, err := e.okxClient.GetPositions(ctx, okx.GetPositionsRequest{
	//     InstID: e.cfg.Symbol,
	// })
	// if err != nil {
	//     return nil, err
	// }
	// if len(okxPosition) == 0 || okxPosition[0].Pos == 0 {
	//     e.currentPosition.Direction = DirFlat
	//     e.currentPosition.Size = 0
	// } else {
	//     // 转换 Okx 仓位模型到 Position
	//     // ...
	// }
	// ---------------------------------

	// 返回内部模拟的仓位 (在真实环境中，应返回查询 API 结果)
	return &model.Position{
		InstID:    e.position.Symbol,
		Direction: e.position.Side,
		Size:      e.position.Size,
		AvgPrice:  e.position.AvgPrice,
		UPL:       e.position.UPL,
		EntryTime: time.Time{},
	}, nil
}
