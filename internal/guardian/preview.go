package guardian

// Preview is a plain-language risk summary of a protection setup, computed before
// (or at) arming so a non-technical user sees what they're risking.
type Preview struct {
	Side      string   `json:"side"`
	Entry     float64  `json:"entry"`
	StopPrice float64  `json:"stop_price"`
	RiskUSD   float64  `json:"risk_usd"`
	RiskPct   float64  `json:"risk_pct"`
	Warnings  []string `json:"warnings"`
}

// NewPreview builds a risk preview for a proposed protection.
func NewPreview(side string, entry, qty float64, cfg ProtectionConfig, atr float64) Preview {
	p := NewProtection(side, entry, qty, cfg, atr)
	return Preview{
		Side:      p.Side,
		Entry:     p.Entry,
		StopPrice: p.Stop,
		RiskUSD:   p.RiskUSD(),
		RiskPct:   p.RiskPct(),
		Warnings:  RiskWarnings(p),
	}
}

// RiskWarnings returns plain-language sanity warnings for a protection setup: a
// stop too tight will be whipsawed out; a stop too wide risks a large single loss.
func RiskWarnings(p *Protection) []string {
	var w []string
	pct := p.RiskPct()
	switch {
	case pct > 0 && pct < 0.5:
		w = append(w, "止损很紧(<0.5%),容易被正常波动扫出,考虑放宽")
	case pct > 20:
		w = append(w, "止损很宽(>20%),单次亏损可能较大,考虑收紧或减小仓位")
	}
	return w
}
