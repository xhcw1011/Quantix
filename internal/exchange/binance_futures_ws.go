package exchange

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	futures "github.com/adshao/go-binance/v2/futures"
	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/config"
)

// BinanceFuturesWSClient subscribes to Binance USDM Futures WebSocket streams.
type BinanceFuturesWSClient struct {
	log            *zap.Logger
	staleTimeout   time.Duration
	staleCheck     time.Duration
	reconnectDelay time.Duration
	openErrorDelay time.Duration
}

// NewBinanceFuturesWSClient creates a new Futures WebSocket client.
func NewBinanceFuturesWSClient(cfg config.BinanceConfig, wsCfg config.WSConfig, log *zap.Logger) *BinanceFuturesWSClient {
	ApplyBinanceNetworkMode(cfg)
	return &BinanceFuturesWSClient{
		log:            log,
		staleTimeout:   wsCfg.StaleTimeout,
		staleCheck:     wsCfg.StaleCheckInterval,
		reconnectDelay: wsCfg.ReconnectDelay,
		openErrorDelay: wsCfg.OpenErrorDelay,
	}
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
			w.sleep(ctx, w.openErrorDelay)
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
			w.sleep(ctx, w.reconnectDelay)
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
			w.sleep(ctx, w.openErrorDelay)
			continue
		}

		w.log.Info("futures ticker websocket connected", zap.Strings("symbols", symbols))

		select {
		case <-ctx.Done():
			close(stopC)
			return
		case <-stopC:
			w.log.Warn("futures ticker websocket disconnected, reconnecting...")
			w.sleep(ctx, w.reconnectDelay)
		}
	}
}

// closeAllStopCs safely closes all stop channels and the combined channel.
func closeAllStopCs(stopCs []chan struct{}, doneCombined chan struct{}) {
	for _, sc := range stopCs {
		select {
		case <-sc:
		default:
			close(sc)
		}
	}
	select {
	case <-doneCombined:
	default:
		close(doneCombined)
	}
}

func (w *BinanceFuturesWSClient) openKlineStreams(ctx context.Context, symbols, intervals []string, handler KlineHandler) (chan struct{}, error) {
	if len(symbols) == 0 || len(intervals) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	doneCombined := make(chan struct{})
	var stopCs []chan struct{}
	var lastDataTime atomic.Int64
	lastDataTime.Store(time.Now().UnixNano())

	var teardownOnce sync.Once
	teardown := func(reason string) {
		teardownOnce.Do(func() {
			w.log.Warn("futures kline: tearing down all streams", zap.String("reason", reason))
			closeAllStopCs(stopCs, doneCombined)
		})
	}

	for _, symbol := range symbols {
		for _, interval := range intervals {
			sym := symbol
			itv := interval

			wsHandler := func(event *futures.WsKlineEvent) {
				if event == nil {
					return
				}
				lastDataTime.Store(time.Now().UnixNano())
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

			doneC, stopC, err := futures.WsKlineServe(sym, itv, wsHandler, errHandler)
			if err != nil {
				for _, sc := range stopCs {
					close(sc)
				}
				close(doneCombined)
				return nil, err
			}
			stopCs = append(stopCs, stopC)

			// Any single stream disconnect → tear down all.
			go func(done <-chan struct{}, sym, itv string) {
				select {
				case <-done:
					teardown(fmt.Sprintf("stream %s/%s died", sym, itv))
				case <-doneCombined:
				case <-ctx.Done():
				}
			}(doneC, sym, itv)
		}
	}

	// Stale data watchdog: if no data received for staleTimeout, force reconnect.
	go func() {
		ticker := time.NewTicker(w.staleCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				last := time.Unix(0, lastDataTime.Load())
				if time.Since(last) > w.staleTimeout {
					teardown(fmt.Sprintf("no kline data for %s", time.Since(last).Round(time.Second)))
					return
				}
			case <-doneCombined:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	return doneCombined, nil
}

func (w *BinanceFuturesWSClient) openTickerStreams(ctx context.Context, symbols []string, handler TickerHandler) (chan struct{}, error) {
	if len(symbols) == 0 {
		ch := make(chan struct{})
		close(ch)
		return ch, nil
	}

	doneCombined := make(chan struct{})
	var stopCs []chan struct{}
	var lastDataTime atomic.Int64
	lastDataTime.Store(time.Now().UnixNano())

	var teardownOnce sync.Once
	teardown := func(reason string) {
		teardownOnce.Do(func() {
			w.log.Warn("futures ticker: tearing down all streams", zap.String("reason", reason))
			closeAllStopCs(stopCs, doneCombined)
		})
	}

	for _, symbol := range symbols {
		sym := symbol

		wsHandler := func(event *futures.WsBookTickerEvent) {
			if event == nil {
				return
			}
			lastDataTime.Store(time.Now().UnixNano())
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

		doneC, stopC, err := futures.WsBookTickerServe(sym, wsHandler, errHandler)
		if err != nil {
			for _, sc := range stopCs {
				close(sc)
			}
			close(doneCombined)
			return nil, err
		}
		stopCs = append(stopCs, stopC)

		go func(done <-chan struct{}, sym string) {
			select {
			case <-done:
				teardown(fmt.Sprintf("stream %s died", sym))
			case <-doneCombined:
			case <-ctx.Done():
			}
		}(doneC, sym)
	}

	// Stale data watchdog.
	go func() {
		ticker := time.NewTicker(w.staleCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				last := time.Unix(0, lastDataTime.Load())
				if time.Since(last) > w.staleTimeout {
					teardown(fmt.Sprintf("no ticker data for %s", time.Since(last).Round(time.Second)))
					return
				}
			case <-doneCombined:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

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
