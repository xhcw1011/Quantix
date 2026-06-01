// Local OKX order probe. Reads qiyue's credential from the SERVER's DB over an
// SSH tunnel (run from your Mac with the engine off, or use a copy of secrets).
//
// What it does:
//   1. fetches /api/v5/account/config → prints posMode
//   2. submits a tiny test LIMIT order (1 contract, way below market — will hang
//      pending until cancelled) with three posSide variants:
//        A. posSide=long   (what broker has been sending)
//        B. posSide="" omitted
//        C. posSide=net
//   3. cancels any of those that succeeded
//
// Usage on this Mac (the server's encryption key + cred row are needed):
//   sudo cat /etc/quantix/env   on server → copy QUANTIX_ENCRYPTION_KEY locally
//   export QUANTIX_ENCRYPTION_KEY=<that hex>
//   go run ./cmd/okx-order-probe \
//        -api <APIKEY> -secret <APISECRET> -pass <PASSPHRASE> -demo
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

const apiBase = "https://www.okx.com"

func main() {
	apiKey := flag.String("api", "", "OKX API key")
	apiSecret := flag.String("secret", "", "OKX API secret")
	passphrase := flag.String("pass", "", "OKX passphrase")
	demo := flag.Bool("demo", true, "use x-simulated-trading header")
	flag.Parse()
	if *apiKey == "" || *apiSecret == "" || *passphrase == "" {
		fmt.Println("usage: -api <KEY> -secret <SECRET> -pass <PASSPHRASE> [-demo=true]")
		os.Exit(1)
	}
	ctx := context.Background()
	c := &client{apiKey: *apiKey, apiSecret: *apiSecret, passphrase: *passphrase, demo: *demo}

	fmt.Println("=== 1. /api/v5/account/config ===")
	var cfg struct {
		Code string `json:"code"`
		Data []struct {
			AcctLv  string `json:"acctLv"`
			PosMode string `json:"posMode"`
		} `json:"data"`
	}
	if err := c.get(ctx, "/api/v5/account/config", &cfg); err != nil {
		fmt.Println("ERROR:", err)
		os.Exit(1)
	}
	if len(cfg.Data) == 0 {
		fmt.Println("no data returned")
		os.Exit(1)
	}
	fmt.Printf("  acctLv=%s  posMode=%s\n", cfg.Data[0].AcctLv, cfg.Data[0].PosMode)

	// Tiny test order: 1 contract LIMIT BUY at $500 (very far below market) so it
	// stays NEW and we can cancel cleanly.
	type ord struct {
		InstID  string `json:"instId"`
		TdMode  string `json:"tdMode"`
		Side    string `json:"side"`
		PosSide string `json:"posSide,omitempty"`
		OrdType string `json:"ordType"`
		Sz      string `json:"sz"`
		Px      string `json:"px"`
	}

	cases := []struct {
		name    string
		posSide string
	}{
		{"A. posSide=long  (current broker behavior)", "long"},
		{"B. posSide omitted", ""},
		{"C. posSide=net", "net"},
	}

	var orderIDs []string
	for _, tc := range cases {
		req := ord{
			InstID: "ETH-USDT-SWAP", TdMode: "cross", Side: "buy",
			PosSide: tc.posSide, OrdType: "limit", Sz: "1", Px: "500",
		}
		fmt.Printf("\n=== 2. POST /trade/order : %s ===\n", tc.name)
		var resp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
			Data []struct {
				OrdID string `json:"ordId"`
				SCode string `json:"sCode"`
				SMsg  string `json:"sMsg"`
			} `json:"data"`
		}
		if err := c.post(ctx, "/api/v5/trade/order", req, &resp); err != nil {
			fmt.Println("ERROR:", err)
			continue
		}
		if resp.Code == "0" && len(resp.Data) > 0 && resp.Data[0].SCode == "0" {
			fmt.Printf("  ✓ ORDER PLACED  ordId=%s\n", resp.Data[0].OrdID)
			orderIDs = append(orderIDs, resp.Data[0].OrdID)
		} else {
			sCode, sMsg := "", ""
			if len(resp.Data) > 0 {
				sCode = resp.Data[0].SCode
				sMsg = resp.Data[0].SMsg
			}
			fmt.Printf("  ✗ REJECTED  code=%s sCode=%s sMsg=%s\n", resp.Code, sCode, sMsg)
		}
	}

	// Cancel anything that got placed so we leave no garbage.
	for _, id := range orderIDs {
		var cancelResp map[string]any
		_ = c.post(ctx, "/api/v5/trade/cancel-order",
			map[string]string{"instId": "ETH-USDT-SWAP", "ordId": id}, &cancelResp)
		fmt.Printf("\ncancelled ordId=%s\n", id)
	}
}

type client struct {
	apiKey, apiSecret, passphrase string
	demo                          bool
}

func (c *client) get(ctx context.Context, path string, out any) error {
	return c.req(ctx, "GET", path, nil, out)
}
func (c *client) post(ctx context.Context, path string, body, out any) error {
	return c.req(ctx, "POST", path, body, out)
}
func (c *client) req(ctx context.Context, method, path string, body, out any) error {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + method + path + string(bodyBytes)
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(prehash))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	var rdr io.Reader
	if bodyBytes != nil {
		rdr = bytes.NewReader(bodyBytes)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OK-ACCESS-KEY", c.apiKey)
	req.Header.Set("OK-ACCESS-SIGN", sig)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", c.passphrase)
	if c.demo {
		req.Header.Set("x-simulated-trading", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return json.Unmarshal(b, out)
}
