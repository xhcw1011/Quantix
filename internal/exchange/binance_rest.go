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

// BinanceRESTClient wraps the go-binance client for REST operations.
type BinanceRESTClient struct {
	client *binance.Client
	log    *zap.Logger
}

// NewBinanceRESTClient creates a new Binance REST client.
// apiKey and apiSecret may be empty for public endpoints.
func NewBinanceRESTClient(apiKey, apiSecret string, useTestnet bool, log *zap.Logger) (*BinanceRESTClient, error) {
	return NewBinanceRESTClientWithConfig(apiKey, apiSecret, useTestnet, config.BinanceConfig{}, log)
}

// NewBinanceRESTClientWithConfig creates a Binance REST client with optional private-key auth.
func NewBinanceRESTClientWithConfig(apiKey, apiSecret string, useTestnet bool, cfg config.BinanceConfig, log *zap.Logger) (*BinanceRESTClient, error) {
	ApplyBinanceNetworkMode(cfg)
	c := binance.NewClient(apiKey, apiSecret)
	if err := ConfigureBinanceAuth(c, cfg); err != nil {
		return nil, fmt.Errorf("binance REST auth config: %w", err)
	}
	return &BinanceRESTClient{client: c, log: log}, nil
}

// GetKlines fetches historical candlestick data.
// symbol: e.g. "BTCUSDT", interval: e.g. "1h", limit: max 1000.
func (b *BinanceRESTClient) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	svc := b.client.NewKlinesService().
		Symbol(symbol).
		Interval(interval).
		Limit(limit)

	raw, err := svc.Do(ctx)
	if err != nil {
		return nil, err
	}

	klines := make([]Kline, 0, len(raw))
	for _, r := range raw {
		k, err := convertBinanceKline(symbol, interval, r)
		if err != nil {
			b.log.Warn("failed to parse kline", zap.Error(err), zap.String("symbol", symbol))
			continue
		}
		klines = append(klines, k)
	}
	return klines, nil
}

// GetKlinesBetween fetches historical candlesticks between startTime and endTime.
func (b *BinanceRESTClient) GetKlinesBetween(ctx context.Context, symbol, interval string, startTime, endTime time.Time, limit int) ([]Kline, error) {
	svc := b.client.NewKlinesService().
		Symbol(symbol).
		Interval(interval).
		StartTime(startTime.UnixMilli()).
		EndTime(endTime.UnixMilli()).
		Limit(limit)

	raw, err := svc.Do(ctx)
	if err != nil {
		return nil, err
	}

	klines := make([]Kline, 0, len(raw))
	for _, r := range raw {
		k, err := convertBinanceKline(symbol, interval, r)
		if err != nil {
			b.log.Warn("failed to parse kline", zap.Error(err))
			continue
		}
		klines = append(klines, k)
	}
	return klines, nil
}

// GetServerTime returns Binance server time for sync checking.
func (b *BinanceRESTClient) GetServerTime(ctx context.Context) (time.Time, error) {
	ms, err := b.client.NewServerTimeService().Do(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

// Compile-time interface assertion.
var _ RESTClient = (*BinanceRESTClient)(nil)

func convertBinanceKline(symbol, interval string, r *binance.Kline) (Kline, error) {
	open, err := strconv.ParseFloat(r.Open, 64)
	if err != nil {
		return Kline{}, err
	}
	high, err := strconv.ParseFloat(r.High, 64)
	if err != nil {
		return Kline{}, err
	}
	low, err := strconv.ParseFloat(r.Low, 64)
	if err != nil {
		return Kline{}, err
	}
	close_, err := strconv.ParseFloat(r.Close, 64)
	if err != nil {
		return Kline{}, err
	}
	vol, err := strconv.ParseFloat(r.Volume, 64)
	if err != nil {
		return Kline{}, err
	}
	quoteVol, err := strconv.ParseFloat(r.QuoteAssetVolume, 64)
	if err != nil {
		return Kline{}, err
	}

	if open <= 0 || high <= 0 || low <= 0 || close_ <= 0 {
		return Kline{}, fmt.Errorf("invalid zero/negative price in kline: O=%.8f H=%.8f L=%.8f C=%.8f", open, high, low, close_)
	}

	return Kline{
		Symbol:      symbol,
		Interval:    interval,
		OpenTime:    time.UnixMilli(r.OpenTime),
		CloseTime:   time.UnixMilli(r.CloseTime),
		Open:        open,
		High:        high,
		Low:         low,
		Close:       close_,
		Volume:      vol,
		QuoteVolume: quoteVol,
		NumTrades:   r.TradeNum,
		IsClosed:    true,
	}, nil
}
