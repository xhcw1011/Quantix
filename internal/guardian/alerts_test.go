package guardian

import "testing"

func TestProfitMilestone_FiresOncePerLevel(t *testing.T) {
	r := &ProfitMilestone{Step: 1}
	if fire, _ := r.Eval(AlertCtx{PnlR: 0.5}); fire {
		t.Fatal("should not fire below 1R")
	}
	if fire, msg := r.Eval(AlertCtx{PnlR: 1.2}); !fire || msg == "" {
		t.Fatal("should fire at +1R")
	}
	if fire, _ := r.Eval(AlertCtx{PnlR: 1.9}); fire {
		t.Fatal("should not re-fire +1R")
	}
	if fire, _ := r.Eval(AlertCtx{PnlR: 2.3}); !fire {
		t.Fatal("should fire at +2R")
	}
}

func TestStopProximity_EdgeTriggered(t *testing.T) {
	r := &StopProximity{WithinR: 0.3}
	if fire, _ := r.Eval(AlertCtx{StopDistR: 0.5}); fire {
		t.Fatal("far from stop: no fire")
	}
	if fire, _ := r.Eval(AlertCtx{StopDistR: 0.2}); !fire {
		t.Fatal("entering proximity: fire")
	}
	if fire, _ := r.Eval(AlertCtx{StopDistR: 0.1}); fire {
		t.Fatal("still inside: no repeat")
	}
	if fire, _ := r.Eval(AlertCtx{StopDistR: 0.6}); fire {
		t.Fatal("leaving: no fire")
	}
	if fire, _ := r.Eval(AlertCtx{StopDistR: 0.2}); !fire {
		t.Fatal("re-entering: fire again")
	}
}

func TestStagnation_FiresOnce(t *testing.T) {
	r := &Stagnation{Bars: 5, BandR: 0.3}
	if fire, _ := r.Eval(AlertCtx{BarsHeld: 3, PnlR: 0.1}); fire {
		t.Fatal("not held long enough")
	}
	if fire, _ := r.Eval(AlertCtx{BarsHeld: 6, PnlR: 0.5}); fire {
		t.Fatal("moved enough, not stagnant")
	}
	if fire, _ := r.Eval(AlertCtx{BarsHeld: 6, PnlR: 0.1}); !fire {
		t.Fatal("held long + flat: fire")
	}
	if fire, _ := r.Eval(AlertCtx{BarsHeld: 7, PnlR: 0.1}); fire {
		t.Fatal("stagnation fires once")
	}
}

func TestVolSpike_EdgeTriggered(t *testing.T) {
	r := &VolSpike{Mult: 2}
	if fire, _ := r.Eval(AlertCtx{ATR: 3, AvgATR: 2}); fire {
		t.Fatal("3 < 2*2: no spike")
	}
	if fire, _ := r.Eval(AlertCtx{ATR: 5, AvgATR: 2}); !fire {
		t.Fatal("5 >= 4: spike")
	}
	if fire, _ := r.Eval(AlertCtx{ATR: 6, AvgATR: 2}); fire {
		t.Fatal("still elevated: no repeat")
	}
	if fire, _ := r.Eval(AlertCtx{ATR: 3, AvgATR: 2}); fire {
		t.Fatal("calmed: no fire")
	}
}

func TestLevelCross_BothDirections(t *testing.T) {
	r := &LevelCross{Level: 100}
	r.Eval(AlertCtx{Price: 98}) // establishes side, no fire
	if fire, _ := r.Eval(AlertCtx{Price: 101}); !fire {
		t.Fatal("crossed up: fire")
	}
	if fire, _ := r.Eval(AlertCtx{Price: 103}); fire {
		t.Fatal("still above: no repeat")
	}
	if fire, _ := r.Eval(AlertCtx{Price: 99}); !fire {
		t.Fatal("crossed down: fire")
	}
}

func TestMAState_CrossFact(t *testing.T) {
	r := &MAState{}
	r.Eval(AlertCtx{Price: 98, MA: 100, HasMA: true}) // below, establish
	if fire, msg := r.Eval(AlertCtx{Price: 102, MA: 100, HasMA: true}); !fire || msg == "" {
		t.Fatal("price crossed above MA: fire")
	}
	if fire, _ := r.Eval(AlertCtx{Price: 105, MA: 100, HasMA: true}); fire {
		t.Fatal("still above: no repeat")
	}
	// no MA available => never fires
	r2 := &MAState{}
	if fire, _ := r2.Eval(AlertCtx{Price: 102, HasMA: false}); fire {
		t.Fatal("no MA: never fires")
	}
}

func TestAlertEngine_CollectsFired(t *testing.T) {
	e := NewAlertEngine()
	e.Add(&ProfitMilestone{Step: 1})
	e.Add(&LevelCross{Level: 100})
	e.Evaluate(AlertCtx{Price: 98, PnlR: 0}) // establish level side
	out := e.Evaluate(AlertCtx{Price: 101, PnlR: 1.2})
	if len(out) != 2 {
		t.Fatalf("want 2 alerts (milestone+cross), got %d", len(out))
	}
}
