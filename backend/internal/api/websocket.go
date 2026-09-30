package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Client represents a connected WebSocket client
type Client struct {
	conn    *websocket.Conn
	send    chan []byte
	hub     *Hub
	filters map[string]bool // Optional workflow exec ID filters
	mu      sync.RWMutex
	// principal authenticated the upgrade; a nil one fails every recheck.
	principal *Principal
}

// Hub manages all connected WebSocket clients and broadcasts events
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan models.WebSocketEvent
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
	upgrader   websocket.Upgrader

	recheck      func(context.Context, *Principal) (bool, error)
	recheckEvery time.Duration
}

// HubOption keeps NewHub() callable without arguments while the origin policy
// stays optional.
type HubOption func(*Hub)

// WithAllowedOrigins sets the WebSocket handshake allowlist. Origin-less
// (non-browser) clients are always allowed; same-origin only on a trusted host.
func WithAllowedOrigins(allowed []string) HubOption {
	return func(h *Hub) {
		checker := newOriginChecker(allowed)
		h.upgrader.CheckOrigin = checker.allows
	}
}

// WithPrincipalRecheck makes every connection re-validate its principal each
// interval and close with 1008 once check reports it invalid. Auth otherwise
// runs only on the upgrade, so a revoked key would keep its event stream.
func WithPrincipalRecheck(check func(context.Context, *Principal) (bool, error), every time.Duration) HubOption {
	return func(h *Hub) {
		h.recheck = check
		h.recheckEvery = every
	}
}

func NewHub(opts ...HubOption) *Hub {
	h := &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan models.WebSocketEvent, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 4096,
		},
	}
	WithAllowedOrigins(nil)(h)
	for _, o := range opts {
		o(h)
	}
	return h
}

// Run starts the hub event loop
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			total := len(h.clients)
			h.mu.Unlock()
			log.Info().Int("total_clients", total).Msg("WebSocket client connected")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			total := len(h.clients)
			h.mu.Unlock()
			log.Info().Int("total_clients", total).Msg("WebSocket client disconnected")

		case event := <-h.broadcast:
			data, err := json.Marshal(event)
			if err != nil {
				log.Error().Err(err).Msg("failed to marshal WebSocket event")
				continue
			}

			// Write lock: slow-client eviction below mutates h.clients. send is
			// closed only while the client is still in the map, and eviction
			// removes it in the same critical section, so unregister cannot
			// close it a second time.
			h.mu.Lock()
			for client := range h.clients {
				// Apply event filters if set
				if !client.shouldReceive(event) {
					continue
				}
				select {
				case client.send <- data:
				default:
					// Slow client — drop message and disconnect
					log.Warn().Msg("slow WebSocket client, dropping connection")
					close(client.send)
					delete(h.clients, client)
				}
			}
			h.mu.Unlock()
		}
	}
}

// Broadcast implements the orchestrator.EventBroadcaster interface
func (h *Hub) Broadcast(event models.WebSocketEvent) {
	select {
	case h.broadcast <- event:
	default:
		log.Warn().Str("event_type", event.Type).Msg("broadcast channel full, dropping event")
	}
}

// ServeWS handles incoming WebSocket upgrade requests
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		logFrom(r).Warn().Err(err).Msg("WebSocket upgrade failed")
		return
	}

	client := &Client{
		conn:      conn,
		send:      make(chan []byte, 256),
		hub:       h,
		filters:   make(map[string]bool),
		principal: PrincipalFrom(r.Context()),
	}

	// Optional: filter by workflow exec ID via query param
	if execID := r.URL.Query().Get("workflow_exec_id"); execID != "" {
		client.filters[execID] = true
	}

	h.register <- client

	// The request context is cancelled when this handler returns, long before
	// the connection ends; keep its values, drop its cancellation.
	go client.writePump(context.WithoutCancel(r.Context()))
	go client.readPump()
}

// shouldReceive returns true if the client should receive this event
func (c *Client) shouldReceive(event models.WebSocketEvent) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.filters) == 0 {
		return true // No filter = receive all
	}

	// Try to extract workflow exec ID from payload
	if payload, ok := event.Payload.(map[string]any); ok {
		if execID, ok := payload["workflow_exec_id"].(string); ok {
			return c.filters[execID]
		}
	}

	return true // Events without exec ID are always delivered (e.g. metrics)
}

// stillAuthorized fails open on a check error, so a database blip does not
// drop every client; revocation still takes effect on the next good check.
func (c *Client) stillAuthorized(ctx context.Context) bool {
	if c.principal == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ok, err := c.hub.recheck(ctx, c.principal)
	if err != nil {
		log.Warn().Err(err).Str("api_key_id", c.principal.KeyID).Msg("WebSocket credential recheck failed")
		return true
	}
	return ok
}

func (c *Client) writePump(ctx context.Context) {
	ticker := time.NewTicker(54 * time.Second)
	var recheck <-chan time.Time
	if c.hub.recheck != nil {
		t := time.NewTicker(c.hub.recheckEvery)
		defer t.Stop()
		recheck = t.C
	}
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			// One event per frame: the frontend JSON.parses each frame whole.
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}

		case <-recheck:
			if !c.stillAuthorized(ctx) {
				// Best effort: the connection is closed on return either way.
				_ = c.conn.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "credential expired or revoked"),
					time.Now().Add(time.Second))
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		_ = c.conn.Close()
	}()

	c.conn.SetReadLimit(4096)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Error().Err(err).Msg("WebSocket unexpected close")
			}
			break
		}

		// Handle client commands (e.g., subscribe to specific workflow)
		var cmd struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		if err := json.Unmarshal(message, &cmd); err == nil {
			if cmd.Type == "subscribe" && cmd.Payload != "" {
				c.mu.Lock()
				c.filters[cmd.Payload] = true
				c.mu.Unlock()
			} else if cmd.Type == "unsubscribe" {
				c.mu.Lock()
				delete(c.filters, cmd.Payload)
				c.mu.Unlock()
			}
		}
	}
}

// ConnectedClients returns the number of active WebSocket connections
func (h *Hub) ConnectedClients() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
