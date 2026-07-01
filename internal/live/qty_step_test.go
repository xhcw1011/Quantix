package live

import (
	"math"
	"testing"
)

func TestRoundQtyToStep(t *testing.T) {
	tests := []struct {
		name      string
		qty, step float64
		want      float64
	}{
		// The bug: BTC auto-size 3103*0.99/60000 ≈ 0.05120565 rejected as -1111.
		{"BTC auto-size floors to 0.001", 0.05120565, 0.001, 0.051},
		// Float-safety: 2.116/0.001 = 2115.9999999998 must NOT floor to 2.115.
		{"exact multiple stays (float-safe)", 2.116, 0.001, 2.116},
		{"already clean stays", 0.052, 0.001, 0.052},
		{"floors down, never up", 0.0519, 0.001, 0.051},
		{"below one step -> 0", 0.0009, 0.001, 0.0},
		// Guard branches: never mutate when we can't/shouldn't round.
		{"step<=0 returns qty unchanged", 0.05120565, 0, 0.05120565},
		{"zero qty unchanged", 0, 0.001, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roundQtyToStep(tt.qty, tt.step)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("roundQtyToStep(%v, %v) = %v, want %v", tt.qty, tt.step, got, tt.want)
			}
		})
	}
}
