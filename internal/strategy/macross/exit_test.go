package macross

import "testing"

func TestUpdateLossStreak_IncrementsWhileBeyondTrigger(t *testing.T) {
	streak := updateLossStreak(0, -0.015, 0.01) // -1.5% floating, trigger -1%
	if streak != 1 {
		t.Fatalf("want 1, got %d", streak)
	}
	streak = updateLossStreak(streak, -0.02, 0.01)
	if streak != 2 {
		t.Fatalf("want 2, got %d", streak)
	}
}

func TestUpdateLossStreak_ResetsWhenRecovered(t *testing.T) {
	streak := updateLossStreak(3, -0.002, 0.01) // recovered above -1% trigger
	if streak != 0 {
		t.Fatalf("want 0 (reset), got %d", streak)
	}
}

func TestShouldReduce_FiresOnceStreakReachesConfirmBars(t *testing.T) {
	if shouldReduce(1, 2, false) {
		t.Fatal("streak below confirmBars should not reduce")
	}
	if !shouldReduce(2, 2, false) {
		t.Fatal("streak at confirmBars should reduce")
	}
}

func TestShouldReduce_DoesNotFireTwice(t *testing.T) {
	if shouldReduce(5, 2, true) {
		t.Fatal("already-reduced position should not reduce again")
	}
}

func TestShouldTrailClose_NoCloseBelowActivation(t *testing.T) {
	// peak profit never reached the activation threshold -- must not close,
	// no matter how far the (small) profit retraces.
	if shouldTrailClose(0.03, 0.0, 0.05, 0.35) {
		t.Fatal("should not trail-close when peak never activated")
	}
}

func TestShouldTrailClose_ClosesOnGivebackFromPeak(t *testing.T) {
	// peak 10% profit, giveback 35% of that = retrace to 6.5%; at 6% current
	// profit that's past the giveback line -- should close.
	if !shouldTrailClose(0.10, 0.06, 0.05, 0.35) {
		t.Fatal("should trail-close after giving back 35% of a 10% peak")
	}
}

func TestShouldTrailClose_HoldsIfStillNearPeak(t *testing.T) {
	// same 10% peak, but only retraced to 9% -- well within the 35% giveback
	// band -- should hold, not close.
	if shouldTrailClose(0.10, 0.09, 0.05, 0.35) {
		t.Fatal("should not trail-close so close to the peak")
	}
}
