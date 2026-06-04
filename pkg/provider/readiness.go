// Package provider contains the default (CE) ReadinessProvider implementation.
package provider

import "context"

// NoopReadinessProvider is the CE default: it contributes no extra checks.
type NoopReadinessProvider struct{}

// ReadinessChecks returns no additional checks in CE mode.
func (p *NoopReadinessProvider) ReadinessChecks(ctx context.Context) []ReadinessCheck {
	return nil
}

// defaultReadinessProvider is the globally registered default.
var defaultReadinessProvider ReadinessProvider = &NoopReadinessProvider{}
