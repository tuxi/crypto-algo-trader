package executor

import (
	"context"
	"crypto-algo-trader/internal/strategy"
)

// Executor 是交易执行器的通用接口，负责与交易所通信
type Executor interface {
	// ExecuteSignal 接收策略信号，并尝试执行交易 (开仓、平仓、修改订单)
	ExecuteSignal(ctx context.Context, signal strategy.Signal) error

	// GetCurrentPosition 查询并返回当前持仓信息
	GetCurrentPosition(ctx context.Context) (*strategy.Position, error)

	// GetBalance 获取账户余额
	GetBalance(ctx context.Context) (float64, error)
}
