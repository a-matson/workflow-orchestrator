package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Configure proper CORS in production
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}

// Client represents a connected WebSocket client
type Client struct {
	conn    *websocket.Conn
	send    chan []byte
	hub     *Hub
	filters map[string]bool // Optional workflow exec ID filters
	mu      sync.RWMutex
}

// Hub manages all connected WebSocket clients and broadcasts events
type Hub struct {
	clients    map[*Client]bool
	broadcast  chan models.WebSocketEvent
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]bool),
		broadcast:  make(chan models.WebSocketEvent, 256),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Run starts the hub event loop
func (h *Hub) Run() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			log.Info().Int("total_clients", len(h.clients)).Msg("WebSocket client connected")

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()
			log.Info().Int("total_clients", len(h.clients)).Msg("WebSocket client disconnected")

		case event := <-h.broadcast:
			data, err := json.Marshal(event)
			if err != nil {
				log.Error().Err(err).Msg("failed to marshal WebSocket event")
				continue
			}

			h.mu.RLock()
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
			h.mu.RUnlock()

		case <-ticker.C:
			// Ping all clients to detect dead connections
			h.mu.RLock()
			for client := range h.clients {
				_ = client.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					_ = client.conn.Close()
				}
			}
			h.mu.RUnlock()
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
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("WebSocket upgrade failed")
		return
	}

	client := &Client{
		conn:    conn,
		send:    make(chan []byte, 256),
		hub:     h,
		filters: make(map[string]bool),
	}

	// Optional: filter by workflow exec ID via query param
	if execID := r.URL.Query().Get("workflow_exec_id"); execID != "" {
		client.filters[execID] = true
	}

	h.register <- client

	go client.writePump()
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

func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
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

			w, err := c.conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			_, err = w.Write(message)
			if err != nil {
				return
			}

			// Flush any buffered messages
			n := len(c.send)
			for i := 0; i < n; i++ {
				_, _ = w.Write([]byte{'\n'})
				_, _ = w.Write(<-c.send)
			}

			if err := w.Close(); err != nil {
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
