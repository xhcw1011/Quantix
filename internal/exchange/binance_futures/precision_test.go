package binance_futures

import "testing"

func TestStepDecimals(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0.00100000", 3}, {"0.10", 1}, {"1.0", 0}, {"1", 0},
		{"0.001", 3}, {"0.5", 1}, {"0.00001000", 5},
	}
	for _, c := range cases {
		if got := stepDecimals(c.in); got != c.want {
			t.Errorf("stepDecimals(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseStep(t *testing.T) {
	cases := []struct {
		in       string
		wantStep float64
		wantDec  int
	}{
		{"0.00100000", 0.001, 3},
		{"0.1", 0.1, 1},
		{"1", 1, 0},
		{"bad", 0, 0},
		{"0", 0, 0},
		{"", 0, 0},
	}
	for _, c := range cases {
		gotStep, gotDec := parseStep(c.in)
		if gotStep != c.wantStep || gotDec != c.wantDec {
			t.Errorf("parseStep(%q) = (%v,%d), want (%v,%d)", c.in, gotStep, gotDec, c.wantStep, c.wantDec)
		}
	}
}

func TestFloorToStep(t *testing.T) {
	cases := []struct {
		v, step, want float64
	}{
		{0.05120565, 0.001, 0.051}, // the BTC -1111 bug
		{2.116, 0.001, 2.116},      // float-safe: must NOT floor to 2.115
		{0.0009, 0.001, 0.0},       // below one step
		{5, 0, 5},                  // step<=0 unchanged
	}
	for _, c := range cases {
		if got := floorToStep(c.v, c.step); got < c.want-1e-9 || got > c.want+1e-9 {
			t.Errorf("floorToStep(%v,%v) = %v, want %v", c.v, c.step, got, c.want)
		}
	}
}

func TestRoundToStep(t *testing.T) {
	cases := []struct {
		v, step, want float64
	}{
		{61844.393, 0.1, 61844.4}, // the stop-price -1111 bug
		{1627.437, 0.01, 1627.44},
		{61844.35, 0.1, 61844.4}, // round half away
		{5, 0, 5},
	}
	for _, c := range cases {
		if got := roundToStep(c.v, c.step); got < c.want-1e-6 || got > c.want+1e-6 {
			t.Errorf("roundToStep(%v,%v) = %v, want %v", c.v, c.step, got, c.want)
		}
	}
}

func TestSymbolFilterFormat(t *testing.T) {
	btc := symbolFilter{stepSize: 0.001, tickSize: 0.1, qtyDecimals: 3, priceDecimals: 1}
	if got := btc.qtyStr(0.05120565); got != "0.051" {
		t.Errorf("btc.qtyStr = %q, want 0.051", got)
	}
	if got := btc.priceStr(61844.393); got != "61844.4" {
		t.Errorf("btc.priceStr = %q, want 61844.4", got)
	}
	// Unknown filter (zero value) → safe fallbacks (3dp qty, 2dp price).
	var unknown symbolFilter
	if got := unknown.qtyStr(0.05120565); got != "0.051" {
		t.Errorf("unknown.qtyStr = %q, want 0.051", got)
	}
	if got := unknown.priceStr(1627.4); got != "1627.40" {
		t.Errorf("unknown.priceStr = %q, want 1627.40", got)
	}
}
