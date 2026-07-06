package pool

import "sync"

// Manager owns the set of pools and the strategy→pool membership, aggregates member
// states each update, and publishes each pool's Snapshot. It is the Capital Layer's
// decision point: it never blocks an order — the ORG reads StatusFor(...) and
// enforces. Concurrency-safe (live engines update/query it from many goroutines).
type Manager struct {
	mu     sync.Mutex
	pools  map[string]*Pool
	strat  map[string]string // strategyID → pool name
	latest map[string]MemberState
	snaps  map[string]Snapshot
}

// NewManager builds a manager from pool configs and a strategy→pool membership map
// (membership may be empty and filled later with Assign).
func NewManager(configs []Config, membership map[string]string) *Manager {
	pools := make(map[string]*Pool, len(configs))
	snaps := make(map[string]Snapshot, len(configs))
	for _, c := range configs {
		pools[c.Name] = New(c)
		snaps[c.Name] = Snapshot{Name: c.Name, Status: Active, MaxLongExp: c.MaxLongExp, MaxShortExp: c.MaxShortExp}
	}
	if membership == nil {
		membership = map[string]string{}
	}
	return &Manager{pools: pools, strat: membership, latest: map[string]MemberState{}, snaps: snaps}
}

// Assign maps a strategy to a pool (dynamic membership, e.g. as engines start).
func (m *Manager) Assign(strategyID, poolName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.strat[strategyID] = poolName
}

// Report caches one strategy's latest member state and recomputes its pool's
// Snapshot from all that pool's members. This is how live engines feed the Capital
// Layer — each engine reports independently, the Manager aggregates.
//
// NOTE (shadow-era simplification): the halt/recovery hysteresis advances once per
// Report, so RecoverBars currently counts reports, not bars. Fine while shadow only
// logs; a tick-based advance is the refinement before enforcement.
func (m *Manager) Report(strategyID string, st MemberState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.latest[strategyID] = st
	pn, ok := m.strat[strategyID]
	if !ok {
		return // unassigned strategy — not pooled
	}
	p, ok := m.pools[pn]
	if !ok {
		return
	}
	var states []MemberState
	for sid, pName := range m.strat {
		if pName == pn {
			if ms, ok := m.latest[sid]; ok {
				states = append(states, ms)
			}
		}
	}
	m.snaps[pn] = p.Update(states)
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
