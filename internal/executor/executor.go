package executor

import (
	"context"
	"crypto-algo-trader/internal/model"
)

// Executor 是交易执行器的通用接口，负责与交易所通信
type Executor interface {
	// 接收策略信号，并尝试执行交易 (开仓、平仓、修改订单)
	ExecuteSignal(ctx context.Context, signal model.Signal) error

	// 查询并返回当前持仓信息
	GetCurrentPosition(ctx context.Context) (*model.Position, error)

	// 获取账户余额
	GetBalance(ctx context.Context) (float64, error)

	// 返回已完成的交易记录，用于策略分析和自适应
	GetTradeHistory() ([]*model.TradeRecord, error)

	// 返回账户历史上的最高净值
	GetMaxEquity() float64
}
