// Package monitor exposes Prometheus metrics for the Quantix system.
package monitor

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// Metrics holds all application-level Prometheus metrics.
type Metrics struct {
	KlinesReceived  *prometheus.CounterVec
	KlinesStored    *prometheus.CounterVec
	TickersReceived *prometheus.CounterVec
	StoreErrors     *prometheus.CounterVec
	WSReconnects    *prometheus.CounterVec
	DBWriteLatency  *prometheus.HistogramVec
}

// New registers and returns all metrics with the default Prometheus registry.
func New() *Metrics {
	return &Metrics{
		KlinesReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_klines_received_total",
			Help: "Total number of kline events received from WebSocket.",
		}, []string{"symbol", "interval"}),

		KlinesStored: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_klines_stored_total",
			Help: "Total number of closed klines persisted to the database.",
		}, []string{"symbol", "interval"}),

		TickersReceived: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_tickers_received_total",
			Help: "Total number of ticker events received from WebSocket.",
		}, []string{"symbol"}),

		StoreErrors: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_store_errors_total",
			Help: "Total number of database write errors.",
		}, []string{"operation"}),

		WSReconnects: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "quantix_ws_reconnects_total",
			Help: "Total number of WebSocket reconnection attempts.",
		}, []string{"stream"}),

		DBWriteLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "quantix_db_write_duration_seconds",
			Help:    "Latency of database write operations.",
			Buckets: prometheus.DefBuckets,
		}, []string{"operation"}),
	}
}

// ServeHTTP starts the Prometheus metrics HTTP server on the given address.
// Blocks until the server returns an error; call in a goroutine.
func ServeHTTP(addr string, log *zap.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok")) //nolint:errcheck
	})

	log.Info("metrics server listening", zap.String("addr", addr))
	if err := http.ListenAndServe(addr, mux); err != nil && err != http.ErrServerClosed {
		log.Error("metrics server error", zap.Error(err))
	}
}
