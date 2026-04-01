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

// BinanceFuturesRESTClient wraps the go-binance futures client for REST market-data.
type BinanceFuturesRESTClient struct {
	client *futures.Client
	log    *zap.Logger
}

// NewBinanceFuturesRESTClient creates a Binance USDM Futures REST client.
func NewBinanceFuturesRESTClient(apiKey, apiSecret string, useTestnet bool, cfg config.BinanceConfig, log *zap.Logger) (*BinanceFuturesRESTClient, error) {
	ApplyBinanceNetworkMode(cfg)
	c := futures.NewClient(apiKey, apiSecret)
	if err := ConfigureBinanceFuturesAuth(c, cfg); err != nil {
		return nil, fmt.Errorf("binance futures REST auth config: %w", err)
	}
	return &BinanceFuturesRESTClient{client: c, log: log}, nil
}

// GetKlines fetches historical candlestick data from Binance Futures.
func (b *BinanceFuturesRESTClient) GetKlines(ctx context.Context, symbol, interval string, limit int) ([]Kline, error) {
	raw, err := b.client.NewKlinesService().
		Symbol(symbol).Interval(interval).Limit(limit).Do(ctx)
	if err != nil {
		return nil, err
	}
	klines := make([]Kline, 0, len(raw))
	for _, r := range raw {
		k, err := convertFuturesKline(symbol, interval, r)
		if err != nil {
			b.log.Warn("failed to parse futures kline", zap.Error(err), zap.String("symbol", symbol))
			continue
		}
		klines = append(klines, k)
	}
	return klines, nil
}

// GetKlinesBetween fetches historical candlesticks between startTime and endTime.
func (b *BinanceFuturesRESTClient) GetKlinesBetween(ctx context.Context, symbol, interval string, startTime, endTime time.Time, limit int) ([]Kline, error) {
	raw, err := b.client.NewKlinesService().
		Symbol(symbol).Interval(interval).
		StartTime(startTime.UnixMilli()).EndTime(endTime.UnixMilli()).
		Limit(limit).Do(ctx)
	if err != nil {
		return nil, err
	}
	klines := make([]Kline, 0, len(raw))
	for _, r := range raw {
		k, err := convertFuturesKline(symbol, interval, r)
		if err != nil {
			b.log.Warn("failed to parse futures kline", zap.Error(err))
			continue
		}
		klines = append(klines, k)
	}
	return klines, nil
}

// GetServerTime returns Binance Futures server time.
func (b *BinanceFuturesRESTClient) GetServerTime(ctx context.Context) (time.Time, error) {
	ms, err := b.client.NewServerTimeService().Do(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return time.UnixMilli(ms), nil
}

var _ RESTClient = (*BinanceFuturesRESTClient)(nil)

func convertFuturesKline(symbol, interval string, r *futures.Kline) (Kline, error) {
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
		return Kline{}, fmt.Errorf("invalid zero/negative price in futures kline: O=%.8f H=%.8f L=%.8f C=%.8f", open, high, low, close_)
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
