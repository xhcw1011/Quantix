package strategy

// PendingEntry tracks a resting limit entry order that should fall back to a
// market order if unfilled after a bar-count budget. The zero value means "no
// pending entry" — any strategy that wants limit-entry-with-timeout composes
// one PendingEntry field and drives it from OnBar: place the limit order,
// store the resulting OrderID and a market Fallback, then each subsequent bar
// call Timeout until either the position appears (call Clear) or the timeout
// is reached (cancel OrderID, submit Fallback, call Clear).
type PendingEntry struct {
	OrderID  string
	Bars     int
	Fallback OrderRequest // submitted verbatim on timeout
}

// Active reports whether a limit entry is currently resting and being tracked.
func (p *PendingEntry) Active() bool { return p.OrderID != "" }

// Clear resets to the zero value (no pending entry).
func (p *PendingEntry) Clear() { *p = PendingEntry{} }

// Timeout advances the bar counter and reports whether timeoutBars has been
// reached. Callers should only call this when Active() is true.
func (p *PendingEntry) Timeout(timeoutBars int) bool {
	p.Bars++
	return p.Bars >= timeoutBars
}
