// Package ws manages WebSocket connections for real-time push notifications.
//
// Architecture:
//   - A single Hub goroutine owns all client registration/deregistration.
//   - Each Client runs two goroutines: readPump and writePump.
//   - PushToUser sends to all online clients of a given user.
package ws

import (
	"context"
	"crypto/sha256"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	"github.com/historysync/hsync-server/pkg/repository"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second

	// Send pings to peer with this period. Must be less than pongWait.
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer.
	maxMessageSize = 4096
)

// ── Message Types ────────────────────────────────────────────

// PushMessage is a JSON-serializable notification sent to connected clients.
type PushMessage struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"ts"`
}

// Pre-defined message types.
const (
	MsgConnected           = "connected"
	MsgBundleUploaded      = "bundle_uploaded"
	MsgSnapshotUploaded    = "snapshot_uploaded"
	MsgDeviceRevoked       = "device_revoked"
	MsgQuotaWarning        = "quota_warning"
	MsgSubscriptionChanged = "subscription_changed"
)

// ── Client ───────────────────────────────────────────────────

// Client represents a single WebSocket connection.
type Client struct {
	userID   uuid.UUID
	deviceID uuid.UUID
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
}

// readPump pumps messages from the WebSocket connection to the hub.
// The application only expects pong messages from clients.
func (c *Client) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Debug().Err(err).Str("user_id", c.userID.String()).Msg("ws read error")
			}
			break
		}
	}
}

// writePump pumps messages from the hub to the WebSocket connection.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Debug().Err(err).Str("user_id", c.userID.String()).Msg("ws write error")
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// ── Hub ──────────────────────────────────────────────────────

// Hub maintains the set of active clients and broadcasts push notifications.
type Hub struct {
	mu         sync.RWMutex
	clients    map[uuid.UUID]map[*Client]bool // userID -> set of clients
	register   chan *Client
	unregister chan *Client
	devices    *repository.DeviceRepo
}

// NewHub creates a new Hub and starts its run loop.
func NewHub(devices *repository.DeviceRepo) *Hub {
	return &Hub{
		clients:    make(map[uuid.UUID]map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
		devices:    devices,
	}
}

// Run starts the Hub's event loop. Must be called from a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if h.clients[client.userID] == nil {
				h.clients[client.userID] = make(map[*Client]bool)
			}
			h.clients[client.userID][client] = true
			h.mu.Unlock()
			log.Debug().
				Str("user_id", client.userID.String()).
				Str("device_id", client.deviceID.String()).
				Int("total_connections", h.countUserClients(client.userID)).
				Msg("ws client connected")

		case client := <-h.unregister:
			h.mu.Lock()
			if clients, ok := h.clients[client.userID]; ok {
				if _, ok := clients[client]; ok {
					delete(clients, client)
					close(client.send)
					if len(clients) == 0 {
						delete(h.clients, client.userID)
					}
				}
			}
			h.mu.Unlock()
			log.Debug().
				Str("user_id", client.userID.String()).
				Msg("ws client disconnected")
		}
	}
}

// PushToUser sends a message to all connected clients of a specific user.
func (h *Hub) PushToUser(userID uuid.UUID, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	clients := h.clients[userID]
	for client := range clients {
		select {
		case client.send <- msg:
		default:
			// Client is too slow; close connection
			go func(c *Client) {
				h.unregister <- c
			}(client)
		}
	}
}

// BroadcastToAll sends a message to every connected client.
func (h *Hub) BroadcastToAll(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	for _, clients := range h.clients {
		for client := range clients {
			select {
			case client.send <- msg:
			default:
				go func(c *Client) {
					h.unregister <- c
				}(client)
			}
		}
	}
}

// RegisterClient wires up a new WebSocket connection into the hub.
func (h *Hub) RegisterClient(userID, deviceID uuid.UUID, conn *websocket.Conn) {
	client := &Client{
		userID:   userID,
		deviceID: deviceID,
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 64), // buffered channel to avoid blocking pushes
	}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

// ActiveUserCount returns the number of users with at least one active connection.
func (h *Hub) ActiveUserCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// ActiveConnectionCount returns the total number of active WebSocket connections.
func (h *Hub) ActiveConnectionCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	count := 0
	for _, clients := range h.clients {
		count += len(clients)
	}
	return count
}

// countUserClients is a helper (caller must hold at least RLock).
func (h *Hub) countUserClients(userID uuid.UUID) int {
	if clients, ok := h.clients[userID]; ok {
		return len(clients)
	}
	return 0
}

// ── WebSocket Upgrade Handler ───────────────────────────────

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// UpgradeHandler is a Fiber-compatible handler that upgrades HTTP to WebSocket.
// The request should include a `token` query parameter with a valid device token.
func (h *Hub) UpgradeHandler(c fiber.Ctx) error {
	// Fiber v3 uses fasthttp; adapt gorilla/websocket via fasthttpadaptor.
	fasthttpadaptor.NewFastHTTPHandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if h.devices == nil {
				http.Error(w, "websocket device repository not configured", http.StatusInternalServerError)
				return
			}

			token := strings.TrimSpace(r.URL.Query().Get("token"))
			if token == "" {
				http.Error(w, "missing device token", http.StatusUnauthorized)
				return
			}

			tokenHash := sha256.Sum256([]byte(token))
			device, err := h.devices.GetByTokenHash(context.Background(), tokenHash[:])
			if err != nil {
				log.Error().Err(err).Msg("ws device token lookup failed")
				http.Error(w, "failed to validate device token", http.StatusInternalServerError)
				return
			}
			if device == nil || device.RevokedAt != nil {
				http.Error(w, "invalid device token", http.StatusUnauthorized)
				return
			}

			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				log.Error().Err(err).Msg("ws upgrade failed")
				return
			}
			if err := h.devices.UpdateLastSync(context.Background(), device.ID); err != nil {
				log.Warn().Err(err).Str("device_id", device.DeviceUUID.String()).Msg("failed to update device last_sync_at")
			}
			h.RegisterClient(device.UserID, device.DeviceUUID, conn)
		},
	)(c.RequestCtx())

	return nil
}
