package api

import (
	"net/http/httptest"
	"testing"
)

func TestCheckOrigin(t *testing.T) {
	h := &WSHub{allowedOrigins: []string{"http://localhost:5173"}}
	cases := []struct {
		name   string
		origin string
		host   string
		want   bool
	}{
		{"same host, nginx stripped port", "http://54.46.102.153:9119", "54.46.102.153", true},
		{"same host+port", "http://54.46.102.153:9119", "54.46.102.153:9119", true},
		{"allowlisted dev origin", "http://localhost:5173", "127.0.0.1", true},
		{"cross-origin rejected", "http://evil.example", "54.46.102.153", false},
		{"non-browser (no origin)", "", "54.46.102.153", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/ws", nil)
			r.Host = c.host
			if c.origin != "" {
				r.Header.Set("Origin", c.origin)
			}
			if got := h.checkOrigin(r); got != c.want {
				t.Errorf("checkOrigin(origin=%q host=%q) = %v, want %v", c.origin, c.host, got, c.want)
			}
		})
	}
}
