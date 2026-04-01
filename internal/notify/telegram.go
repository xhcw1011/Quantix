// Package notify sends trading alerts via Telegram.
package notify

import (
	"fmt"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"go.uber.org/zap"
)

// Notifier sends trading alerts via Telegram and/or email.
// All methods are safe for concurrent use and are no-ops when both channels are disabled.
type Notifier struct {
	bot    *tgbotapi.BotAPI
	chatID int64
	email  *emailSender // may be nil
	log    *zap.Logger
}

// New creates a Telegram notifier.
// Returns a disabled (no-op) notifier if token or chatID is empty.
func New(token string, chatID int64, log *zap.Logger) *Notifier {
	if token == "" || chatID == 0 {
		log.Info("Telegram notifier disabled (no token/chat_id configured)")
		return &Notifier{log: log}
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Error("Telegram bot init failed, notifications disabled", zap.Error(err))
		return &Notifier{log: log}
	}

	log.Info("Telegram notifier ready", zap.String("bot", bot.Self.UserName))
	return &Notifier{bot: bot, chatID: chatID, log: log}
}

// Enabled returns true when at least one notification channel is active.
func (n *Notifier) Enabled() bool { return n.bot != nil || n.email != nil }

// AddEmail attaches an SMTP email sender to this notifier.
// host, port (0→587), user, password, from, to must all be non-empty to enable email.
func (n *Notifier) AddEmail(host string, port int, user, password, from, to string) {
	n.email = newEmailSender(host, port, user, password, from, to)
	if n.email != nil {
		n.log.Info("email notifier ready", zap.String("to", to))
	}
}

// ─── Typed alert methods ──────────────────────────────────────────────────────

// TradeSignal notifies a buy/sell signal.
func (n *Notifier) TradeSignal(symbol, side, strategyID string, price float64) {
	emoji := "📈"
	if side == "SELL" {
		emoji = "📉"
	}
	n.send(fmt.Sprintf(
		"%s *Trade Signal* [%s]\n`%s %s @ $%.2f`\n_%s_",
		emoji, strategyID, side, symbol, price, time.Now().Format("15:04:05"),
	))
}

// FillNotification notifies an order fill.
func (n *Notifier) FillNotification(strategyID, orderID, symbol, side string, qty, price, fee, realizedPnL float64) {
	emoji := "✅"
	pnlStr := ""
	if realizedPnL != 0 {
		sign := "+"
		if realizedPnL < 0 {
			sign = ""
		}
		pnlStr = fmt.Sprintf("\nRealized PnL: `%s$%.2f`", sign, realizedPnL)
	}
	n.send(fmt.Sprintf(
		"%s *Fill* [%s]\n`%s %.6f %s @ $%.2f`\nFee: `$%.4f`%s\n_%s_",
		emoji, strategyID, side, qty, symbol, price, fee, pnlStr,
		time.Now().Format("15:04:05"),
	))
}

// RiskAlert notifies a risk rule violation or circuit breaker.
func (n *Notifier) RiskAlert(strategyID, message string, equity, drawdownPct float64) {
	n.send(fmt.Sprintf(
		"⚡ *RISK ALERT* [%s]\n%s\nEquity: `$%.2f` | Drawdown: `%.2f%%`\n_%s_",
		strategyID, message, equity, drawdownPct,
		time.Now().Format("2006-01-02 15:04:05"),
	))
}

// DailySummary sends a daily P&L report.
func (n *Notifier) DailySummary(strategyID string, equity, realizedPnL, returnPct float64, trades, wins int) {
	sign := "+"
	if returnPct < 0 {
		sign = ""
	}
	winRate := 0.0
	if trades > 0 {
		winRate = float64(wins) / float64(trades) * 100
	}
	n.send(fmt.Sprintf(
		"📊 *Daily Summary* [%s]\n"+
			"Return: `%s%.2f%%` | Equity: `$%.2f`\n"+
			"Realized PnL: `$%.2f`\n"+
			"Trades: `%d` | Win Rate: `%.1f%%`\n"+
			"_%s_",
		strategyID,
		sign, returnPct, equity,
		realizedPnL,
		trades, winRate,
		time.Now().Format("2006-01-02"),
	))
}

// SystemAlert notifies a system event (startup, shutdown, error).
func (n *Notifier) SystemAlert(level, message string) {
	emoji := map[string]string{
		"INFO":  "ℹ️",
		"WARN":  "⚠️",
		"ERROR": "🔴",
	}[level]
	if emoji == "" {
		emoji = "📌"
	}
	text := fmt.Sprintf("%s *System %s*\n%s\n_%s_",
		emoji, level, message, time.Now().Format("15:04:05"))
	if level == "CRITICAL" {
		n.sendCritical(text)
	} else {
		n.send(text)
	}
}

// send delivers a notification via Telegram (markdown) and/or email (plain text).
// No-op when neither channel is enabled.
func (n *Notifier) send(text string) {
	n.sendWithRetry(text, false)
}

// sendCritical delivers a notification with one retry on failure.
func (n *Notifier) sendCritical(text string) {
	n.sendWithRetry(text, true)
}

func (n *Notifier) sendWithRetry(text string, retry bool) {
	if n.bot != nil {
		msg := tgbotapi.NewMessage(n.chatID, text)
		msg.ParseMode = tgbotapi.ModeMarkdown
		if _, err := n.bot.Send(msg); err != nil {
			n.log.Warn("Telegram send failed", zap.Error(err))
			if retry {
				time.Sleep(2 * time.Second)
				if _, err2 := n.bot.Send(msg); err2 != nil {
					n.log.Error("Telegram send CRITICAL retry failed — alert may be lost", zap.Error(err2))
				}
			}
		}
	}
	if n.email != nil {
		subject, body := splitSubjectBody(text)
		if err := n.email.send(subject, body); err != nil {
			n.log.Warn("email send failed", zap.Error(err))
			if retry {
				time.Sleep(2 * time.Second)
				if err2 := n.email.send(subject, body); err2 != nil {
					n.log.Error("email send CRITICAL retry failed", zap.Error(err2))
				}
			}
		}
	}
}

// splitSubjectBody strips markdown markers and splits into subject (first line) + body.
func splitSubjectBody(text string) (subject, body string) {
	plain := text
	for _, ch := range []string{"*", "`", "_"} {
		plain = strings.ReplaceAll(plain, ch, "")
	}
	plain = strings.TrimSpace(plain)
	if idx := strings.IndexByte(plain, '\n'); idx >= 0 {
		return strings.TrimSpace(plain[:idx]), plain
	}
	return plain, plain
}
