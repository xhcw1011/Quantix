// One-off, read-only diagnostic: subscribe to BTCUSDT futures kline WS
// streams on the DEMO/TESTNET endpoint across several intervals
// simultaneously, in complete isolation, to determine whether the
// 2026-08-17 "zero kline data" issue is specific to the 15m interval or
// affects testnet's BTCUSDT kline feed broadly. No orders placed.
package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	futures "github.com/adshao/go-binance/v2/futures"
)

func main() {
	futures.UseTestnet = false
	futures.UseDemo = true

	intervals := []string{"1m", "5m", "15m"}
	var wg sync.WaitGroup
	var mu sync.Mutex
	counts := map[string]int{}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	start := time.Now()

	for _, itv := range intervals {
		itv := itv
		wg.Add(1)
		go func() {
			defer wg.Done()
			wsHandler := func(event *futures.WsKlineEvent) {
				if event == nil {
					return
				}
				mu.Lock()
				counts[itv]++
				n := counts[itv]
				mu.Unlock()
				if n <= 2 {
					fmt.Printf("[+%.1fs] [%s] msg #%d final=%v close=%s\n", time.Since(start).Seconds(), itv, n, event.Kline.IsFinal, event.Kline.Close)
				}
			}
			errHandler := func(err error) {
				fmt.Printf("[+%.1fs] [%s] ERROR: %v\n", time.Since(start).Seconds(), itv, err)
			}
			doneC, stopC, err := futures.WsKlineServe("BTCUSDT", itv, wsHandler, errHandler)
			if err != nil {
				fmt.Printf("[%s] failed to open stream: %v\n", itv, err)
				return
			}
			fmt.Printf("[+%.1fs] [%s] connected\n", time.Since(start).Seconds(), itv)
			go func() {
				<-ctx.Done()
				close(stopC)
			}()
			<-doneC
		}()
	}

	wg.Wait()
	fmt.Println("=== final counts ===")
	mu.Lock()
	for _, itv := range intervals {
		fmt.Printf("%s: %d messages\n", itv, counts[itv])
	}
	mu.Unlock()
}
