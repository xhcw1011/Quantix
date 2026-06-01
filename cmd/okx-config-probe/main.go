// One-off: query OKX demo account config for qiyue (credential_id=5, user_id=9).
// Specifically: posMode (long_short vs net), account level, position mode.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/Quantix/quantix/internal/api"
	"github.com/Quantix/quantix/internal/config"
	"github.com/Quantix/quantix/internal/data"
	"github.com/Quantix/quantix/internal/logger"
)

const apiBase = "https://www.okx.com"

func main() {
	cfg, _ := config.Load("/opt/quantix/config/config.yaml")
	log, _ := logger.New("production", "info", "")
	defer log.Sync()
	kB, _ := hex.DecodeString(os.Getenv("QUANTIX_ENCRYPTION_KEY"))
	enc, _ := api.NewEncryptor(kB)
	ctx, cn := context.WithTimeout(context.Background(), 20*time.Second)
	defer cn()
	store, _ := data.New(ctx, cfg.Database.DSN(), log)
	defer store.Close()
	cred, _ := store.GetCredentialByID(ctx, 5, 9)
	apiKey, _ := enc.Decrypt(cred.APIKey)
	apiSecret, _ := enc.Decrypt(cred.APISecret)
	passphrase, _ := enc.Decrypt(cred.Passphrase)

	for _, path := range []string{
		"/api/v5/account/config",
		"/api/v5/account/positions",
		"/api/v5/account/balance",
	} {
		fmt.Printf("\n=== GET %s ===\n", path)
		body, err := okxGet(ctx, path, apiKey, apiSecret, passphrase, cred.Demo)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			continue
		}
		fmt.Println(string(body))
	}
}

func okxGet(ctx context.Context, path, key, secret, passphrase string, demo bool) ([]byte, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	prehash := ts + "GET" + path
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, bytes.NewReader(nil))
	if err != nil {
		return nil, err
	}
	req.Header.Set("OK-ACCESS-KEY", key)
	req.Header.Set("OK-ACCESS-SIGN", sig)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", passphrase)
	if demo {
		req.Header.Set("x-simulated-trading", "1")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
