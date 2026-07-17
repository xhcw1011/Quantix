package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// WSHub manages all active WebSocket connections.
type WSHub struct {
	mu             sync.RWMutex
	clients        map[int][]*wsClient // userID → connections
	allowedOrigins []string
	log            *zap.Logger
}

type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	userID int
	closed atomic.Bool // set when send channel is closed
}

// NewWSHub creates a new hub. Allowed origins are read from QUANTIX_CORS_ORIGINS env var
// (comma-separated). Empty env var means all origins are blocked; use "*" to allow all.
func NewWSHub(log *zap.Logger) *WSHub {
	var allowedOrigins []string
	if env := os.Getenv("QUANTIX_CORS_ORIGINS"); env != "" {
		for _, o := range strings.Split(env, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowedOrigins = append(allowedOrigins, o)
			}
		}
	}
	// Default: allow localhost dev origin
	if len(allowedOrigins) == 0 {
		allowedOrigins = []string{"http://localhost:5173", "http://localhost:3000"}
	}
	return &WSHub{
		clients:        make(map[int][]*wsClient),
		allowedOrigins: allowedOrigins,
		log:            log,
	}
}

// checkOrigin returns true if the request origin is allowed. Same-origin requests
// are always allowed (the page was served from the same host it's connecting to),
// which makes the WS work on any deployment host without configuring
// QUANTIX_CORS_ORIGINS. We compare the hostname only — nginx forwards Host as the
// bare hostname (no port), while the browser Origin includes the UI port.
func (h *WSHub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client (curl, internal tools)
	}
	if u, err := url.Parse(origin); err == nil && u.Hostname() != "" {
		if u.Hostname() == hostOnly(r.Host) {
			return true // same host → same-origin, safe
		}
	}
	for _, o := range h.allowedOrigins {
		if o == "*" || o == origin {
			return true
		}
	}
	return false
}

// hostOnly strips an optional :port from a Host header value.
func hostOnly(host string) string {
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		return host[:i]
	}
	return host
}

// Broadcast sends a message to all connections for the given userID.
func (h *WSHub) Broadcast(userID int, msg any) {
	data, err := json.Marshal(msg)
	if err != nil {
		h.log.Error("ws broadcast: json marshal failed", zap.Error(err))
		return
	}

	h.mu.RLock()
	orig := h.clients[userID]
	conns := make([]*wsClient, len(orig))
	copy(conns, orig)
	h.mu.RUnlock()

	for _, c := range conns {
		// Skip clients whose send channel has been closed.
		if c.closed.Load() {
			continue
		}
		select {
		case c.send <- data:
		default:
			h.log.Warn("ws broadcast: dropped message for slow client",
				zap.Int("user_id", c.userID))
		}
	}
}

func (h *WSHub) add(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c.userID] = append(h.clients[c.userID], c)
}

func (h *WSHub) remove(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	conns := h.clients[c.userID]
	out := conns[:0]
	for _, x := range conns {
		if x != c {
			out = append(out, x)
		}
	}
	h.clients[c.userID] = out
}

// handleWS upgrades the connection and registers it with the hub.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromCtx(r)

	upgrader := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     s.wsHub.checkOrigin,
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Warn("websocket upgrade failed", zap.Error(err))
		return
	}

	c := &wsClient{
		conn:   conn,
		send:   make(chan []byte, 256),
		userID: userID,
	}
	s.wsHub.add(c)
	defer func() {
		// Mark closed BEFORE removing from hub to prevent Broadcast from sending.
		c.closed.Store(true)
		s.wsHub.remove(c)
		close(c.send) // signal writer goroutine to exit
		if err := conn.Close(); err != nil {
			s.log.Debug("ws conn close error", zap.Error(err))
		}
	}()

	// Writer goroutine
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case msg, ok := <-c.send:
				if !ok {
					return
				}
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					return
				}
			case <-ticker.C:
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			}
		}
	}()

	// Reader loop (keeps connection alive, discards incoming messages)
	conn.SetReadLimit(512)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second)) //nolint:errcheck
		return nil
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
}
