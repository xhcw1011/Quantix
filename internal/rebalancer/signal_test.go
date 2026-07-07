package rebalancer

import "testing"

func TestBuildStates(t *testing.T) {
	// dates d0..d3; W=2 trailing funding window, volWin=2.
	series := map[string]Series{
		"A": {
			Price:   map[string]float64{"d0": 10, "d1": 11, "d2": 12, "d3": 13},
			Volume:  map[string]float64{"d0": 5e6, "d1": 5e6, "d2": 5e6, "d3": 5e6},
			Funding: map[string]float64{"d0": 0.001, "d1": 0.002, "d2": 0.003, "d3": 0.004},
			First:   "d0",
		},
	}
	dates := []string{"d0", "d1", "d2", "d3"}
	got := BuildStates(series, dates, "d3", 2, 2)
	if len(got) != 1 {
		t.Fatalf("want 1 state, got %d", len(got))
	}
	c := got[0]
	if c.Symbol != "A" || c.Price != 13 {
		t.Fatalf("price/symbol wrong: %+v", c)
	}
	if c.TrailFunding != 0.007 { // d2+d3 = 0.003+0.004
		t.Fatalf("trail funding = %v, want 0.007", c.TrailFunding)
	}
	if c.TrailVolume != 5e6 {
		t.Fatalf("trail vol = %v, want 5e6", c.TrailVolume)
	}
	if c.DaysListed != 3 { // index(d3)=3 - firstIdx(d0)=0
		t.Fatalf("days listed = %v, want 3", c.DaysListed)
	}
}

func TestBuildStatesSkipsMissingPrice(t *testing.T) {
	series := map[string]Series{
		"A": {Price: map[string]float64{"d3": 13}, Volume: map[string]float64{"d3": 5e6},
			Funding: map[string]float64{"d3": 0.001}, First: "d3"},
		"B": {Price: map[string]float64{}, Volume: map[string]float64{},
			Funding: map[string]float64{}, First: "d3"}, // no price on asOf → skipped
	}
	got := BuildStates(series, []string{"d3"}, "d3", 2, 2)
	if len(got) != 1 || got[0].Symbol != "A" {
		t.Fatalf("want only A, got %+v", got)
	}
}
