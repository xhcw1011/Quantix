package guardian

import (
	"fmt"
	"strings"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
	"github.com/Quantix/quantix/internal/strategy/registry"
)

func init() {
	registry.Register("guardian", Factory)
}

// guardianConfig is the parsed, validated setup for one Guardian engine.
type guardianConfig struct {
	Symbol string
	Adopt  bool

	// explicit position (used when Adopt == false)
	Side  string
	Entry float64
	Qty   float64

	Prot      ProtectionConfig
	ATRWindow int
	MAPeriod  int

	// alerts (zero value = that alert disabled)
	AlertMilestoneStep float64
	AlertStopProxR     float64
	AlertStagBars      int
	AlertStagBandR     float64
	AlertVolSpikeMult  float64
	AlertLevels        []float64
}

// Factory builds a Guardian strategy from a params map (registry FactoryFn).
func Factory(params map[string]any, log *zap.Logger) (strategy.Strategy, error) {
	gc, err := parseGuardianConfig(params)
	if err != nil {
		return nil, err
	}
	var g *Guardian
	if gc.Adopt {
		g = NewAdoptGuardian(gc.Symbol, gc.Prot, gc.ATRWindow, log)
	} else {
		g = NewGuardian(gc.Symbol, NewProtection(gc.Side, gc.Entry, gc.Qty, gc.Prot, 0), gc.ATRWindow, log)
	}
	if gc.MAPeriod > 0 {
		g.SetMAPeriod(gc.MAPeriod)
	}
	if eng := buildAlertEngine(gc); eng.Len() > 0 {
		// Dispatcher is injected by the engine layer via SetDispatcher; alerts
		// evaluate harmlessly with a nil dispatcher until then.
		g.SetAlerts(eng, nil)
	}
	return g, nil
}

func buildAlertEngine(gc guardianConfig) *AlertEngine {
	e := NewAlertEngine()
	if gc.AlertMilestoneStep > 0 {
		e.Add(&ProfitMilestone{Step: gc.AlertMilestoneStep})
	}
	if gc.AlertStopProxR > 0 {
		e.Add(&StopProximity{WithinR: gc.AlertStopProxR})
	}
	if gc.AlertStagBars > 0 {
		band := gc.AlertStagBandR
		if band <= 0 {
			band = 0.3
		}
		e.Add(&Stagnation{Bars: gc.AlertStagBars, BandR: band})
	}
	if gc.AlertVolSpikeMult > 0 {
		e.Add(&VolSpike{Mult: gc.AlertVolSpikeMult})
	}
	for _, lv := range gc.AlertLevels {
		e.Add(&LevelCross{Level: lv})
	}
	if gc.MAPeriod > 0 {
		e.Add(&MAState{})
	}
	return e
}

func parseGuardianConfig(p map[string]any) (guardianConfig, error) {
	gc := guardianConfig{
		Adopt:     boolOr(p, "Adopt", true),
		ATRWindow: intOr(p, "ATRWindow", 14),
		MAPeriod:  intOr(p, "MAPeriod", 0),
		Prot: ProtectionConfig{
			// Plain-percentage defaults (intuitive for non-technical users); ATR
			// mode stays available to API callers via StopMode="atr".
			StopMode:     parseStopMode(strOr(p, "StopMode", "pct")),
			StopValue:    floatOr(p, "StopValue", 0.03),
			TrailEnabled: boolOr(p, "TrailEnabled", true),
			ActivateR:    floatOr(p, "ActivateR", 1),
			TrailMode:    parseTrailMode(strOr(p, "TrailMode", "r")),
			TrailValue:   floatOr(p, "TrailValue", 1),
		},
		// Sensible alerts on by default; user need not configure them.
		AlertMilestoneStep: floatOr(p, "AlertProfitMilestone", 1),
		AlertStopProxR:     floatOr(p, "AlertStopProximityR", 0.3),
		AlertStagBars:      intOr(p, "AlertStagnationBars", 0),
		AlertStagBandR:     floatOr(p, "AlertStagnationBandR", 0),
		AlertVolSpikeMult:  floatOr(p, "AlertVolSpikeMult", 0),
	}
	gc.Symbol = strOr(p, "Symbol", "")
	if gc.Symbol == "" {
		return gc, fmt.Errorf("guardian: Symbol is required")
	}
	// Take-profit: the UI sends only a value; a positive value means percent-of-
	// entry unless an explicit mode is given, and 0 (or missing) means "no target".
	gc.Prot.TPValue = floatOr(p, "TPValue", 0)
	gc.Prot.TPMode = parseTPMode(strOr(p, "TPMode", ""))
	if gc.Prot.TPValue > 0 && gc.Prot.TPMode == TPNone {
		gc.Prot.TPMode = TPPct
	}
	if gc.Prot.TPValue <= 0 {
		gc.Prot.TPMode = TPNone
	}
	// Break-even: the UI sends BreakEvenPct (profit % at which to lock in no-loss);
	// convert to R for the percentage stop. API callers may set BreakEvenAtR directly.
	if bePct := floatOr(p, "BreakEvenPct", 0); bePct > 0 && gc.Prot.StopMode == StopPct && gc.Prot.StopValue > 0 {
		gc.Prot.BreakEvenAtR = bePct / gc.Prot.StopValue
	} else {
		gc.Prot.BreakEvenAtR = floatOr(p, "BreakEvenAtR", 0)
	}
	for _, v := range sliceOr(p, "AlertLevels") {
		if f, ok := asFloat(v); ok {
			gc.AlertLevels = append(gc.AlertLevels, f)
		}
	}
	if !gc.Adopt {
		gc.Side = normSide(strOr(p, "Side", ""))
		gc.Entry = floatOr(p, "Entry", 0)
		gc.Qty = floatOr(p, "Qty", 0)
		if (gc.Side != SideLong && gc.Side != SideShort) || gc.Entry <= 0 || gc.Qty <= 0 {
			return gc, fmt.Errorf("guardian: non-adopt requires Side (long/short), Entry>0, Qty>0")
		}
	}
	return gc, nil
}

// ── param helpers (JSON decodes numbers as float64) ─────────────────

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func floatOr(p map[string]any, k string, def float64) float64 {
	if v, ok := p[k]; ok {
		if f, ok := asFloat(v); ok {
			return f
		}
	}
	return def
}

func intOr(p map[string]any, k string, def int) int {
	return int(floatOr(p, k, float64(def)))
}

func strOr(p map[string]any, k, def string) string {
	if v, ok := p[k]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func boolOr(p map[string]any, k string, def bool) bool {
	if v, ok := p[k]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func sliceOr(p map[string]any, k string) []any {
	if v, ok := p[k]; ok {
		if s, ok := v.([]any); ok {
			return s
		}
	}
	return nil
}

func normSide(s string) string {
	switch strings.ToLower(s) {
	case "long", "buy":
		return SideLong
	case "short", "sell":
		return SideShort
	}
	return s
}

func parseStopMode(s string) StopMode {
	switch strings.ToLower(s) {
	case "price":
		return StopPrice
	case "pct", "percent":
		return StopPct
	default:
		return StopATR
	}
}

func parseTrailMode(s string) TrailMode {
	if strings.ToLower(s) == "r" {
		return TrailR
	}
	return TrailATR
}

func parseTPMode(s string) TPMode {
	switch strings.ToLower(s) {
	case "price":
		return TPPrice
	case "pct", "percent":
		return TPPct
	case "r":
		return TPR
	default:
		return TPNone
	}
}
