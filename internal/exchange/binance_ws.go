package exchange

import (
	"context"
	"fmt"
	"strconv"
	"time"

	binance "github.com/adshao/go-binance/v2"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
)

// KlineHandler is called for each incoming kline event.
type KlineHandler func(Kline)

// TickerHandler is called for each incoming 24hr ticker event.
type TickerHandler func(Ticker)

// BinanceWSClient subscribes to Binance WebSocket streams.
type BinanceWSClient struct {
	log *zap.Logger
}

// NewBinanceWSClient creates a new WebSocket client.
func NewBinanceWSClient(cfg config.BinanceConfig, log *zap.Logger) *BinanceWSClient {
	ApplyBinanceNetworkMode(cfg)
	return &BinanceWSClient{log: log}
}

// SubscribeKlines opens a combined kline stream for multiple symbols/intervals.
// It runs until ctx is cancelled, reconnecting automatically on error.
func (w *BinanceWSClient) SubscribeKlines(ctx context.Context, symbols []string, intervals []string, handler KlineHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stopC, err := w.openKlineStreams(ctx, symbols, intervals, handler)
		if err != nil {
			w.log.Error("failed to open kline streams", zap.Error(err))
			w.sleep(ctx, 5*time.Second)
			continue
		}

		w.log.Info("kline websocket connected",
			zap.Strings("symbols", symbols),
			zap.Strings("intervals", intervals))

		select {
		case <-ctx.Done():
			close(stopC)
			return
		case <-stopC:
			w.log.Warn("kline websocket disconnected, reconnecting...")
			w.sleep(ctx, 2*time.Second)
		}
	}
}

// SubscribeTickers opens a best-bid/ask ticker stream for multiple symbols.
// It runs until ctx is cancelled, reconnecting automatically on error.
func (w *BinanceWSClient) SubscribeTickers(ctx context.Context, symbols []string, handler TickerHandler) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stopC, err := w.openTickerStreams(ctx, symbols, handler)
		if err != nil {
			w.log.Error("failed to open ticker streams", zap.Error(err))
			w.sleep(ctx, 5*time.Second)
			continue
		}

		w.log.Info("ticker websocket connected", zap.Strings("symbols", symbols))

		select {
		case <-ctx.Done():
			close(stopC)
			return
		case <-stopC:
			w.log.Warn("ticker websocket disconnected, reconnecting...")
			w.sleep(ctx, 2*time.Second)
		}
	}
}

// openKlineStreams creates the actual WebSocket connections for kline streams.
// Returns a stopC channel that is closed when the connection drops.
func (w *BinanceWSClient) openKlineStreams(ctx context.Context, symbols, intervals []string, handler KlineHandler) (chan struct{}, error) {
	if len(symbols) == 0 || len(intervals) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	// We use a shared done channel that we close on first disconnect.
	doneCombined := make(chan struct{})
	var firstDone chan struct{}

	for _, symbol := range symbols {
		for _, interval := range intervals {
			sym := symbol
			itv := interval

			wsHandler := func(event *binance.WsKlineEvent) {
				if event == nil {
					return
				}
				k, err := convertWSKline(sym, event)
				if err != nil {
					w.log.Warn("failed to convert ws kline", zap.Error(err))
					return
				}
				handler(k)
			}

			errHandler := func(err error) {
				w.log.Error("kline websocket error",
					zap.String("symbol", sym),
					zap.String("interval", itv),
					zap.Error(err))
			}

			doneC, _, err := binance.WsKlineServe(sym, itv, wsHandler, errHandler)
			if err != nil {
				close(doneCombined)
				return nil, err
			}

			if firstDone == nil {
				firstDone = doneC
				// Forward first stream's done to our combined done
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

// openTickerStreams creates WebSocket connections for book ticker (bid/ask) streams.
func (w *BinanceWSClient) openTickerStreams(ctx context.Context, symbols []string, handler TickerHandler) (chan struct{}, error) {
	if len(symbols) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	doneCombined := make(chan struct{})
	var firstDone chan struct{}

	for _, symbol := range symbols {
		sym := symbol

		wsHandler := func(event *binance.WsBookTickerEvent) {
			if event == nil {
				return
			}
			t, err := convertWSBookTicker(sym, event)
			if err != nil {
				w.log.Warn("failed to convert book ticker", zap.Error(err))
				return
			}
			handler(t)
		}

		errHandler := func(err error) {
			w.log.Error("ticker websocket error",
				zap.String("symbol", sym),
				zap.Error(err))
		}

		doneC, _, err := binance.WsBookTickerServe(sym, wsHandler, errHandler)
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

// Compile-time interface assertion.
var _ WSClient = (*BinanceWSClient)(nil)

func (w *BinanceWSClient) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func convertWSKline(symbol string, e *binance.WsKlineEvent) (Kline, error) {
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
		return Kline{}, fmt.Errorf("invalid zero/negative price in ws kline: O=%.8f H=%.8f L=%.8f C=%.8f", open, high, low, close_)
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

func convertWSBookTicker(symbol string, e *binance.WsBookTickerEvent) (Ticker, error) {
	bid, err := strconv.ParseFloat(e.BestBidPrice, 64)
	if err != nil {
		return Ticker{}, err
	}
	ask, err := strconv.ParseFloat(e.BestAskPrice, 64)
	if err != nil {
		return Ticker{}, err
	}
	if bid <= 0 || ask <= 0 {
		return Ticker{}, fmt.Errorf("invalid zero/negative ticker price: bid=%.8f ask=%.8f", bid, ask)
	}

	return Ticker{
		Symbol:    symbol,
		BidPrice:  bid,
		AskPrice:  ask,
		LastPrice: (bid + ask) / 2, // mid-price approximation
		Timestamp: time.Now(),
	}, nil
}
