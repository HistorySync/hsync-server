package model

import "testing"

func TestTierLimits(t *testing.T) {
	tests := []struct {
		name       string
		tier       UserTier
		storageGB  int64
		maxDevices int32
	}{
		{name: "free", tier: TierFree, storageGB: 1, maxDevices: 1},
		{name: "pro", tier: TierPro, storageGB: 10, maxDevices: 5},
		{name: "team", tier: TierTeam, storageGB: 50, maxDevices: 20},
		{name: "enterprise", tier: TierEnterprise, storageGB: 100, maxDevices: 100},
		{name: "unknown falls back to free", tier: UserTier("unknown"), storageGB: 1, maxDevices: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := TierLimits(tt.tier)
			if got := limits.StorageLimitBytes; got != tt.storageGB*1024*1024*1024 {
				t.Fatalf("StorageLimitBytes = %d, want %d GB", got, tt.storageGB)
			}
			if limits.MaxDevices != tt.maxDevices {
				t.Fatalf("MaxDevices = %d, want %d", limits.MaxDevices, tt.maxDevices)
			}
		})
	}
}
