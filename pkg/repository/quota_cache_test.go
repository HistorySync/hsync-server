package repository

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/historysync/hsync-server/pkg/model"
)

// TestQuotaUsageJSONRoundtrip verifies the QuotaUsage struct serializes and
// deserializes correctly, since the read-through cache stores JSON in Redis.
func TestQuotaUsageJSONRoundtrip(t *testing.T) {
	original := &model.QuotaUsage{
		UserID:      uuid.New(),
		TotalBytes:  1024 * 1024 * 50,
		BundleCount: 42,
		SnapCount:   3,
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored model.QuotaUsage
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if restored.UserID != original.UserID ||
		restored.TotalBytes != original.TotalBytes ||
		restored.BundleCount != original.BundleCount ||
		restored.SnapCount != original.SnapCount {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", restored, original)
	}
}

// TestCacheHelpersNilRedis verifies that cache operations are safe no-ops when
// the Redis client is nil (graceful degradation path).
func TestCacheHelpersNilRedis(t *testing.T) {
	r := &QuotaRepo{pool: nil, redis: nil}
	userID := uuid.New()
	ctx := context.Background()

	// getCachedUsage returns nil when redis is nil.
	if got := r.getCachedUsage(ctx, userID); got != nil {
		t.Fatal("getCachedUsage with nil redis returned non-nil")
	}

	// cacheUsage and invalidateUsageCache are no-ops (no panic).
	r.cacheUsage(ctx, &model.QuotaUsage{UserID: userID, TotalBytes: 100})
	r.invalidateUsageCache(ctx, userID)
}

// TestQuotaCacheKeyIsDeterministic verifies the cache key format is stable.
func TestQuotaCacheKeyIsDeterministic(t *testing.T) {
	id := uuid.New()
	k1 := quotaCacheKey(id)
	k2 := quotaCacheKey(id)
	if k1 != k2 {
		t.Fatalf("cache key not deterministic: %q vs %q", k1, k2)
	}
	if k1 != "quota:usage:"+id.String() {
		t.Fatalf("unexpected cache key format: %q", k1)
	}
}
