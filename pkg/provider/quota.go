// Package provider contains the default (CE) QuotaProvider implementation.
package provider

// UnlimitedQuotaProvider is the CE default: no quota restrictions.
type UnlimitedQuotaProvider struct{}

// GetLimits returns unlimited quotas for CE single-user mode.
func (p *UnlimitedQuotaProvider) GetLimits(userID string) (*QuotaLimitsInfo, error) {
	return &QuotaLimitsInfo{
		StorageLimitBytes:   1 << 40, // ~1 TB (effectively unlimited for single user)
		MaxDevices:          1,
		MaxBundleSize:       50 * 1024 * 1024, // 50 MB
		MaxSnapshots:        1,
		MaxRPM:              1000,
		BundleRetentionDays: 365,
	}, nil
}

// GetUsage returns zero usage in CE mode (no tracking needed).
func (p *UnlimitedQuotaProvider) GetUsage(userID string) (*QuotaUsageInfo, error) {
	return &QuotaUsageInfo{}, nil
}

// CheckStorageQuota always passes in CE mode.
func (p *UnlimitedQuotaProvider) CheckStorageQuota(userID string, additionalBytes int64) error {
	return nil
}

// RecordUsage is a no-op in CE mode.
func (p *UnlimitedQuotaProvider) RecordUsage(userID string, bytes int64) error {
	return nil
}

// defaultQuotaProvider is the globally registered default.
var defaultQuotaProvider QuotaProvider = &UnlimitedQuotaProvider{}
