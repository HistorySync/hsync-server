package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

// fakeReservationHook records reservation calls so lifecycle tests can assert
// exactly which hook methods fired, without a database or external service.
type fakeReservationHook struct {
	reserveID  string
	reserveErr error
	settleErr  error

	reserveCalls int
	settleCalls  int
	releaseCalls int

	lastReserveUserID uuid.UUID
	lastReserveBytes  int64
	lastReserveReq    ReservationRequest
	lastSettleID      string
	lastSettleBytes   int64
	lastReleaseID     string
}

func (f *fakeReservationHook) ReserveStorage(_ context.Context, userID uuid.UUID, bytes int64, req ReservationRequest) (string, error) {
	f.reserveCalls++
	f.lastReserveUserID = userID
	f.lastReserveBytes = bytes
	f.lastReserveReq = req
	if f.reserveErr != nil {
		return "", f.reserveErr
	}
	return f.reserveID, nil
}

func (f *fakeReservationHook) SettleStorage(_ context.Context, reservationID string, bytes int64) error {
	f.settleCalls++
	f.lastSettleID = reservationID
	f.lastSettleBytes = bytes
	return f.settleErr
}

func (f *fakeReservationHook) ReleaseStorage(_ context.Context, reservationID string) {
	f.releaseCalls++
	f.lastReleaseID = reservationID
}

func TestReserveNilHookInactive(t *testing.T) {
	guard, err := reserve(context.Background(), nil, uuid.New(), 100, ReservationRequest{Reason: "bundle_upload"})
	if err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	if guard.active() {
		t.Fatal("guard.active() = true for nil hook, want false")
	}
	// With no hook the caller falls back to legacy quota; settle and release
	// must be safe no-ops.
	if err := guard.settle(context.Background(), 100); err != nil {
		t.Fatalf("settle() error = %v, want nil", err)
	}
	guard.release(context.Background())
}

func TestReserveErrorPropagates(t *testing.T) {
	wantErr := errors.New("over quota")
	hook := &fakeReservationHook{reserveErr: wantErr}
	guard, err := reserve(context.Background(), hook, uuid.New(), 100, ReservationRequest{Reason: "bundle_upload"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("reserve() error = %v, want %v", err, wantErr)
	}
	if guard != nil {
		t.Fatal("reserve() guard = non-nil on error, want nil")
	}
	if hook.reserveCalls != 1 {
		t.Fatalf("reserveCalls = %d, want 1", hook.reserveCalls)
	}
}

func TestReserveForwardsRequestMetadata(t *testing.T) {
	hook := &fakeReservationHook{reserveID: "res-meta"}
	req := ReservationRequest{
		Reason:     "bundle_upload",
		BundleID:   "bundle-123",
		DeviceUUID: "device-abc",
		RequestID:  "req-xyz",
	}
	userID := uuid.New()
	if _, err := reserve(context.Background(), hook, userID, 256, req); err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	if hook.lastReserveUserID != userID {
		t.Fatalf("reserve user id = %v, want %v", hook.lastReserveUserID, userID)
	}
	if hook.lastReserveBytes != 256 {
		t.Fatalf("reserve bytes = %d, want 256", hook.lastReserveBytes)
	}
	if hook.lastReserveReq != req {
		t.Fatalf("reserve request = %+v, want %+v", hook.lastReserveReq, req)
	}
}

func TestReserveEmptyIDInactive(t *testing.T) {
	hook := &fakeReservationHook{reserveID: ""}
	guard, err := reserve(context.Background(), hook, uuid.New(), 100, ReservationRequest{Reason: "bundle_upload"})
	if err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	// A hook that returns an empty id means no reservation was taken, so the
	// guard stays inactive and downstream hook calls must not fire.
	if guard.active() {
		t.Fatal("guard.active() = true for empty reservation id, want false")
	}
	if err := guard.settle(context.Background(), 100); err != nil {
		t.Fatalf("settle() error = %v, want nil", err)
	}
	guard.release(context.Background())
	if hook.settleCalls != 0 || hook.releaseCalls != 0 {
		t.Fatalf("settleCalls=%d releaseCalls=%d, want 0 0", hook.settleCalls, hook.releaseCalls)
	}
}

func TestGuardSettleThenReleaseNoOp(t *testing.T) {
	hook := &fakeReservationHook{reserveID: "res-1"}
	guard, err := reserve(context.Background(), hook, uuid.New(), 100, ReservationRequest{Reason: "bundle_upload"})
	if err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	if !guard.active() {
		t.Fatal("guard.active() = false, want true")
	}
	if err := guard.settle(context.Background(), 80); err != nil {
		t.Fatalf("settle() error = %v, want nil", err)
	}
	// A deferred release after a successful settle must not reclaim capacity,
	// otherwise settled bytes would be double-counted.
	guard.release(context.Background())
	if hook.settleCalls != 1 {
		t.Fatalf("settleCalls = %d, want 1", hook.settleCalls)
	}
	if hook.lastSettleID != "res-1" || hook.lastSettleBytes != 80 {
		t.Fatalf("settle args = (%q, %d), want (res-1, 80)", hook.lastSettleID, hook.lastSettleBytes)
	}
	if hook.releaseCalls != 0 {
		t.Fatalf("releaseCalls = %d after settle, want 0", hook.releaseCalls)
	}
}

func TestGuardReleaseWithoutSettle(t *testing.T) {
	hook := &fakeReservationHook{reserveID: "res-2"}
	guard, err := reserve(context.Background(), hook, uuid.New(), 100, ReservationRequest{Reason: "snapshot_upload"})
	if err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	// Models a blob or metadata write failure: the upload returns before
	// settling, so the deferred release must reclaim the reservation.
	guard.release(context.Background())
	if hook.releaseCalls != 1 {
		t.Fatalf("releaseCalls = %d, want 1", hook.releaseCalls)
	}
	if hook.lastReleaseID != "res-2" {
		t.Fatalf("release id = %q, want res-2", hook.lastReleaseID)
	}
	if hook.settleCalls != 0 {
		t.Fatalf("settleCalls = %d, want 0", hook.settleCalls)
	}
}

func TestGuardSettleErrorAllowsRelease(t *testing.T) {
	settleErr := errors.New("settle failed")
	hook := &fakeReservationHook{reserveID: "res-3", settleErr: settleErr}
	guard, err := reserve(context.Background(), hook, uuid.New(), 100, ReservationRequest{Reason: "bundle_upload"})
	if err != nil {
		t.Fatalf("reserve() error = %v, want nil", err)
	}
	if err := guard.settle(context.Background(), 100); !errors.Is(err, settleErr) {
		t.Fatalf("settle() error = %v, want %v", err, settleErr)
	}
	// Settle failed, so the guard is not settled and the deferred release must
	// reclaim the reservation as cleanup.
	guard.release(context.Background())
	if hook.releaseCalls != 1 {
		t.Fatalf("releaseCalls = %d after settle failure, want 1", hook.releaseCalls)
	}
}
