//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestDeviceCreateAndGet(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "dev@example.com")
	d := seedDevice(t, repos, u.ID)
	if d.ID == uuid.Nil || d.CreatedAt.IsZero() {
		t.Fatal("Create did not populate ID/CreatedAt")
	}

	byUUID, err := repos.Devices.GetByUserAndUUID(ctx, u.ID, d.DeviceUUID)
	if err != nil {
		t.Fatalf("GetByUserAndUUID: %v", err)
	}
	if byUUID == nil || byUUID.ID != d.ID {
		t.Fatalf("GetByUserAndUUID = %+v, want ID %s", byUUID, d.ID)
	}

	byID, err := repos.Devices.GetByID(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID == nil || byID.DeviceUUID != d.DeviceUUID {
		t.Fatalf("GetByID = %+v", byID)
	}

	missing, err := repos.Devices.GetByUserAndUUID(ctx, u.ID, uuid.New())
	if err != nil {
		t.Fatalf("GetByUserAndUUID(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetByUserAndUUID(missing) = %+v, want nil", missing)
	}
}

func TestDeviceUniquePerUser(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "uniq@example.com")
	deviceUUID := uuid.New()

	first := &model.Device{UserID: u.ID, DeviceUUID: deviceUUID, Platform: "linux"}
	if err := repos.Devices.Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	dup := &model.Device{UserID: u.ID, DeviceUUID: deviceUUID, Platform: "linux"}
	if err := repos.Devices.Create(ctx, dup); err == nil {
		t.Fatal("duplicate (user_id, device_uuid) Create succeeded, want unique-violation error")
	}
}

func TestDeviceRevokeAndCountActive(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "revoke@example.com")
	d1 := seedDevice(t, repos, u.ID)
	seedDevice(t, repos, u.ID)

	count, err := repos.Devices.CountActiveByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountActiveByUser: %v", err)
	}
	if count != 2 {
		t.Fatalf("active count = %d, want 2", count)
	}

	if err := repos.Devices.Revoke(ctx, u.ID, d1.DeviceUUID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	count, err = repos.Devices.CountActiveByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("CountActiveByUser after revoke: %v", err)
	}
	if count != 1 {
		t.Fatalf("active count after revoke = %d, want 1", count)
	}

	revoked, err := repos.Devices.GetByUserAndUUID(ctx, u.ID, d1.DeviceUUID)
	if err != nil {
		t.Fatalf("GetByUserAndUUID: %v", err)
	}
	if revoked == nil || revoked.RevokedAt == nil {
		t.Fatalf("expected RevokedAt to be set, got %+v", revoked)
	}
}

func TestDeviceTokenHashLookup(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "token@example.com")
	d := seedDevice(t, repos, u.ID)

	hash := []byte("device-token-hash")
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	if err := repos.Devices.UpdateToken(ctx, d.ID, hash, expiresAt); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}

	got, err := repos.Devices.GetByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if got == nil || got.ID != d.ID {
		t.Fatalf("GetByTokenHash = %+v, want ID %s", got, d.ID)
	}

	missing, err := repos.Devices.GetByTokenHash(ctx, []byte("nope"))
	if err != nil {
		t.Fatalf("GetByTokenHash(missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("GetByTokenHash(missing) = %+v, want nil", missing)
	}
}

func TestDeviceTokenHashLookupRejectsExpiredToken(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "expired-token@example.com")
	d := seedDevice(t, repos, u.ID)

	hash := []byte("expired-device-token-hash")
	if err := repos.Devices.UpdateToken(ctx, d.ID, hash, time.Now().UTC().Add(-time.Minute)); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}

	got, err := repos.Devices.GetByTokenHash(ctx, hash)
	if err != nil {
		t.Fatalf("GetByTokenHash(expired): %v", err)
	}
	if got != nil {
		t.Fatalf("GetByTokenHash(expired) = %+v, want nil", got)
	}
}

func TestDeviceTokenExpiryPersistsOnCreateAndRead(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "persist-token@example.com")
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Round(time.Microsecond)
	d := &model.Device{
		UserID:         u.ID,
		DeviceUUID:     uuid.New(),
		DeviceName:     "persist-device",
		Platform:       "windows",
		AppVersion:     "1.0.0",
		TokenHash:      []byte("persisted-token-hash"),
		TokenExpiresAt: &expiresAt,
	}
	if err := repos.Devices.Create(ctx, d); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repos.Devices.GetByID(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil || got.TokenExpiresAt == nil {
		t.Fatalf("GetByID = %+v, want token expiry", got)
	}
	if got.TokenExpiresAt.UTC().Unix() != expiresAt.UTC().Unix() {
		t.Fatalf("TokenExpiresAt = %s, want %s", got.TokenExpiresAt.UTC(), expiresAt.UTC())
	}
}

func TestDeviceListByUserScoped(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u1 := seedUser(t, repos, "owner1@example.com")
	u2 := seedUser(t, repos, "owner2@example.com")
	seedDevice(t, repos, u1.ID)
	seedDevice(t, repos, u1.ID)
	seedDevice(t, repos, u2.ID)

	list, err := repos.Devices.ListByUser(ctx, u1.ID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListByUser len = %d, want 2", len(list))
	}
	for _, d := range list {
		if d.UserID != u1.ID {
			t.Fatalf("ListByUser returned device for user %s, want %s", d.UserID, u1.ID)
		}
	}
}

func TestDeviceRevocationLog(t *testing.T) {
	repos := setupTest(t)
	ctx := testContext(t)

	u := seedUser(t, repos, "audit@example.com")
	deviceUUID := uuid.New()

	if err := repos.DeviceRevocations.RecordRevocation(ctx, u.ID, deviceUUID, u.ID); err != nil {
		t.Fatalf("RecordRevocation: %v", err)
	}

	revs, err := repos.DeviceRevocations.ListByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListByUser: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("revocations len = %d, want 1", len(revs))
	}
	r := revs[0]
	if r.DeviceUUID != deviceUUID || r.RevokedBy != u.ID || r.RevokedAt.IsZero() {
		t.Fatalf("revocation = %+v, want device %s revoked_by %s", r, deviceUUID, u.ID)
	}
}
