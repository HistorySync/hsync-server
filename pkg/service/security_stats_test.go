package service

import (
	"context"
	"testing"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

type fakeSecurityAuditStore struct {
	since24h time.Time
	since7d  time.Time
	until    time.Time
	counts   []model.SecurityEventWindowCount
}

func (f *fakeSecurityAuditStore) SecurityEventCounts(_ context.Context, since24h, since7d, until time.Time) ([]model.SecurityEventWindowCount, error) {
	f.since24h = since24h
	f.since7d = since7d
	f.until = until
	return f.counts, nil
}

type fakeSecurityUserStore struct {
	enabledUsers int64
	totalUsers   int64
}

func (f *fakeSecurityUserStore) TwoFactorEnabledStats(_ context.Context) (int64, int64, error) {
	return f.enabledUsers, f.totalUsers, nil
}

func TestSecurityStatsAggregatesAuditAndTwoFactorCounts(t *testing.T) {
	now := time.Date(2026, 6, 5, 8, 30, 0, 0, time.UTC)
	audit := &fakeSecurityAuditStore{
		counts: []model.SecurityEventWindowCount{
			{EventType: model.AuditEventLoginSuccess, Last24h: 3, Last7d: 10},
			{EventType: model.AuditEventLoginFailure, Last24h: 2, Last7d: 9},
			{EventType: model.AuditEventTwoFactorChallengeSuccess, Last24h: 4, Last7d: 11},
			{EventType: model.AuditEventTwoFactorChallengeFailure, Last24h: 1, Last7d: 5},
			{EventType: model.AuditEventAdminConfigChange, Last24h: 0, Last7d: 2},
		},
	}
	users := &fakeSecurityUserStore{enabledUsers: 25, totalUsers: 100}
	svc := NewSecurityStatsService(audit, users)
	svc.clock = func() time.Time { return now }

	stats, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if !audit.since24h.Equal(now.Add(-24*time.Hour)) || !audit.since7d.Equal(now.Add(-7*24*time.Hour)) || !audit.until.Equal(now) {
		t.Fatalf("window = %s/%s/%s, want bounded by %s", audit.since24h, audit.since7d, audit.until, now)
	}
	if stats.Last24h.LoginSuccess != 3 || stats.Last7d.LoginSuccess != 10 {
		t.Fatalf("login success = %d/%d, want 3/10", stats.Last24h.LoginSuccess, stats.Last7d.LoginSuccess)
	}
	if stats.Last24h.LoginFailure != 2 || stats.Last7d.LoginFailure != 9 {
		t.Fatalf("login failure = %d/%d, want 2/9", stats.Last24h.LoginFailure, stats.Last7d.LoginFailure)
	}
	if stats.Last24h.TwoFactorChallengeSuccess != 4 || stats.Last7d.TwoFactorChallengeSuccess != 11 {
		t.Fatalf("2fa success = %d/%d, want 4/11", stats.Last24h.TwoFactorChallengeSuccess, stats.Last7d.TwoFactorChallengeSuccess)
	}
	if stats.Last24h.TwoFactorChallengeFailure != 1 || stats.Last7d.TwoFactorChallengeFailure != 5 {
		t.Fatalf("2fa failure = %d/%d, want 1/5", stats.Last24h.TwoFactorChallengeFailure, stats.Last7d.TwoFactorChallengeFailure)
	}
	if stats.TwoFactor.EnabledUsers != 25 || stats.TwoFactor.TotalUsers != 100 || stats.TwoFactor.EnabledRatio != 0.25 {
		t.Fatalf("two factor stats = %+v, want 25/100/0.25", stats.TwoFactor)
	}
	if len(stats.EventsByType) != 5 {
		t.Fatalf("events by type len = %d, want 5", len(stats.EventsByType))
	}
}

func TestSecurityStatsEmptyDataReturnsStableStructure(t *testing.T) {
	now := time.Date(2026, 6, 5, 8, 30, 0, 0, time.UTC)
	svc := NewSecurityStatsService(nil, nil)
	svc.clock = func() time.Time { return now }

	stats, err := svc.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if !stats.GeneratedAt.Equal(now) {
		t.Fatalf("GeneratedAt = %s, want %s", stats.GeneratedAt, now)
	}
	if stats.Last24h.LoginSuccess != 0 || stats.Last7d.LoginFailure != 0 {
		t.Fatalf("empty counts are not zero: %+v %+v", stats.Last24h, stats.Last7d)
	}
	if stats.TwoFactor.EnabledUsers != 0 || stats.TwoFactor.TotalUsers != 0 || stats.TwoFactor.EnabledRatio != 0 {
		t.Fatalf("empty two factor stats = %+v, want zeros", stats.TwoFactor)
	}
	if stats.EventsByType == nil {
		t.Fatal("EventsByType is nil, want empty slice")
	}
}
