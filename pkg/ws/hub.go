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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"github.com/valyala/fasthttp/fasthttpadaptor"

	"github.com/historysync/hsync-server/pkg/model"
	"github.com/historysync/hsync-server/pkg/observability"
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

// Message Types
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

// Client
// Client represents a single WebSocket connection.
type Client struct {
	userID   uuid.UUID
	deviceID uuid.UUID
	hub      *Hub
	conn     *websocket.Conn
	send     chan []byte
	reserved bool
}

// DeviceStore is the repository surface the WebSocket handshake needs.
type DeviceStore interface {
	GetByTokenHash(ctx context.Context, tokenHash []byte) (*model.Device, error)
	UpdateLastSync(ctx context.Context, id uuid.UUID) error
}

// Options controls WebSocket handshake hardening.
type Options struct {
	OriginCheckDisabled   bool
	AllowedOrigins        []string
	MaxConnections        int
	MaxConnectionsPerUser int
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

// Hub
// Hub maintains the set of active clients and broadcasts push notifications.
type Hub struct {
	mu             sync.RWMutex
	clients        map[uuid.UUID]map[*Client]bool // userID -> set of clients
	pending        map[uuid.UUID]int              // reserved upgrade slots by userID
	pendingGlobal  int
	register       chan *Client
	unregister     chan *Client
	devices        DeviceStore
	options        Options
	allowedOrigins map[string]struct{}
}

// NewHub creates a new Hub and starts its run loop.
func NewHub(devices *repository.DeviceRepo) *Hub {
	return NewHubWithOptions(devices, Options{})
}

// NewHubWithOptions creates a new Hub with explicit handshake hardening options.
func NewHubWithOptions(devices DeviceStore, opts Options) *Hub {
	allowed := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		if normalized := normalizeOrigin(origin); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return &Hub{
		clients:        make(map[uuid.UUID]map[*Client]bool),
		pending:        make(map[uuid.UUID]int),
		register:       make(chan *Client),
		unregister:     make(chan *Client),
		devices:        devices,
		options:        opts,
		allowedOrigins: allowed,
	}
}

// Run starts the Hub's event loop. Must be called from a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if client.reserved {
				h.releaseReservedLocked(client.userID)
			}
			if h.clients[client.userID] == nil {
				h.clients[client.userID] = make(map[*Client]bool)
			}
			h.clients[client.userID][client] = true
			active := h.activeConnectionCountLocked()
			h.mu.Unlock()
			observability.SetWebSocketActiveConnections(active)
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
			active := h.activeConnectionCountLocked()
			h.mu.Unlock()
			observability.SetWebSocketActiveConnections(active)
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
	h.registerClient(userID, deviceID, conn, false)
}

func (h *Hub) registerReservedClient(userID, deviceID uuid.UUID, conn *websocket.Conn) {
	h.registerClient(userID, deviceID, conn, true)
}

func (h *Hub) registerClient(userID, deviceID uuid.UUID, conn *websocket.Conn, reserved bool) {
	client := &Client{
		userID:   userID,
		deviceID: deviceID,
		hub:      h,
		conn:     conn,
		send:     make(chan []byte, 64), // buffered channel to avoid blocking pushes
		reserved: reserved,
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
	return h.activeConnectionCountLocked()
}

func (h *Hub) activeConnectionCountLocked() int {
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

func (h *Hub) reserveSlot(userID uuid.UUID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	activeGlobal := h.activeConnectionCountLocked()
	if h.options.MaxConnections > 0 && activeGlobal+h.pendingGlobal >= h.options.MaxConnections {
		return false
	}
	activeUser := h.countUserClients(userID)
	if h.options.MaxConnectionsPerUser > 0 && activeUser+h.pending[userID] >= h.options.MaxConnectionsPerUser {
		return false
	}
	h.pendingGlobal++
	h.pending[userID]++
	return true
}

func (h *Hub) releaseReserved(userID uuid.UUID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.releaseReservedLocked(userID)
}

func (h *Hub) releaseReservedLocked(userID uuid.UUID) {
	if h.pendingGlobal > 0 {
		h.pendingGlobal--
	}
	if h.pending[userID] <= 1 {
		delete(h.pending, userID)
		return
	}
	h.pending[userID]--
}

func (h *Hub) checkOrigin(r *http.Request) bool {
	if h == nil || h.options.OriginCheckDisabled {
		return true
	}
	rawOrigin := strings.TrimSpace(r.Header.Get("Origin"))
	if rawOrigin == "" {
		return true
	}
	origin := normalizeOrigin(rawOrigin)
	if origin == "" {
		return false
	}
	if len(h.allowedOrigins) > 0 {
		_, ok := h.allowedOrigins[origin]
		return ok
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Host, r.Host)
}

func normalizeOrigin(origin string) string {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + strings.ToLower(parsed.Host)
}

// WebSocket Upgrade Handler
func (h *Hub) upgrader() websocket.Upgrader {
	return websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     h.checkOrigin,
	}
}

// UpgradeHandler is a Fiber-compatible handler that upgrades HTTP to WebSocket.
// The request should include a valid device token in Authorization. The legacy
// `token` query parameter is still accepted for older clients.
func (h *Hub) UpgradeHandler(c fiber.Ctx) error {
	// Fiber v3 uses fasthttp; adapt gorilla/websocket via fasthttpadaptor.
	fasthttpadaptor.NewFastHTTPHandlerFunc(h.ServeHTTP)(c.RequestCtx())

	return nil
}

// ServeHTTP upgrades a net/http request to WebSocket after authentication and
// handshake governance checks.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.devices == nil {
		http.Error(w, "websocket device repository not configured", http.StatusInternalServerError)
		return
	}
	if !h.checkOrigin(r) {
		observability.RecordWebSocketUpgradeRejected("origin")
		http.Error(w, "websocket origin is not allowed", http.StatusForbidden)
		return
	}

	token := deviceTokenFromRequest(r)
	if token == "" {
		http.Error(w, "missing device token", http.StatusUnauthorized)
		return
	}

	tokenHash := sha256.Sum256([]byte(token))
	device, err := h.devices.GetByTokenHash(r.Context(), tokenHash[:])
	if err != nil {
		log.Error().Err(err).Msg("ws device token lookup failed")
		http.Error(w, "failed to validate device token", http.StatusInternalServerError)
		return
	}
	if device == nil {
		http.Error(w, "invalid device token", http.StatusUnauthorized)
		return
	}

	if !h.reserveSlot(device.UserID) {
		observability.RecordWebSocketUpgradeRejected("capacity")
		http.Error(w, "websocket connection capacity exceeded", http.StatusTooManyRequests)
		return
	}

	upgrader := h.upgrader()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.releaseReserved(device.UserID)
		log.Error().Err(err).Msg("ws upgrade failed")
		return
	}
	if err := h.devices.UpdateLastSync(r.Context(), device.ID); err != nil {
		log.Warn().Err(err).Str("device_id", device.DeviceUUID.String()).Msg("failed to update device last_sync_at")
	}
	h.registerReservedClient(device.UserID, device.DeviceUUID, conn)
}

func deviceTokenFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return strings.TrimSpace(r.URL.Query().Get("token"))
}

func bearerToken(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	const prefix = "bearer "
	if len(header) >= len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return header
}
