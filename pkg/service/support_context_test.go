package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestSupportContextLookupAggregatesBaseContext(t *testing.T) {
	userID := uuid.New()
	deviceID := uuid.New()
	deviceUUID := uuid.New()
	now := time.Now().UTC()
	users := &fakeSupportUsers{
		byID: map[uuid.UUID]*model.User{
			userID: {ID: userID, Email: "user@example.com", Tier: model.TierPro, Status: model.StatusActive},
		},
		byEmail: map[string]*model.User{},
	}
	users.byEmail["user@example.com"] = users.byID[userID]
	svc := NewSupportContextService(SupportContextDeps{
		Users: users,
		Devices: &fakeSupportDevices{devices: []model.Device{{
			ID:         deviceID,
			UserID:     userID,
			DeviceUUID: deviceUUID,
			DeviceName: "laptop",
			Platform:   "windows",
			AppVersion: "1.2.3",
			TokenHash:  []byte("secret-token-hash"),
			CreatedAt:  now,
		}}},
		Quota: &fakeSupportQuota{
			usage:  &model.QuotaUsage{UserID: userID, TotalBytes: 42, BundleCount: 2, SnapCount: 1},
			limits: &model.QuotaLimits{UserID: userID, StorageLimitBytes: 100, MaxDevices: 3},
		},
		Audit: &fakeSupportAudit{logs: []model.AuditLog{{
			ID:         uuid.New(),
			EventType:  model.AuditEventLoginSuccess,
			TargetType: "user",
			TargetID:   userID.String(),
			Metadata:   map[string]any{"token": "hidden", "result": "ok"},
			CreatedAt:  now,
		}}},
	})

	ctx, err := svc.Lookup(context.Background(), SupportContextLookup{Email: "user@example.com"})
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if ctx.User == nil || ctx.User.ID != userID {
		t.Fatalf("Lookup() user = %#v, want %s", ctx.User, userID)
	}
	if len(ctx.Devices) != 1 || ctx.Devices[0].DeviceUUID != deviceUUID {
		t.Fatalf("Lookup() devices = %#v", ctx.Devices)
	}
	if ctx.Quota == nil || ctx.Quota.EffectiveLimit.StorageLimitBytes != 100 {
		t.Fatalf("Lookup() quota = %#v", ctx.Quota)
	}
	if len(ctx.RecentAudit) != 1 || ctx.RecentAudit[0].Metadata["token"] != nil {
		t.Fatalf("Lookup() audit metadata = %#v", ctx.RecentAudit)
	}
}

func TestSupportContextLookupRejectsMismatchedCriteria(t *testing.T) {
	idUser := &model.User{ID: uuid.New(), Email: "first@example.com"}
	emailUser := &model.User{ID: uuid.New(), Email: "second@example.com"}
	svc := NewSupportContextService(SupportContextDeps{
		Users: &fakeSupportUsers{
			byID:    map[uuid.UUID]*model.User{idUser.ID: idUser},
			byEmail: map[string]*model.User{emailUser.Email: emailUser},
		},
	})

	_, err := svc.Lookup(context.Background(), SupportContextLookup{
		UserID: idUser.ID.String(),
		Email:  emailUser.Email,
	})
	if !errors.Is(err, ErrSupportContextLookupMismatch) {
		t.Fatalf("Lookup() error = %v, want ErrSupportContextLookupMismatch", err)
	}
}

type fakeSupportUsers struct {
	byID    map[uuid.UUID]*model.User
	byEmail map[string]*model.User
}

func (f *fakeSupportUsers) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	if user := f.byID[id]; user != nil {
		clone := *user
		return &clone, nil
	}
	return nil, nil
}

func (f *fakeSupportUsers) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	if user := f.byEmail[email]; user != nil {
		clone := *user
		return &clone, nil
	}
	return nil, nil
}

type fakeSupportDevices struct {
	devices []model.Device
}

func (f *fakeSupportDevices) ListByUser(ctx context.Context, userID uuid.UUID) ([]model.Device, error) {
	return append([]model.Device(nil), f.devices...), nil
}

type fakeSupportQuota struct {
	usage  *model.QuotaUsage
	limits *model.QuotaLimits
}

func (f *fakeSupportQuota) GetUsage(ctx context.Context, userID uuid.UUID) (*model.QuotaUsage, error) {
	clone := *f.usage
	return &clone, nil
}

func (f *fakeSupportQuota) GetLimits(ctx context.Context, userID uuid.UUID) (*model.QuotaLimits, error) {
	if f.limits == nil {
		return nil, nil
	}
	clone := *f.limits
	return &clone, nil
}

type fakeSupportAudit struct {
	logs []model.AuditLog
}

func (f *fakeSupportAudit) ListVisibleByUser(ctx context.Context, userID uuid.UUID, limit int32) ([]model.AuditLog, error) {
	return append([]model.AuditLog(nil), f.logs...), nil
}
