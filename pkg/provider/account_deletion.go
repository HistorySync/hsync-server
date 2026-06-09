package provider

import "context"

// AllowAccountDeletionPolicy is the CE default: account deletion is allowed
// unless an embedding edition supplies a stricter policy.
type AllowAccountDeletionPolicy struct{}

func (p *AllowAccountDeletionPolicy) EvaluateAccountDeletion(ctx context.Context, req AccountDeletionRequest) (*AccountDeletionDecision, error) {
	return &AccountDeletionDecision{Allowed: true}, nil
}

var defaultAccountDeletionPolicy AccountDeletionPolicy = &AllowAccountDeletionPolicy{}

type NoopAccountErasureReporter struct{}

func (r *NoopAccountErasureReporter) DescribeAccountErasure(ctx context.Context, req AccountErasureReportRequest) (*AccountErasureReport, error) {
	return &AccountErasureReport{}, nil
}

var defaultAccountErasureReporter AccountErasureReporter = &NoopAccountErasureReporter{}
