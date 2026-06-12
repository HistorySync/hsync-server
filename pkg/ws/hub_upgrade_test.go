package ws

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestUpgradeRejectsBadOrigin(t *testing.T) {
	store := newFakeDeviceStore("device-token")
	hub := NewHubWithOptions(store, Options{AllowedOrigins: []string{"https://app.example.com"}})
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	t.Cleanup(server.Close)

	_, resp, err := dialWS(t, server.URL, "device-token", "https://evil.example.com")
	if err == nil {
		t.Fatal("Dial() error = nil, want bad origin rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want %d", responseStatus(resp), http.StatusForbidden)
	}
	if store.lookups != 0 {
		t.Fatalf("token lookups = %d, want 0 before origin rejection", store.lookups)
	}
}

func TestUpgradeRejectsPerUserOverLimit(t *testing.T) {
	store := newFakeDeviceStore("device-token")
	hub := NewHubWithOptions(store, Options{
		OriginCheckDisabled:   true,
		MaxConnectionsPerUser: 1,
	})
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	t.Cleanup(server.Close)

	conn := mustDialWS(t, server.URL, "device-token", "")
	t.Cleanup(func() { _ = conn.Close() })
	waitForActiveConnections(t, hub, 1)

	_, resp, err := dialWS(t, server.URL, "device-token", "")
	if err == nil {
		t.Fatal("Dial() error = nil, want per-user capacity rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want %d", responseStatus(resp), http.StatusTooManyRequests)
	}
}

func TestUpgradeRejectsGlobalOverLimit(t *testing.T) {
	store := newFakeDeviceStore("device-token")
	hub := NewHubWithOptions(store, Options{
		OriginCheckDisabled: true,
		MaxConnections:      1,
	})
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	t.Cleanup(server.Close)

	conn := mustDialWS(t, server.URL, "device-token", "")
	t.Cleanup(func() { _ = conn.Close() })
	waitForActiveConnections(t, hub, 1)

	_, resp, err := dialWS(t, server.URL, "device-token", "")
	if err == nil {
		t.Fatal("Dial() error = nil, want global capacity rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %v, want %d", responseStatus(resp), http.StatusTooManyRequests)
	}
}

func TestUpgradeAcceptsValidTokenAndOrigin(t *testing.T) {
	store := newFakeDeviceStore("device-token")
	hub := NewHubWithOptions(store, Options{AllowedOrigins: []string{"https://app.example.com"}})
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	t.Cleanup(server.Close)

	conn := mustDialWS(t, server.URL, "device-token", "https://app.example.com")
	t.Cleanup(func() { _ = conn.Close() })
	waitForActiveConnections(t, hub, 1)

	if store.lastSyncUpdates != 1 {
		t.Fatalf("last sync updates = %d, want 1", store.lastSyncUpdates)
	}
	if hub.ActiveUserCount() != 1 || hub.ActiveConnectionCount() != 1 {
		t.Fatalf("active users/connections = %d/%d, want 1/1", hub.ActiveUserCount(), hub.ActiveConnectionCount())
	}
}

func TestUpgradeRejectsExpiredOrMissingValidatedToken(t *testing.T) {
	store := newFakeDeviceStore("device-token")
	store.device = nil
	hub := NewHubWithOptions(store, Options{OriginCheckDisabled: true})
	go hub.Run()
	server := httptest.NewServer(http.HandlerFunc(hub.ServeHTTP))
	t.Cleanup(server.Close)

	_, resp, err := dialWS(t, server.URL, "device-token", "")
	if err == nil {
		t.Fatal("Dial() error = nil, want invalid token rejection")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %v, want %d", responseStatus(resp), http.StatusUnauthorized)
	}
}

type fakeDeviceStore struct {
	tokenHash       [sha256.Size]byte
	device          *model.Device
	lookups         int
	lastSyncUpdates int
}

func newFakeDeviceStore(token string) *fakeDeviceStore {
	return &fakeDeviceStore{
		tokenHash: sha256.Sum256([]byte(token)),
		device: &model.Device{
			ID:         uuid.New(),
			UserID:     uuid.New(),
			DeviceUUID: uuid.New(),
		},
	}
}

func (s *fakeDeviceStore) GetByTokenHash(_ context.Context, tokenHash []byte) (*model.Device, error) {
	s.lookups++
	if string(tokenHash) != string(s.tokenHash[:]) {
		return nil, nil
	}
	if s.device == nil {
		return nil, nil
	}
	clone := *s.device
	return &clone, nil
}

func (s *fakeDeviceStore) UpdateLastSync(_ context.Context, id uuid.UUID) error {
	if id == s.device.ID {
		s.lastSyncUpdates++
	}
	return nil
}

func mustDialWS(t *testing.T, serverURL string, token string, origin string) *websocket.Conn {
	t.Helper()
	conn, resp, err := dialWS(t, serverURL, token, origin)
	if err != nil {
		t.Fatalf("Dial() error = %v; status=%v", err, responseStatus(resp))
	}
	return conn
}

func dialWS(t *testing.T, serverURL string, token string, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	if origin != "" {
		header.Set("Origin", origin)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(serverURL, "http"), header)
}

func waitForActiveConnections(t *testing.T, hub *Hub, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if hub.ActiveConnectionCount() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("active connections = %d, want %d", hub.ActiveConnectionCount(), want)
}

func responseStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}
