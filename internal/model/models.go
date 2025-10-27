package model

import "time"

// Ticker 代表最小粒度的市场数据（成交或价格快照）
type Ticker struct {
	Symbol       string  // 所属交易对，例如 "BTCUSDT"
	Timestamp    int64   // 毫秒时间戳
	Price        float64 // 价格
	Volume       float64 // 交易量 (0 表示价格快照)
	IsBuyerMaker bool    // 是否为 Maker 导致的成交 (用于判断方向)
}

// KLine 代表聚合后的 K 线数据
type KLine struct {
	Symbol    string // 所属交易对
	Interval  string // 周期，例如 "1m", "5m", "1h"
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	StartTime time.Time
	EndTime   time.Time
}
