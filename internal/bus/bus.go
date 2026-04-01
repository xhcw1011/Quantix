package bus

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Bus wraps a NATS connection with typed publish / subscribe helpers.
// All methods are safe for concurrent use.
type Bus struct {
	nc  *nats.Conn
	log *zap.Logger
}

// Connect establishes a NATS connection with automatic reconnect.
// Returns a ready-to-use Bus or an error if the server is unreachable.
func Connect(url string, log *zap.Logger) (*Bus, error) {
	opts := []nats.Option{
		nats.Name("quantix"),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1), // reconnect forever
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Warn("NATS disconnected", zap.NamedError("reason", err))
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info("NATS reconnected", zap.String("url", nc.ConnectedUrl()))
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			log.Warn("NATS connection closed")
		}),
	}

	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("NATS connect %s: %w", url, err)
	}
	log.Info("NATS connected", zap.String("url", nc.ConnectedUrl()))
	return &Bus{nc: nc, log: log}, nil
}

// Close drains and closes the NATS connection.
func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Drain() //nolint:errcheck
	}
}

// ─── Publish helpers ──────────────────────────────────────────────────────────

func (b *Bus) PublishKline(msg KlineMsg) error {
	return b.publish(TopicKline(msg.Symbol, msg.Interval), msg)
}

func (b *Bus) PublishFill(msg FillMsg) error {
	return b.publish(TopicFill(msg.StrategyID), msg)
}

func (b *Bus) PublishAlert(msg AlertMsg) error {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	return b.publish(TopicAlert(), msg)
}

func (b *Bus) PublishStatus(msg StatusMsg) error {
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	return b.publish(TopicStatus(msg.StrategyID), msg)
}

// ─── Subscribe helpers ────────────────────────────────────────────────────────

// OnKline subscribes to closed kline events for a specific symbol/interval.
func (b *Bus) OnKline(symbol, interval string, fn func(KlineMsg)) (*nats.Subscription, error) {
	return b.subscribe(TopicKline(symbol, interval), func(data []byte) {
		var msg KlineMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			b.log.Warn("kline decode error", zap.Error(err))
			return
		}
		fn(msg)
	})
}

// OnFill subscribes to fill events for a specific strategy.
func (b *Bus) OnFill(strategyID string, fn func(FillMsg)) (*nats.Subscription, error) {
	return b.subscribe(TopicFill(strategyID), func(data []byte) {
		var msg FillMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			b.log.Warn("fill decode error", zap.Error(err))
			return
		}
		fn(msg)
	})
}

// OnAlert subscribes to all alert events.
func (b *Bus) OnAlert(fn func(AlertMsg)) (*nats.Subscription, error) {
	return b.subscribe(TopicAlert(), func(data []byte) {
		var msg AlertMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			b.log.Warn("alert decode error", zap.Error(err))
			return
		}
		fn(msg)
	})
}

// ─── Internals ────────────────────────────────────────────────────────────────

func (b *Bus) publish(topic string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", topic, err)
	}
	if err := b.nc.Publish(topic, data); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

func (b *Bus) subscribe(topic string, fn func([]byte)) (*nats.Subscription, error) {
	sub, err := b.nc.Subscribe(topic, func(m *nats.Msg) {
		fn(m.Data)
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", topic, err)
	}
	b.log.Debug("NATS subscribed", zap.String("topic", topic))
	return sub, nil
}
