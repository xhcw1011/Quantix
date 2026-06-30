package aistrat

import (
	"math"
	"testing"
)

func TestUpdateTrendScore(t *testing.T) {
	tests := []struct {
		name                             string
		prev, body, atr, decay, cap, max float64
		want                             float64
	}{
		{"flat warmup: atr<=0 just decays", 2.0, 5, 0, 0.9, 1.0, 5.0, 1.8},
		{"bullish body adds strength-weighted", 0, 5, 10, 0.9, 1.0, 5.0, 0.5},
		{"bearish body subtracts", 0, -5, 10, 0.9, 1.0, 5.0, -0.5},
		{"per-bar cap clamps a spike bar", 0, 30, 10, 0.9, 1.0, 5.0, 1.0},
		{"chop cancels toward zero", 0.5, -5, 10, 0.9, 1.0, 5.0, -0.05},
		{"score cap clamps overflow", 4.8, 10, 10, 0.9, 1.0, 5.0, 5.0},
		{"score cap clamps negative overflow", -4.8, -10, 10, 0.9, 1.0, 5.0, -5.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := updateTrendScore(tt.prev, tt.body, tt.atr, tt.decay, tt.cap, tt.max)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("updateTrendScore(prev=%.2f body=%.0f atr=%.0f) = %.4f, want %.4f",
					tt.prev, tt.body, tt.atr, got, tt.want)
			}
		})
	}
}

func TestTrendEntryDir(t *testing.T) {
	tests := []struct {
		name             string
		score, threshold float64
		htfDir, want     int
	}{
		{"disabled when threshold<=0", 9, 0, 1, 0},
		{"long: score>=thr and htf confirms up", 3.5, 3.5, 1, 1},
		{"long: score ok but htf neutral → no", 3.5, 3.5, 0, 0},
		{"long: score ok but htf against → no", 3.5, 3.5, -1, 0},
		{"long: below threshold → no", 2.0, 3.5, 1, 0},
		{"short: score<=-thr and htf confirms down", -3.6, 3.5, -1, -1},
		{"short: htf against (up) → no", -3.6, 3.5, 1, 0},
		{"flat score → no", 0, 3.5, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trendEntryDir(tt.score, tt.threshold, tt.htfDir); got != tt.want {
				t.Errorf("trendEntryDir(score=%.2f thr=%.2f htf=%d) = %d, want %d",
					tt.score, tt.threshold, tt.htfDir, got, tt.want)
			}
		})
	}
}

func TestTrendAlignPenalty(t *testing.T) {
	tests := []struct {
		name             string
		rawConf          float64
		sideSign         int
		score, fullScore float64
		want             float64
	}{
		{"disabled when fullScore<=0", 0.8, 1, -5, 0, 0.8},
		{"long fully penalized at full bearish score", 0.8, 1, -2.5, 2.5, 0.0},
		{"long half penalized at half bearish score", 0.8, 1, -1.25, 2.5, 0.4},
		{"long with-trend (bullish score) unchanged", 0.8, 1, 2.5, 2.5, 0.8},
		{"long flat score unchanged", 0.8, 1, 0, 2.5, 0.8},
		{"short fully penalized at full bullish score", 0.8, -1, 2.5, 2.5, 0.0},
		{"short with-trend (bearish score) unchanged", 0.8, -1, -2.5, 2.5, 0.8},
		{"penalty clamps at zero past full", 0.8, 1, -5, 2.5, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trendAlignPenalty(tt.rawConf, tt.sideSign, tt.score, tt.fullScore)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("trendAlignPenalty(raw=%.2f side=%d score=%.2f full=%.2f) = %.4f, want %.4f",
					tt.rawConf, tt.sideSign, tt.score, tt.fullScore, got, tt.want)
			}
		})
	}
}
