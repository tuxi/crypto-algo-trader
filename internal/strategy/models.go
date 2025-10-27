package strategy

import (
	"fmt"
	"time"
)

// ActionType 定义了信号类型
type ActionType string

const (
	ActionNone   ActionType = "NONE"   // 无操作
	ActionOpen   ActionType = "OPEN"   // 开仓
	ActionClose  ActionType = "CLOSE"  // 平仓 (指平掉当前仓位)
	ActionUpdate ActionType = "UPDATE" // 更新止损/止盈
)

// Direction 定义了持仓或期望开仓的方向
type Direction string

const (
	DirLong  Direction = "LONG"
	DirShort Direction = "SHORT"
	DirFlat  Direction = "FLAT"
)

// Signal 结构体定义了策略层向执行层发出的具体指令
type Signal struct {
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
