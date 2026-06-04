package provider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// ProviderCandidate describes one selectable provider implementation.
type ProviderCandidate[T any] struct {
	Name     string
	Priority int
	Provider T
	Healthy  func(context.Context) bool
}

// SelectFirstHealthy returns the highest-priority healthy provider.
func SelectFirstHealthy[T any](ctx context.Context, candidates []ProviderCandidate[T]) (T, string, error) {
	var zero T
	if len(candidates) == 0 {
		return zero, "", errors.New("no provider candidates configured")
	}

	ordered := append([]ProviderCandidate[T](nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].Priority < ordered[j].Priority
	})

	for _, candidate := range ordered {
		if candidate.Name == "" {
			return zero, "", errors.New("provider candidate name is required")
		}
		if isZeroProvider(candidate.Provider) {
			continue
		}
		if candidate.Healthy != nil && !candidate.Healthy(ctx) {
			continue
		}
		return candidate.Provider, candidate.Name, nil
	}

	return zero, "", fmt.Errorf("no healthy provider candidates available")
}

// QuotaSelectorProvider delegates quota operations to the first healthy candidate.
type QuotaSelectorProvider struct {
	candidates []ProviderCandidate[QuotaProvider]
}

var _ QuotaProvider = (*QuotaSelectorProvider)(nil)

// NewQuotaSelectorProvider creates a fallback quota provider chain.
func NewQuotaSelectorProvider(candidates ...ProviderCandidate[QuotaProvider]) *QuotaSelectorProvider {
	return &QuotaSelectorProvider{candidates: candidates}
}

// ActiveProviderName returns the selected quota provider name.
func (p *QuotaSelectorProvider) ActiveProviderName(ctx context.Context) (string, error) {
	_, name, err := p.selectProvider(ctx)
	return name, err
}

func (p *QuotaSelectorProvider) GetLimits(userID string) (*QuotaLimitsInfo, error) {
	selected, _, err := p.selectProvider(context.Background())
	if err != nil {
		return nil, err
	}
	return selected.GetLimits(userID)
}

func (p *QuotaSelectorProvider) GetUsage(userID string) (*QuotaUsageInfo, error) {
	selected, _, err := p.selectProvider(context.Background())
	if err != nil {
		return nil, err
	}
	return selected.GetUsage(userID)
}

func (p *QuotaSelectorProvider) CheckStorageQuota(userID string, additionalBytes int64) error {
	selected, _, err := p.selectProvider(context.Background())
	if err != nil {
		return err
	}
	return selected.CheckStorageQuota(userID, additionalBytes)
}

func (p *QuotaSelectorProvider) RecordUsage(userID string, bytes int64) error {
	selected, _, err := p.selectProvider(context.Background())
	if err != nil {
		return err
	}
	return selected.RecordUsage(userID, bytes)
}

func (p *QuotaSelectorProvider) selectProvider(ctx context.Context) (QuotaProvider, string, error) {
	return SelectFirstHealthy(ctx, p.candidates)
}

func isZeroProvider[T any](provider T) bool {
	value := reflect.ValueOf(provider)
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
