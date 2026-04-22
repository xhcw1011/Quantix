package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateUsername(t *testing.T) {
	assert.NoError(t, validateUsername("alice"))
	assert.NoError(t, validateUsername("user_123"))

	assert.Error(t, validateUsername("ab"))                        // too short (2 chars)
	assert.Error(t, validateUsername(strings.Repeat("a", 33)))     // too long (33 chars)
	assert.Error(t, validateUsername("has space"))                  // contains space
	assert.Error(t, validateUsername("user@name"))                  // contains @
}

func TestValidateEmail(t *testing.T) {
	assert.NoError(t, validateEmail("user@example.com"))

	assert.Error(t, validateEmail("noatsign"))            // no @
	assert.Error(t, validateEmail("@example.com"))        // starts with @
	assert.Error(t, validateEmail("user@nodot"))          // no dot in domain
}

func TestValidatePassword(t *testing.T) {
	assert.NoError(t, validatePassword("abcdefgh"))               // exactly 8 chars

	assert.Error(t, validatePassword("short"))                     // 5 chars, too short
	assert.Error(t, validatePassword(strings.Repeat("a", 129)))    // 129 chars, too long
}

func TestValidateSymbol(t *testing.T) {
	assert.NoError(t, validateSymbol("BTCUSDT"))

	assert.Error(t, validateSymbol(""))                            // empty
	assert.Error(t, validateSymbol("BTC-USDT"))                    // contains hyphen
	assert.Error(t, validateSymbol(strings.Repeat("A", 21)))      // 21 chars, too long
}

func TestValidateInterval(t *testing.T) {
	assert.NoError(t, validateInterval("1h"))
	assert.NoError(t, validateInterval("1d"))

	assert.Error(t, validateInterval("2h"))
	assert.Error(t, validateInterval(""))
}

func TestValidateExchange(t *testing.T) {
	assert.NoError(t, validateExchange("binance"))
	assert.NoError(t, validateExchange("okx"))
	assert.NoError(t, validateExchange("bybit"))

	assert.Error(t, validateExchange("kraken"))
}
