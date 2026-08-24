// One-off, read-only: raw WebSocket connectivity probe against several
// candidate Binance Futures testnet kline stream URLs, bypassing go-binance
// entirely, to determine which (if any) actually deliver data right now.
// No orders, no auth, pure market-data subscription.
package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	candidates := []string{
		"wss://fstream.binancefuture.com/market/ws/btcusdt@kline_1m", // what go-binance v2.8.12 currently uses for demo
		"wss://fstream.binancefuture.com/ws/btcusdt@kline_1m",        // legacy/plain path, same host
		"wss://fstream.binancefuture.com/public/ws/btcusdt@kline_1m", // "public" path variant
		"wss://stream.binancefuture.com/ws/btcusdt@kline_1m",         // alternate host some Binance docs reference
	}

	var wg sync.WaitGroup
	for _, url := range candidates {
		url := url
		wg.Add(1)
		go func() {
			defer wg.Done()
			probe(url)
		}()
	}
	wg.Wait()
}

func probe(url string) {
	start := time.Now()
	c, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		status := "?"
		if resp != nil {
			status = resp.Status
		}
		fmt.Printf("[%s] DIAL FAILED after %.2fs: %v (http status: %s)\n", url, time.Since(start).Seconds(), err, status)
		return
	}
	defer c.Close()
	fmt.Printf("[%s] dialed OK in %.2fs, waiting up to 20s for a message...\n", url, time.Since(start).Seconds())
	c.SetReadDeadline(time.Now().Add(20 * time.Second))
	_, msg, err := c.ReadMessage()
	if err != nil {
		fmt.Printf("[%s] NO MESSAGE within 20s: %v\n", url, err)
		return
	}
	preview := string(msg)
	if len(preview) > 150 {
		preview = preview[:150] + "..."
	}
	fmt.Printf("[%s] GOT MESSAGE (%d bytes): %s\n", url, len(msg), preview)
}
