package provider

import "context"

// NoopOpsRestoreProvider is the CE default: it contributes no extra restore
// rehearsal checks.
type NoopOpsRestoreProvider struct{}

// RestoreChecks returns no additional checks in CE mode.
func (p *NoopOpsRestoreProvider) RestoreChecks(ctx context.Context) []OpsRestoreCheck {
	return nil
}

// defaultOpsRestoreProvider is the globally registered default.
var defaultOpsRestoreProvider OpsRestoreProvider = &NoopOpsRestoreProvider{}
