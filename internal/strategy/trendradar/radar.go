// Package trendradar is an alert-only strategy: it watches a symbol's MACD and
// pings the user (Telegram) when a DECISIVE trend signal forms — a 15m cross whose
// histogram grows strong AND agrees with the 4h direction. It places NO orders.
//
// It exists because exhaustive testing showed automated MACD trading has no net
// edge after cost, but the signal is still a useful DECISION AID: the radar flags
// "a trend may be forming", the human decides whether to act, and the guardian
// manages the risk on whatever they open. Edge from judgment, discipline from the
// bot — the one combination that doesn't get competed away.
package trendradar

import (
	"fmt"
	"math"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/exchange"
	"github.com/Quantix/quantix/internal/indicator"
	"github.com/Quantix/quantix/internal/strategy"
)

const (
	macdWarm       = 40 // bars needed before MACD is meaningful
	strengthWindow = 50 // trailing window for the "recent avg |hist|" strength baseline
)

// Radar watches sigInterval (e.g. 15m) for decisive MACD crosses confirmed by
// confInterval (e.g. 4h) direction, and dispatches an alert. Never trades.
type Radar struct {
	symbol       string
	sigIv        string
	confIv       string
	strengthMult float64 // M: |hist| must exceed M × recent avg |hist| to be "decisive"
	log          *zap.Logger
	disp         Dispatcher

	closesSig  []float64
	closesConf []float64
	h4Bull     bool
	ready4h    bool
	lastFired  int // 0 none, +1 golden, -1 death — debounces repeat alerts
}

// New builds a radar. strengthMult=0 disables the strength filter (any cross).
func New(symbol, sigInterval, confInterval string, strengthMult float64, log *zap.Logger) *Radar {
	if log == nil {
		log = zap.NewNop()
	}
	return &Radar{symbol: symbol, sigIv: sigInterval, confIv: confInterval, strengthMult: strengthMult, log: log}
}

func (r *Radar) Name() string                                { return "trendradar" }
func (r *Radar) OnFill(_ *strategy.Context, _ strategy.Fill) {} // never trades
func (r *Radar) SetDispatcher(d Dispatcher)                  { r.disp = d }

func (r *Radar) OnBar(ctx *strategy.Context, bar exchange.Kline) {
	if bar.Symbol != r.symbol {
		return
	}
	switch bar.Interval {
	case r.confIv: // 4h — update the confirmation direction
		r.closesConf = append(r.closesConf, bar.Close)
		if len(r.closesConf) >= macdWarm {
			h := indicator.MACD(r.closesConf, 12, 26, 9).Histogram
			r.h4Bull = last(h) > 0
			r.ready4h = true
		}
	case r.sigIv: // 15m — look for a decisive, 4h-aligned cross
		r.closesSig = append(r.closesSig, bar.Close)
		if len(r.closesSig) < macdWarm || !r.ready4h {
			return
		}
		hist := indicator.MACD(r.closesSig, 12, 26, 9).Histogram
		dir := evaluateSignal(last(hist), recentAvgMag(hist, strengthWindow), r.strengthMult, r.h4Bull, r.lastFired)
		if dir == 0 {
			return
		}
		// Advance the debounce state even on warmup replay, so going live doesn't
		// re-alert a trend that already formed in history — only NEW crosses alert.
		r.lastFired = dir
		if bar.Warmup {
			return
		}
		r.fire(dir, bar, last(hist))
	}
}

// evaluateSignal fires (+1 golden / -1 death) only when the histogram is decisively
// strong in a direction the 4h agrees with, and that side hasn't already fired.
func evaluateSignal(hist, avgMag, strengthMult float64, h4Bull bool, lastFired int) int {
	thr := strengthMult * avgMag
	if hist > thr && lastFired != 1 && h4Bull {
		return 1
	}
	if hist < -thr && lastFired != -1 && !h4Bull {
		return -1
	}
	return 0
}

func (r *Radar) fire(dir int, bar exchange.Kline, hist float64) {
	head := "📈 上涨趋势可能形成"
	side := "多"
	if dir == -1 {
		head = "📉 下跌趋势可能形成"
		side = "空"
	}
	title := fmt.Sprintf("🎯 %s %s 趋势雷达 — %s", bar.Symbol, r.sigIv, head)
	msg := fmt.Sprintf(
		"%s 决定性交叉,且 %s 同向。\n价格 %s | 柱子 %.1f | %s方向 %s\n👉 你的判断:要做「%s」就用「守仓·顺便帮我开仓」下,机器人替你守止损/移动止盈。",
		r.sigIv, r.confIv, fmtPrice(bar.Close), hist, r.confIv, bullTxt(r.h4Bull), side)
	if r.disp != nil {
		if err := r.disp.Send(title, msg); err != nil {
			r.log.Warn("trend radar alert failed", zap.Error(err))
		}
	}
	r.log.Info("trend radar signal",
		zap.String("symbol", bar.Symbol), zap.String("interval", r.sigIv),
		zap.Int("dir", dir), zap.Float64("price", bar.Close), zap.Float64("hist", hist))
}

// ── helpers ──

func last(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return xs[len(xs)-1]
}

// recentAvgMag = mean |hist| over the last `win` bars (the strength baseline).
func recentAvgMag(hist []float64, win int) float64 {
	n := len(hist)
	if n == 0 {
		return 0
	}
	lo := n - win
	if lo < 0 {
		lo = 0
	}
	var s float64
	c := 0
	for i := lo; i < n; i++ {
		s += math.Abs(hist[i])
		c++
	}
	if c == 0 {
		return 0
	}
	return s / float64(c)
}

func bullTxt(b bool) string {
	if b {
		return "多头"
	}
	return "空头"
}

func fmtPrice(p float64) string {
	// plain number with thousands separators, no scientific notation
	return fmt.Sprintf("%.2f", p)
}
