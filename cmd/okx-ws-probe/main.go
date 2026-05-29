// Standalone OKX WS probe. Connects to public WS, subscribes to candle5m
// on ETH-USDT-SWAP (demo via x-simulated-trading endpoint is the same WS
// URL for public market data), sends "ping" every 20s, dumps every
// received message verbatim so we can see what OKX actually delivers.
//
//   go run ./cmd/okx-ws-probe
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wsURL = "wss://ws.okx.com:8443/ws/v5/business"

func main() {
	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatalf("dial: %v (resp=%v)", err, resp)
	}
	defer conn.Close()
	fmt.Println("connected to", wsURL)

	var writeMu sync.Mutex

	// Subscribe to multiple intervals + multiple instruments for variety.
	sub := map[string]any{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "candle1m", "instId": "ETH-USDT-SWAP"},
			{"channel": "candle5m", "instId": "ETH-USDT-SWAP"},
			{"channel": "candle15m", "instId": "ETH-USDT-SWAP"},
		},
	}
	b, _ := json.Marshal(sub)
	writeMu.Lock()
	if err := conn.WriteMessage(websocket.TextMessage, b); err != nil {
		log.Fatalf("subscribe send: %v", err)
	}
	writeMu.Unlock()
	fmt.Println("subscribe sent")

	// Heartbeat
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			writeMu.Lock()
			err := conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			writeMu.Unlock()
			if err != nil {
				fmt.Println("ping err:", err)
				return
			}
			fmt.Printf("[%s] sent ping\n", time.Now().Format("15:04:05"))
		}
	}()

	closedBars := 0
	tickBars := 0
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("read err:", err)
			return
		}
		stamp := time.Now().Format("15:04:05")
		// Classify so output is readable
		var head struct {
			Event   string `json:"event"`
			Channel string `json:"channel"`
			Arg     struct {
				Channel string `json:"channel"`
				InstID  string `json:"instId"`
			} `json:"arg"`
			Data [][]string `json:"data"`
		}
		_ = json.Unmarshal(data, &head)
		if head.Event != "" {
			fmt.Printf("[%s] ack/event: %s\n", stamp, string(data))
			continue
		}
		if string(data) == "pong" {
			fmt.Printf("[%s] pong\n", stamp)
			continue
		}
		if head.Arg.Channel != "" && len(head.Data) > 0 {
			// kline event
			for _, row := range head.Data {
				closed := len(row) >= 9 && row[8] == "1"
				if closed {
					closedBars++
				} else {
					tickBars++
				}
				fmt.Printf("[%s] %s/%s row=%v IsClosed=%v\n", stamp,
					head.Arg.InstID, head.Arg.Channel, row, closed)
			}
			fmt.Printf("        running totals: tick=%d closed=%d\n", tickBars, closedBars)
			continue
		}
		fmt.Printf("[%s] OTHER (%d bytes): %s\n", stamp, len(data), string(data))
	}
}
