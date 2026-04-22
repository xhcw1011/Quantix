package api

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	minUsernameLen = 3
	maxUsernameLen = 32
	maxEmailLen    = 254
	minPasswordLen = 8
	maxPasswordLen = 128
	maxLabelLen    = 64
	maxAPIKeyLen   = 256
	maxSymbolLen   = 20
)

var validIntervals = map[string]bool{
	"1m": true, "5m": true, "15m": true,
	"1h": true, "4h": true, "1d": true, "1w": true,
}

var validExchanges = map[string]bool{
	"binance": true, "okx": true, "bybit": true,
}

func validateUsername(s string) error {
	if len(s) < minUsernameLen {
		return fmt.Errorf("username must be at least %d characters", minUsernameLen)
	}
	if len(s) > maxUsernameLen {
		return fmt.Errorf("username must be at most %d characters", maxUsernameLen)
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return fmt.Errorf("username may only contain letters, digits and underscores")
		}
	}
	return nil
}

func validateEmail(s string) error {
	if len(s) > maxEmailLen {
		return fmt.Errorf("email too long")
	}
	if !strings.Contains(s, "@") || strings.HasPrefix(s, "@") || strings.HasSuffix(s, "@") {
		return fmt.Errorf("invalid email address")
	}
	parts := strings.SplitN(s, "@", 2)
	if len(parts) != 2 || parts[0] == "" || !strings.Contains(parts[1], ".") {
		return fmt.Errorf("invalid email address")
	}
	return nil
}

func validatePassword(s string) error {
	if len(s) < minPasswordLen {
		return fmt.Errorf("password must be at least %d characters", minPasswordLen)
	}
	if len(s) > maxPasswordLen {
		return fmt.Errorf("password must be at most %d characters", maxPasswordLen)
	}
	return nil
}

func validateAPIKey(s string) error {
	if s == "" {
		return fmt.Errorf("api_key is required")
	}
	if len(s) > maxAPIKeyLen {
		return fmt.Errorf("api_key too long (max %d chars)", maxAPIKeyLen)
	}
	return nil
}

func validateLabel(s string) error {
	if s == "" {
		return fmt.Errorf("label is required")
	}
	if len(s) > maxLabelLen {
		return fmt.Errorf("label too long (max %d chars)", maxLabelLen)
	}
	return nil
}

func validateSymbol(s string) error {
	if s == "" {
		return fmt.Errorf("symbol is required")
	}
	if len(s) > maxSymbolLen {
		return fmt.Errorf("symbol too long (max %d chars)", maxSymbolLen)
	}
	for _, c := range s {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) {
			return fmt.Errorf("symbol must be alphanumeric")
		}
	}
	return nil
}

func validateInterval(s string) error {
	if !validIntervals[s] {
		return fmt.Errorf("interval must be one of: 1m, 5m, 15m, 1h, 4h, 1d, 1w")
	}
	return nil
}

func validateExchange(s string) error {
	if !validExchanges[s] {
		return fmt.Errorf("exchange must be one of: binance, okx, bybit")
	}
	return nil
}
