package orgateway

import (
	"testing"

	"go.uber.org/zap"

	"github.com/Quantix/quantix/internal/strategy"
)

type recBroker struct {
	placed  []strategy.OrderRequest
	cancels []string
}

func (b *recBroker) PlaceOrder(req strategy.OrderRequest) string {
	b.placed = append(b.placed, req)
	return "oid"
}
func (b *recBroker) CancelOrder(id string) error { b.cancels = append(b.cancels, id); return nil }

type stubState struct{ st OrderState }

func (s stubState) Snapshot(_ strategy.OrderRequest) OrderState { return s.st }

func gw(mode Mode, inner strategy.Broker, st OrderState, rules ...Rule) *Gateway {
	return New(inner, rules, stubState{st}, mode, zap.NewNop())
}

func TestGatewayShadowAlwaysForwards(t *testing.T) {
	inner := &recBroker{}
	g := gw(Shadow, inner, OrderState{Equity: 10000, Price: 100}, MaxSingleTradePctRule{Max: 0.02})
	id := g.PlaceOrder(openLong(5)) // would DENY (5%>2%)
	if id != "oid" {
		t.Fatalf("shadow must forward and return inner id, got %q", id)
	}
	if len(inner.placed) != 1 {
		t.Fatal("shadow must forward the denied order to inner broker")
	}
	if g.Stats()[ReasonMaxSingleTradePct] != 1 {
		t.Fatalf("deny must be recorded in stats, got %v", g.Stats())
	}
}

func TestGatewayEnforceBlocksDeny(t *testing.T) {
	inner := &recBroker{}
	g := gw(Enforce, inner, OrderState{Equity: 10000, Price: 100}, MaxSingleTradePctRule{Max: 0.02})
	id := g.PlaceOrder(openLong(5))
	if id != "" {
		t.Fatalf("enforce must return empty id on DENY, got %q", id)
	}
	if len(inner.placed) != 0 {
		t.Fatal("enforce must NOT forward a denied order")
	}
}

func TestGatewayEnforceForwardsAllow(t *testing.T) {
	inner := &recBroker{}
	g := gw(Enforce, inner, OrderState{Equity: 10000, Price: 100}, MaxSingleTradePctRule{Max: 0.50})
	id := g.PlaceOrder(openLong(1)) // 1% <= 50%
	if id != "oid" || len(inner.placed) != 1 {
		t.Fatal("enforce must forward an allowed order")
	}
	if g.Stats()["ALLOW"] != 1 {
		t.Fatalf("allow must be recorded, got %v", g.Stats())
	}
}

func TestGatewayCancelPassthrough(t *testing.T) {
	inner := &recBroker{}
	g := gw(Enforce, inner, OrderState{}, MaxSingleTradePctRule{Max: 0.02})
	_ = g.CancelOrder("x")
	if len(inner.cancels) != 1 {
		t.Fatal("cancel must always pass through (ORG never blocks cancels)")
	}
}

func TestGatewayFirstFailingRuleWins(t *testing.T) {
	inner := &recBroker{}
	g := gw(Enforce, inner, OrderState{Equity: 10000, Price: 100},
		MaxPositionPctRule{Max: 1.0},     // allows
		MaxSingleTradePctRule{Max: 0.02}, // denies
	)
	if id := g.PlaceOrder(openLong(5)); id != "" {
		t.Fatal("should DENY on the single-trade rule")
	}
	if g.Stats()[ReasonMaxSingleTradePct] != 1 {
		t.Fatalf("reason should be single-trade, got %v", g.Stats())
	}
}
