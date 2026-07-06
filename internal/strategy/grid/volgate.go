package grid

// volGate is a volatility on/off switch for the grid, ported from the
// scripts/breakout_score.py + grid_gate_backtest.py research. The de-biased
// breakout signal that survived out-of-sample was VOLUME (level + rising), so
// the gate scores each bar's volume as a causal percentile composite and runs a
// 3-mechanism state machine on top:
//
//	score = 0.5*vol_hi + 0.5*vol_up   (both in [0,1])
//	  vol_hi = percentile of this bar's volume within the last Window bars
//	  vol_up = percentile of (vol / mean(prev RatioBars vols)) within the window
//
//	① Hysteresis : exit the grid when score >= ExitThresh (0.70); only re-enter
//	   once score < EnterThresh (0.40) — a dead band that kills flip-flop.
//	② Cooldown   : after an exit, wait >= Cooldown bars before re-entry is allowed.
//	③ Persistence: require Persistence consecutive low (< EnterThresh) bars to re-enter.
//
// update() returns whether the grid should be ACTIVE this bar (true) or gated
// off (false). It is deliberately self-contained and side-effect free so it can
// be unit-tested without any market plumbing.
type volGateCfg struct {
	Window      int     // percentile lookback (bars)
	RatioBars   int     // bars averaged for the vol_up "rising" ratio
	ExitThresh  float64 // score >= this → volatile → gate off
	EnterThresh float64 // score < this → eligible-calm for re-entry
	Cooldown    int     // bars to wait after an exit
	Persistence int     // consecutive low bars required to re-enter
}

type volGate struct {
	cfg       volGateCfg
	vols      []float64
	on        bool // true = grid active, false = gated off
	sinceExit int
	lowStreak int
}

func newVolGate(cfg volGateCfg) *volGate {
	if cfg.RatioBars <= 0 {
		cfg.RatioBars = 8
	}
	if cfg.Persistence <= 0 {
		cfg.Persistence = 1
	}
	return &volGate{cfg: cfg, on: true}
}

// pctile returns the fraction of sample values <= val, in [0,1]. Empty → 0.5.
func pctile(val float64, sample []float64) float64 {
	if len(sample) == 0 {
		return 0.5
	}
	c := 0
	for _, x := range sample {
		if x <= val {
			c++
		}
	}
	return float64(c) / float64(len(sample))
}

// update feeds the latest bar volume and returns whether the grid is active.
func (v *volGate) update(vol float64) bool {
	v.vols = append(v.vols, vol)
	n := len(v.vols)
	// Warmup: not enough history to judge volatility → keep the grid running.
	if v.cfg.Window <= 0 || n < v.cfg.Window || n <= v.cfg.RatioBars {
		return true
	}

	win := v.vols[n-v.cfg.Window:]
	volHi := pctile(vol, win)

	ratios := make([]float64, 0, v.cfg.Window)
	for i := n - v.cfg.Window; i < n; i++ {
		if i < v.cfg.RatioBars {
			continue
		}
		var s float64
		for j := i - v.cfg.RatioBars; j < i; j++ {
			s += v.vols[j]
		}
		avg := s / float64(v.cfg.RatioBars)
		if avg > 0 {
			ratios = append(ratios, v.vols[i]/avg)
		}
	}
	volUp := 0.5
	if len(ratios) > 0 {
		volUp = pctile(ratios[len(ratios)-1], ratios) // last = current bar's ratio
	}

	return v.step(0.5*volHi + 0.5*volUp)
}

// step runs the hysteresis/cooldown/persistence state machine for one score.
func (v *volGate) step(score float64) bool {
	if v.on {
		if score >= v.cfg.ExitThresh {
			v.on = false
			v.sinceExit = 0
			v.lowStreak = 0
			return false
		}
		return true
	}
	v.sinceExit++
	if score < v.cfg.EnterThresh {
		v.lowStreak++
	} else {
		v.lowStreak = 0
	}
	if v.sinceExit >= v.cfg.Cooldown && v.lowStreak >= v.cfg.Persistence {
		v.on = true
		return true
	}
	return false
}
