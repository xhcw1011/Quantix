package exchange

import (
	"context"
	"fmt"
	"strconv"
	"time"

	futures "github.com/adshao/go-binance/v2/futures"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
)

// BinanceFuturesWSClient subscribes to Binance USDM Futures WebSocket streams.
type BinanceFuturesWSClient struct {
	log *zap.Logger
}

// NewBinanceFuturesWSClient creates a new Futures WebSocket client.
func NewBinanceFuturesWSClient(cfg config.BinanceConfig, log *zap.Logger) *BinanceFuturesWSClient {
	ApplyBinanceNetworkMode(cfg)
	return &BinanceFuturesWSClient{log: log}
}

// SubscribeKlines opens a combined kline stream for Futures symbols/intervals.
func (w *BinanceFuturesWSClient) SubscribeKlines(ctx context.Context, symbols []string, intervals []string, handler KlineHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stopC, err := w.openKlineStreams(ctx, symbols, intervals, handler)
		if err != nil {
			w.log.Error("failed to open futures kline streams", zap.Error(err))
			w.sleep(ctx, 5*time.Second)
			continue
		}

		w.log.Info("futures kline websocket connected",
			zap.Strings("symbols", symbols),
			zap.Strings("intervals", intervals))

		select {
		case <-ctx.Done():
			close(stopC)
			return
		case <-stopC:
			w.log.Warn("futures kline websocket disconnected, reconnecting...")
			w.sleep(ctx, 2*time.Second)
		}
	}
}

// SubscribeTickers opens a best-bid/ask ticker stream for Futures symbols.
func (w *BinanceFuturesWSClient) SubscribeTickers(ctx context.Context, symbols []string, handler TickerHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stopC, err := w.openTickerStreams(ctx, symbols, handler)
		if err != nil {
			w.log.Error("failed to open futures ticker streams", zap.Error(err))
			w.sleep(ctx, 5*time.Second)
			continue
		}

		w.log.Info("futures ticker websocket connected", zap.Strings("symbols", symbols))

		select {
		case <-ctx.Done():
			close(stopC)
			return
		case <-stopC:
			w.log.Warn("futures ticker websocket disconnected, reconnecting...")
			w.sleep(ctx, 2*time.Second)
		}
	}
}

func (w *BinanceFuturesWSClient) openKlineStreams(ctx context.Context, symbols, intervals []string, handler KlineHandler) (chan struct{}, error) {
	if len(symbols) == 0 || len(intervals) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	doneCombined := make(chan struct{})
	var firstDone chan struct{}

	for _, symbol := range symbols {
		for _, interval := range intervals {
			sym := symbol
			itv := interval

			wsHandler := func(event *futures.WsKlineEvent) {
				if event == nil {
					return
				}
				k, err := convertFuturesWSKline(sym, event)
				if err != nil {
					w.log.Warn("failed to convert futures ws kline", zap.Error(err))
					return
				}
				handler(k)
			}

			errHandler := func(err error) {
				w.log.Error("futures kline websocket error",
					zap.String("symbol", sym), zap.String("interval", itv), zap.Error(err))
			}

			doneC, _, err := futures.WsKlineServe(sym, itv, wsHandler, errHandler)
			if err != nil {
				close(doneCombined)
				return nil, err
			}

			if firstDone == nil {
				firstDone = doneC
				go func() {
					select {
					case <-firstDone:
						select {
						case <-doneCombined:
						default:
							close(doneCombined)
						}
					case <-ctx.Done():
					}
				}()
			}
		}
	}

	return doneCombined, nil
}

func (w *BinanceFuturesWSClient) openTickerStreams(ctx context.Context, symbols []string, handler TickerHandler) (chan struct{}, error) {
	if len(symbols) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	doneCombined := make(chan struct{})
	var firstDone chan struct{}

	for _, symbol := range symbols {
		sym := symbol

		wsHandler := func(event *futures.WsBookTickerEvent) {
			if event == nil {
				return
			}
			t, err := convertFuturesWSBookTicker(sym, event)
			if err != nil {
				w.log.Warn("failed to convert futures book ticker", zap.Error(err))
				return
			}
			handler(t)
		}

		errHandler := func(err error) {
			w.log.Error("futures ticker websocket error", zap.String("symbol", sym), zap.Error(err))
		}

		doneC, _, err := futures.WsBookTickerServe(sym, wsHandler, errHandler)
		if err != nil {
			close(doneCombined)
			return nil, err
		}

		if firstDone == nil {
			firstDone = doneC
			go func() {
				select {
				case <-firstDone:
					select {
					case <-doneCombined:
					default:
						close(doneCombined)
					}
				case <-ctx.Done():
				}
			}()
		}
	}

	return doneCombined, nil
}

var _ WSClient = (*BinanceFuturesWSClient)(nil)

func (w *BinanceFuturesWSClient) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func convertFuturesWSKline(symbol string, e *futures.WsKlineEvent) (Kline, error) {
	k := e.Kline
	open, err := strconv.ParseFloat(k.Open, 64)
	if err != nil {
		return Kline{}, err
	}
	high, err := strconv.ParseFloat(k.High, 64)
	if err != nil {
		return Kline{}, err
	}
	low, err := strconv.ParseFloat(k.Low, 64)
	if err != nil {
		return Kline{}, err
	}
	close_, err := strconv.ParseFloat(k.Close, 64)
	if err != nil {
		return Kline{}, err
	}
	vol, err := strconv.ParseFloat(k.Volume, 64)
	if err != nil {
		return Kline{}, err
	}
	quoteVol, err := strconv.ParseFloat(k.QuoteVolume, 64)
	if err != nil {
		return Kline{}, err
	}
	if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
		return Kline{}, fmt.Errorf("invalid zero/negative price in futures ws kline: O=%.8f H=%.8f L=%.8f C=%.8f", open, high, low, close_)
	}
	return Kline{
		Symbol:      symbol,
		Interval:    k.Interval,
		OpenTime:    time.UnixMilli(k.StartTime),
		CloseTime:   time.UnixMilli(k.EndTime),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       close_,
		Volume:      vol,
		QuoteVolume: quoteVol,
		NumTrades:   k.TradeNum,
		IsClosed:    k.IsFinal,
	}, nil
}

func convertFuturesWSBookTicker(symbol string, e *futures.WsBookTickerEvent) (Ticker, error) {
	bid, err := strconv.ParseFloat(e.BestBidPrice, 64)
	if err != nil {
		return Ticker{}, err
	}
	ask, err := strconv.ParseFloat(e.BestAskPrice, 64)
	if err != nil {
		return Ticker{}, err
	}
	if bid <= 0 || ask <= 0 {
		return Ticker{}, fmt.Errorf("invalid zero/negative futures ticker: bid=%.8f ask=%.8f", bid, ask)
	}
	return Ticker{
		Symbol:    symbol,
		BidPrice:  bid,
		AskPrice:  ask,
		LastPrice: (bid + ask) / 2,
		Timestamp: time.Now(),
	}, nil
}
