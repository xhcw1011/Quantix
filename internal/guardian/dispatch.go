package guardian

import "github.com/Quantix/quantix/internal/notify"

// Dispatcher delivers a fired alert. Kept tiny so tests can fake it and the
// production adapter can wrap the existing notifier.
type Dispatcher interface {
	Send(title, msg string) error
}

// NotifyDispatcher adapts internal/notify.Notifier (Telegram + email) to Dispatcher.
type NotifyDispatcher struct{ n *notify.Notifier }

// NewNotifyDispatcher wraps a notifier (nil-safe; sends become no-ops).
func NewNotifyDispatcher(n *notify.Notifier) *NotifyDispatcher {
	return &NotifyDispatcher{n: n}
}

// Send routes the alert through the notifier's SystemAlert channel.
func (d *NotifyDispatcher) Send(title, msg string) error {
	if d.n == nil || !d.n.Enabled() {
		return nil
	}
	d.n.SystemAlert("GUARDIAN", title+" — "+msg)
	return nil
}
