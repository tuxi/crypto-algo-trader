package api

import (
	"crypto-algo-trader/internal/data"
	"crypto-algo-trader/internal/service"
	"encoding/json"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// OkxWsData 适用于 Okx V5 的通用响应结构
type OkxWsData struct {
	Arg struct {
		Channel string `json:"channel"`
		InstId  string `json:"instId"`
	} `json:"arg"`
	Data  json.RawMessage `json:"data"` // <-- 修正：使用 RawMessage 延迟解析
	Event string          `json:"event"`
}

// OkxTradeData 适配 Okx trades 频道数据结构
type OkxTradeData struct {
	Timestamp string `json:"ts"`   // 成交时间 (毫秒字符串)
	Price     string `json:"px"`   // 成交价格
	Size      string `json:"sz"`   // 成交数量
	Side      string `json:"side"` // buy 或 sell (成交方向，用于判断 IsBuyerMaker)
	TradeId   string `json:"tradeId"`
	InstId    string `json:"instId"`
}

// OkxTickerData 结构体，用于解析 tickers 频道数据
type OkxTickerData struct {
	LastPrice string `json:"last"` // 最新成交价 (tickers 频道使用 'last')
	Timestamp string `json:"ts"`
	InstId    string `json:"instId"`
}

// 映射 InstId 到 Symbol (例如 BTC-USDT-SWAP -> BTCUSDT)
type InstMap map[string]string

// Connector 结构体 (保持不变)
type Connector struct {
	wsConn        *websocket.Conn
	wsURL         string
	instToSymbol  InstMap // InstID -> Symbol 的映射
	tickerChannel chan data.Ticker
}

// NewConnector (保持不变)
func NewConnector(wsURL string, symbols []string) *Connector {
	// 确保通道有足够的缓冲区来应对高频数据
	tickerChan := make(chan data.Ticker, 2048)
	// 构造 instId: 例如 BTCUSDT -> BTC-USDT-SWAP
	instToSymbol := make(InstMap, len(symbols))
	for _, symbol := range symbols {
		instID := symbol[:3] + "-" + symbol[3:] + "-SWAP"
		instToSymbol[instID] = symbol
	}

	service.Logger.Info("Connector initialized", zap.Strings("Symbols", symbols))

	return &Connector{
		wsURL:         wsURL,
		instToSymbol:  instToSymbol,
		tickerChannel: tickerChan,
	}
}

// Start 启动 WebSocket 连接和接收 Goroutine
func (c *Connector) Start() {
	service.Logger.Info("Starting Okx WS multi-symbol connection...", zap.String("URL", c.wsURL))

	u, _ := url.Parse(c.wsURL)
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		service.Logger.Fatal("Failed to connect to WS", zap.Error(err))
	}
	c.wsConn = conn
	defer c.wsConn.Close()

	var args []map[string]string
	for instID, _ := range c.instToSymbol {
		args = append(args, map[string]string{"channel": "trades", "instId": instID})
		args = append(args, map[string]string{"channel": "tickers", "instId": instID})
	}
	// 同时订阅 'trade' 和 'tickers' 频道
	subscribeMsg := map[string]interface{}{
		"op":   "subscribe",
		"args": args,
	}

	if err := c.wsConn.WriteJSON(subscribeMsg); err != nil {
		service.Logger.Error("Failed to send WS aggregated subscription", zap.Error(err))
		return
	}
	service.Logger.Info("Subscribed to all Okx TRADE and TICKERS streams successfully")

	// 启动读循环
	c.readLoop()
}

// readLoop 持续读取 WS 消息并处理
func (c *Connector) readLoop() {
	for {
		_, message, err := c.wsConn.ReadMessage()
		if err != nil {
			service.Logger.Error("Error reading WS message, attempting to reconnect...", zap.Error(err))
			time.Sleep(5 * time.Second)
			continue // 跳过，让其重连
		}

		var wsResp OkxWsData // 使用 RawMessage 结构的 OkxWsData
		if err := json.Unmarshal(message, &wsResp); err != nil {
			continue
		}

		if wsResp.Event != "" {
			continue // 忽略订阅成功或缺取消订阅事件
		}

		instID := wsResp.Arg.InstId
		if instID == "" || len(wsResp.Data) == 0 {
			continue
		}

		symbol, ok := c.instToSymbol[instID] // 根据 InstID 查找 Symbol
		if !ok || len(wsResp.Data) == 0 {
			continue
		}

		if !ok {
			return
		}

		channel := wsResp.Arg.Channel

		if channel == "trades" {
			var trades []OkxTradeData
			if err := json.Unmarshal(wsResp.Data, &trades); err != nil {
				service.Logger.Error("Trade data unmarshal error", zap.Error(err))
				continue
			}

			// 遍历收到的所有成交记录
			for _, okxTrade := range trades {
				// 1. 数据转换
				price, err := service.StringToFloat(okxTrade.Price)
				if err != nil {
					continue
				}

				volume, err := service.StringToFloat(okxTrade.Size)
				if err != nil {
					continue
				}

				timestamp, err := service.StringToInt64(okxTrade.Timestamp)
				if err != nil {
					continue
				}

				// 2. 买卖方向判断 (Okx side: buy/sell)
				// side="buy" 意味着这是一笔主动买入 (Taker 买入)
				// side="sell" 意味着这是一笔主动卖出 (Taker 卖出)
				isBuyerMaker := (okxTrade.Side != "buy") // 如果不是主动买入，则为主动卖出

				// 3. 构建内部 Ticker 结构
				ticker := data.Ticker{
					Symbol:       symbol,
					Timestamp:    timestamp,
					Price:        price,
					Volume:       volume,
					IsBuyerMaker: isBuyerMaker,
				}

				// 发送给 Data Engine
				// 使用 select/default 防止阻塞 Connector
				select {
				case c.tickerChannel <- ticker:
				default:
					service.Logger.Warn("Ticker channel full! Dropping trade data for", zap.String("Symbol", symbol))
				}
			}
		} else if channel == "tickers" {
			var tickers []OkxTickerData
			if err := json.Unmarshal(wsResp.Data, &tickers); err != nil {
				service.Logger.Error("Tickers data unmarshal error", zap.Error(err))
				continue
			}

			// 处理 TICKER 数据 (用于价格连续性)
			if len(tickers) == 0 {
				continue
			}
			okxTicker := tickers[0] // 仅处理最新的快照

			price, err := service.StringToFloat(okxTicker.LastPrice)
			if err != nil {
				continue
			}

			timestamp, _ := service.StringToInt64(okxTicker.Timestamp)

			// 构造 Ticker：volume=0, IsBuyerMaker=false (价格快照)
			ticker := data.Ticker{
				Symbol:       symbol,
				Timestamp:    timestamp,
				Price:        price,
				Volume:       0,
				IsBuyerMaker: false,
			}
			// 使用 select/default 防止阻塞 Connector
			select {
			case c.tickerChannel <- ticker:
			default:
				service.Logger.Debug("Ticker channel full! Dropping ticker snapshot for", zap.String("Symbol", symbol))
			}

		}
	}
}

// GetTickerChannel (保持不变)
func (c *Connector) GetTickerChannel() chan data.Ticker {
	return c.tickerChannel
}
