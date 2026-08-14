package api

import (
	"strings"
	"testing"
)

// TestFilterLogLines_EngineIDActuallyFilters reproduces a real bug found
// 2026-08-06: recentLogs's engine_id filter branch had an empty body (a
// no-op "if ... { /* comment, no continue */ }"), so /api/engines/{id}/
// recent-logs silently ignored the id path param and returned the ENTIRE
// shared server log file to any authenticated user regardless of which
// engine_id they asked for — including every other user's log lines, since
// this is one shared log file across the whole multi-tenant server.
func TestFilterLogLines_EngineIDActuallyFilters(t *testing.T) {
	lines := []string{
		`{"msg":"engine started","id":"BTCUSDT-5m-guardian"}`,
		`{"msg":"engine started","id":"ETHUSDT-15m-macross"}`,
		`{"msg":"order rejected","id":"BTCUSDT-5m-guardian"}`,
		`{"msg":"system startup, no engine id"}`,
	}
	got := filterLogLines(lines, "BTCUSDT-5m-guardian", "", 100)
	if len(got) != 2 {
		t.Fatalf("expected only lines mentioning the requested engine_id, got %d: %v", len(got), got)
	}
	for _, l := range got {
		if !strings.Contains(l, "BTCUSDT-5m-guardian") {
			t.Fatalf("leaked a line not mentioning the requested engine_id: %q", l)
		}
	}
}

func TestFilterLogLines_GrepStillWorks(t *testing.T) {
	lines := []string{
		`{"level":"error","msg":"boom"}`,
		`{"level":"info","msg":"fine"}`,
	}
	got := filterLogLines(lines, "", "error", 100)
	if len(got) != 1 || got[0] != lines[0] {
		t.Fatalf("grep filter broken: got %v", got)
	}
}

func TestFilterLogLines_CombinedFilterAndsNotOrs(t *testing.T) {
	lines := []string{
		`{"id":"BTCUSDT-5m-guardian","level":"error"}`,
		`{"id":"BTCUSDT-5m-guardian","level":"info"}`,
		`{"id":"ETHUSDT-15m-macross","level":"error"}`,
	}
	got := filterLogLines(lines, "BTCUSDT-5m-guardian", "error", 100)
	if len(got) != 1 {
		t.Fatalf("expected exactly the line matching BOTH filters, got %d: %v", len(got), got)
	}
}

func TestFilterLogLines_TrimsToLimitAfterFiltering(t *testing.T) {
	lines := []string{"a-1", "b", "a-2", "b", "a-3"}
	got := filterLogLines(lines, "a-", "", 2)
	if len(got) != 2 || got[0] != "a-2" || got[1] != "a-3" {
		t.Fatalf("expected the last 2 matches, got %v", got)
	}
}
