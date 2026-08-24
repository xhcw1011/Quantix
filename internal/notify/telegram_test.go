package notify

import (
	"strings"
	"testing"
)

// TestStripMarkdownMarkers is the regression test for the 2026-08-21 incident:
// dynamic content (e.g. guardian's "[BTCUSDT] ..." messages) sent through
// Telegram's Markdown parser triggered "can't parse entities", silently
// dropping every guardian retirement alert on a real-money account. The fix
// stops asking Telegram to parse Markdown at all; this only needs to confirm
// the decorative markers our templates still emit don't leak through as
// literal characters once nothing parses them.
func TestStripMarkdownMarkers(t *testing.T) {
	in := "📌 *系统 GUARDIAN*\nclosed — [BTCUSDT] 已按保护单平仓 @ `75,056.30`,守护结束\n_15:04:05_"
	got := stripMarkdownMarkers(in)
	for _, ch := range []string{"*", "`", "_"} {
		if strings.Contains(got, ch) {
			t.Fatalf("stripMarkdownMarkers left %q in output: %q", ch, got)
		}
	}
	if !strings.Contains(got, "[BTCUSDT]") {
		t.Fatalf("stripMarkdownMarkers must not touch non-markdown content, got %q", got)
	}
}

func TestSplitSubjectBody(t *testing.T) {
	subject, body := splitSubjectBody("⚡ *风控告警* [BTCUSDT-15m-macross]\n熔断已触发\n净值: `$100.00`")
	if subject != "⚡ 风控告警 [BTCUSDT-15m-macross]" {
		t.Fatalf("subject = %q", subject)
	}
	if strings.ContainsAny(body, "`*") {
		t.Fatalf("body still contains markdown markers: %q", body)
	}
}
