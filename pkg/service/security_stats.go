package service

import (
	"context"
	"fmt"
	"time"

	"github.com/historysync/hsync-server/pkg/model"
)

type securityAuditStore interface {
	SecurityEventCounts(ctx context.Context, since24h, since7d, until time.Time) ([]model.SecurityEventWindowCount, error)
}

type securityUserStore interface {
	TwoFactorEnabledStats(ctx context.Context) (enabledUsers int64, totalUsers int64, err error)
}

type SecurityStatsService struct {
	audit securityAuditStore
	users securityUserStore
	clock func() time.Time
}

func NewSecurityStatsService(audit securityAuditStore, users securityUserStore) *SecurityStatsService {
	return &SecurityStatsService{
		audit: audit,
		users: users,
		clock: time.Now,
	}
}

func (s *SecurityStatsService) Stats(ctx context.Context) (*model.SecurityStats, error) {
	now := time.Now().UTC()
	if s != nil && s.clock != nil {
		now = s.clock().UTC()
	}
	stats := emptySecurityStats(now)
	if s == nil {
		return stats, nil
	}

	if s.audit != nil {
		counts, err := s.audit.SecurityEventCounts(ctx, stats.Last24h.Since, stats.Last7d.Since, stats.GeneratedAt)
		if err != nil {
			return nil, fmt.Errorf("security event counts: %w", err)
		}
		applySecurityEventCounts(stats, counts)
	}

	if s.users != nil {
		enabledUsers, totalUsers, err := s.users.TwoFactorEnabledStats(ctx)
		if err != nil {
			return nil, fmt.Errorf("two factor enabled stats: %w", err)
		}
		stats.TwoFactor.EnabledUsers = enabledUsers
		stats.TwoFactor.TotalUsers = totalUsers
		if totalUsers > 0 {
			stats.TwoFactor.EnabledRatio = float64(enabledUsers) / float64(totalUsers)
		}
	}

	return stats, nil
}

func emptySecurityStats(now time.Time) *model.SecurityStats {
	since24h := now.Add(-24 * time.Hour)
	since7d := now.Add(-7 * 24 * time.Hour)
	return &model.SecurityStats{
		GeneratedAt: now,
		Last24h: model.SecurityStatsWindow{
			Since: since24h,
			Until: now,
		},
		Last7d: model.SecurityStatsWindow{
			Since: since7d,
			Until: now,
		},
		TwoFactor:    model.SecurityTwoFactorStats{},
		EventsByType: []model.AuditEventTypeCount{},
	}
}

func applySecurityEventCounts(stats *model.SecurityStats, counts []model.SecurityEventWindowCount) {
	for _, count := range counts {
		if count.Last7d > 0 {
			stats.EventsByType = append(stats.EventsByType, model.AuditEventTypeCount{
				EventType: count.EventType,
				Count:     count.Last7d,
			})
		}
		switch count.EventType {
		case model.AuditEventLoginSuccess:
			stats.Last24h.LoginSuccess = count.Last24h
			stats.Last7d.LoginSuccess = count.Last7d
		case model.AuditEventLoginFailure:
			stats.Last24h.LoginFailure = count.Last24h
			stats.Last7d.LoginFailure = count.Last7d
		case model.AuditEventTwoFactorChallengeSuccess:
			stats.Last24h.TwoFactorChallengeSuccess = count.Last24h
			stats.Last7d.TwoFactorChallengeSuccess = count.Last7d
		case model.AuditEventTwoFactorChallengeFailure:
			stats.Last24h.TwoFactorChallengeFailure = count.Last24h
			stats.Last7d.TwoFactorChallengeFailure = count.Last7d
		}
	}
}
