package pool

import "testing"

// The core isolation property: a Growth-pool drawdown halts only Growth's members;
// Yield is untouched.
func TestManagerIsolatesPools(t *testing.T) {
	m := NewManager(
		[]Config{
			{Name: "Growth", NotionalCap: 10000, MaxDrawdown: 0.10, RecoverDrawdown: 0.05},
			{Name: "Yield", NotionalCap: 10000, MaxDrawdown: 0.10, RecoverDrawdown: 0.05},
		},
		map[string]string{"macross": "Growth", "spottrend": "Growth", "grid": "Yield"},
	)
	m.Update(map[string]MemberState{
		"macross":   ms(0, -700, 0, 0),  // Growth total unrealized -1300 → equity 8700
		"spottrend": ms(0, -600, 0, 0),  // → DD 13% ≥ 10% → HALT Growth
		"grid":      ms(0, 50, 2000, 0), // Yield healthy
	})

	if m.StatusFor("macross").Status != Halted || m.StatusFor("spottrend").Status != Halted {
		t.Fatal("both Growth members must see HALTED")
	}
	if m.StatusFor("grid").Status != Active {
		t.Fatal("Yield must be unaffected by Growth's drawdown — the whole point")
	}
	if m.StatusFor("unknown").Status != Active {
		t.Fatal("unmapped strategy → fail-open ACTIVE")
	}
}
