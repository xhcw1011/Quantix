package pool

import "sync"

// Manager owns the set of pools and the strategy→pool membership, aggregates member
// states each update, and publishes each pool's Snapshot. It is the Capital Layer's
// decision point: it never blocks an order — the ORG reads StatusFor(...) and
// enforces. Concurrency-safe (live engines update/query it from many goroutines).
type Manager struct {
	mu    sync.Mutex
	pools map[string]*Pool
	strat map[string]string // strategyID → pool name (read-only after construction)
	snaps map[string]Snapshot
}

// NewManager builds a manager from pool configs and a strategy→pool membership map.
func NewManager(configs []Config, membership map[string]string) *Manager {
	pools := make(map[string]*Pool, len(configs))
	snaps := make(map[string]Snapshot, len(configs))
	for _, c := range configs {
		pools[c.Name] = New(c)
		snaps[c.Name] = Snapshot{Name: c.Name, Status: Active, MaxLongExp: c.MaxLongExp, MaxShortExp: c.MaxShortExp}
	}
	return &Manager{pools: pools, strat: membership, snaps: snaps}
}

// Update aggregates the latest per-strategy member states into their pools and
// republishes every pool's Snapshot (pools with no members this tick update empty).
func (m *Manager) Update(states map[string]MemberState) {
	byPool := make(map[string][]MemberState)
	for sid, st := range states {
		pn := m.poolOf(sid)
		byPool[pn] = append(byPool[pn], st)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, p := range m.pools {
		m.snaps[name] = p.Update(byPool[name])
	}
}

// StatusFor returns the published Snapshot for a strategy's pool. Fail-open: an
// unmapped strategy or a missing pool returns an ACTIVE snapshot with no caps, so a
// stale/absent Capital Layer never blocks trading (ORG's order-safety rules still apply).
func (m *Manager) StatusFor(strategyID string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	pn := m.poolOf(strategyID)
	if s, ok := m.snaps[pn]; ok {
		return s
	}
	return Snapshot{Name: pn, Status: Active}
}

// SetStatus applies a manual operator override to a pool (returns false if unknown).
func (m *Manager) SetStatus(poolName string, s Status) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pools[poolName]
	if !ok {
		return false
	}
	p.SetStatus(s)
	snap := m.snaps[poolName]
	snap.Status = s
	m.snaps[poolName] = snap
	return true
}

func (m *Manager) poolOf(strategyID string) string {
	if pn, ok := m.strat[strategyID]; ok {
		return pn
	}
	return "default"
}
