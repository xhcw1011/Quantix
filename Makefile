GO ?= /usr/local/go/bin/go

.PHONY: smoke-binance smoke-binance-roundtrip smoke-live smoke-live-roundtrip smoke-futures smoke-futures-roundtrip soak

smoke-binance:
	$(GO) run ./cmd/binance-smoke -symbol BTCUSDT -qty 0.00010

smoke-binance-roundtrip:
	$(GO) run ./cmd/binance-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip

smoke-live:
	$(GO) run ./cmd/live-smoke -symbol BTCUSDT -qty 0.00010

smoke-live-roundtrip:
	$(GO) run ./cmd/live-smoke -symbol BTCUSDT -qty 0.00010 -roundtrip

smoke-futures:
	$(GO) run ./cmd/futures-smoke -config config/config.futures.yaml -symbol BTCUSDT -qty 0.002 -leverage 5

smoke-futures-roundtrip:
	$(GO) run ./cmd/futures-smoke -config config/config.futures.yaml -symbol BTCUSDT -qty 0.002 -leverage 5 -roundtrip -json

soak:
	$(GO) run ./cmd/soak -config config/config.example.yaml -strategy macross -symbol BTCUSDT -interval 1m -duration 4h
