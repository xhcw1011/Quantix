package live

import (
	"testing"

	"github.com/Quantix/quantix/internal/strategy"
)

func TestExceedsGrossExposure(t *testing.T) {
	tests := []struct {
		name                                    string
		equity, lev, frac, gross, newQty, price float64
		want                                    bool
	}{
		// equity 3600, 10x, 0.8 → cap notional = 28,800
		{"runaway blocked (31 ETH on $3.6k 10x)", 3600, 10, 0.8, 31, 0.1, 1700, true},
		{"normal add allowed (3+0.1 ETH)", 3600, 10, 0.8, 3, 0.1, 1700, false},
		// equity 1000, 10x, 0.8 → cap = 8000
		{"exactly at cap not blocked", 1000, 10, 0.8, 0, 8, 1000, false},
		{"just over cap blocked", 1000, 10, 0.8, 0, 8.001, 1000, true},
		// guard-off / insufficient-info cases never block
		{"zero equity does not block", 0, 10, 0.8, 100, 100, 1700, false},
		{"zero price does not block", 3600, 10, 0.8, 100, 100, 0, false},
		{"frac<=0 disables", 3600, 10, 0, 100, 100, 1700, false},
		{"leverage<1 treated as 1", 1000, 0, 0.8, 0, 0.9, 1000, true}, // cap=800, 0.9*1000=900>800
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exceedsGrossExposure(tt.equity, tt.lev, tt.frac, tt.gross, tt.newQty, tt.price); got != tt.want {
				t.Errorf("exceedsGrossExposure(eq=%.0f lev=%.0f frac=%.2f gross=%.3f new=%.3f px=%.0f) = %v, want %v",
					tt.equity, tt.lev, tt.frac, tt.gross, tt.newQty, tt.price, got, tt.want)
			}
		})
	}
}

func TestIsOpeningOrder(t *testing.T) {
	tests := []struct {
		side strategy.Side
		ps   strategy.PositionSide
		want bool
	}{
		{strategy.SideBuy, strategy.PositionSideLong, true},   // open/add long
		{strategy.SideSell, strategy.PositionSideShort, true}, // open/add short
		{strategy.SideSell, strategy.PositionSideLong, false}, // close long
		{strategy.SideBuy, strategy.PositionSideShort, false}, // close short
		{strategy.SideBuy, "", true},                          // net buy = open
		{strategy.SideSell, "", false},                        // net sell = reduce
	}
	for _, tt := range tests {
		got := isOpeningOrder(strategy.OrderRequest{Side: tt.side, PositionSide: tt.ps})
		if got != tt.want {
			t.Errorf("isOpeningOrder(side=%s pos=%s) = %v, want %v", tt.side, tt.ps, got, tt.want)
		}
	}
}
