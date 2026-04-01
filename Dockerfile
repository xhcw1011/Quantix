# ─── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Download dependencies first (layer-cached separately from source)
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o quantix-api ./cmd/api

# ─── Runtime stage ────────────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/quantix-api /usr/local/bin/quantix-api
COPY --from=builder /build/migrations ./migrations

EXPOSE 8080

ENTRYPOINT ["quantix-api"]
