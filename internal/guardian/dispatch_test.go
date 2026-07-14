package guardian

import "testing"

func TestSMA_RollingMean(t *testing.T) {
	s := NewSMA(3)
	s.Add(10)
	s.Add(20)
	if s.Ready() {
		t.Fatal("SMA not ready before window fills")
	}
	s.Add(30)
	if !s.Ready() || !approx(s.Value(), 20) {
		t.Fatalf("sma=%v ready=%v want 20", s.Value(), s.Ready())
	}
	s.Add(60) // (20+30+60)/3
	if !approx(s.Value(), (20.0+30+60)/3) {
		t.Fatalf("sma=%v", s.Value())
	}
}

type fakeDispatch struct{ sent []Alert }

func (f *fakeDispatch) Send(title, msg string) error {
	f.sent = append(f.sent, Alert{Title: title, Msg: msg})
	return nil
}

func TestGuardian_DispatchesMilestoneAlert(t *testing.T) {
	g, _, ctx := newLongGuardian(trailCfg())
	eng := NewAlertEngine()
	eng.Add(&ProfitMilestone{Step: 1})
	fd := &fakeDispatch{}
	g.SetAlerts(eng, fd)

	g.OnTick(ctx, 106) // pnlR = 1.2 -> +1R milestone fires
	if len(fd.sent) != 1 {
		t.Fatalf("want 1 dispatched alert, got %d", len(fd.sent))
	}
	if fd.sent[0].Title != "profit_milestone" {
		t.Fatalf("alert title = %q", fd.sent[0].Title)
	}
}
