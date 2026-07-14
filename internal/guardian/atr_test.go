package guardian

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestATR_RollingMeanOfTrueRange(t *testing.T) {
	a := NewATR(3)

	// TR = max(high-low, |high-prevClose|, |low-prevClose|)
	// bar1: max(2,1,1)=2
	if v := a.Update(10, 8, 9); !approx(v, 0) || a.Ready() {
		t.Fatalf("after 1 bar: got %v ready=%v, want 0 not-ready", v, a.Ready())
	}
	// bar2: max(3,3,0)=3
	if v := a.Update(12, 9, 9); !approx(v, 0) || a.Ready() {
		t.Fatalf("after 2 bars: got %v ready=%v, want 0 not-ready", v, a.Ready())
	}
	// bar3: max(1,1,2)=2 -> ATR=(2+3+2)/3=2.3333...
	v := a.Update(11, 10, 12)
	if !a.Ready() || !approx(v, (2.0+3.0+2.0)/3.0) {
		t.Fatalf("after 3 bars: got %v ready=%v, want 2.3333 ready", v, a.Ready())
	}
	if !approx(a.Value(), (2.0+3.0+2.0)/3.0) {
		t.Fatalf("Value() = %v", a.Value())
	}
	// bar4: max(2,3,1)=3 -> window slides to (3,2,3)/3=2.6667
	v = a.Update(13, 11, 10)
	if !approx(v, (3.0+2.0+3.0)/3.0) {
		t.Fatalf("after 4 bars: got %v, want 2.6667", v)
	}
}

func TestATR_WindowMustBePositive(t *testing.T) {
	// window<=0 degrades to window=1 rather than panicking.
	a := NewATR(0)
	v := a.Update(10, 8, 9) // TR=2, window=1 => ready immediately
	if !a.Ready() || !approx(v, 2) {
		t.Fatalf("window=0 fallback: got %v ready=%v, want 2 ready", v, a.Ready())
	}
}
