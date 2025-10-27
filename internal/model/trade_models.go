package model

import (
	"fmt"
	"time"
)

// 来自配置 (RiskConfig)
const (
	// PnL 连续亏损 N 笔后触发收缩
	LossStreakThreshold = 3

	// 最大允许回撤，超过这个值，因子收缩到最小
	MaxAllowedDrawdown = 0.15 // 15%

	// PositionScaleFactor 的边界
	MaxScaleFactor = 1.5
	MinScaleFactor = 0.3
)

// ActionType 定义了信号类型
type ActionType string

const (
	ActionNone   ActionType = "NONE"   // 无操作
	ActionOpen   ActionType = "OPEN"   // 开仓
	ActionClose  ActionType = "CLOSE"  // 平仓 (指平掉当前仓位)
	ActionUpdate ActionType = "UPDATE" // 更新止损/止盈
)

type Direction string

const (
	DirLong  Direction = "long"  // 多
	DirShort Direction = "short" // 空
	DirFlat  Direction = "flat"  // 空仓
)

func (s Direction) String() string {
	return string(s)
}

// Signal 结构体定义了策略层向执行层发出的具体指令
type Signal struct {
	Symbol          string
	Timestamp       time.Time   // 信号生成时间
	Action          ActionType  // 操作类型: OPEN, CLOSE, UPDATE
	Direction       Direction   // 期望方向: LONG, SHORT, FLAT
	Price           float64     // 期望的入场/平仓价格 (可以是市价或限价)
	RiskedUSD       float64     // 本次交易愿意承担的最大USD损失
	PositionSize    float64     // 期望的开仓数量 (币本位，例如 BTC 数量)
	StopLossPrice   float64     // 止损价格
	TakeProfitPrice float64     // 止盈价格
	SourceState     MarketState // 信号来源的市场状态
	Reason          string      // 信号生成的文字描述
}

func (s Signal) String() string {
	return fmt.Sprintf("SIGNAL [%s | %s] @ %.2f | Size: %.4f | SL: %.2f | TP: %.2f | State: %s | Risk: %.2f USD",
		s.Action, s.Direction, s.Price, s.PositionSize, s.StopLossPrice, s.TakeProfitPrice, s.SourceState, s.RiskedUSD)
}

// Position 结构体定义了当前持仓信息 (用于执行器和策略状态同步)
type Position struct {
	InstID    string
	Direction Direction
	Size      float64 // 仓位数量 (如果 Size=0 则为 FLAT)
	AvgPrice  float64 // 平均开仓价格
	UPL       float64 // 未实现盈亏
	EntryTime time.Time
}

// TradeRecord 记录一次完整的开仓和平仓交易
type TradeRecord struct {
	EntryTime     time.Time
	ExitTime      time.Time
	Symbol        string
	PosSide       Direction
	EntryPrice    float64
	ExitPrice     float64
	Size          float64
	RealizedPnL   float64 // 已实现盈亏 (Realized PnL)
	Fee           float64 // 总手续费 (开仓 + 平仓)
	TriggerReason string  // 平仓原因: "Signal", "SL", "TP", "Liquidation"
}

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
