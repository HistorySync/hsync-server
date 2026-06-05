package repository

import (
	"encoding/json"
	"testing"

	"github.com/historysync/hsync-server/pkg/model"
)

func TestNormalizeAuditListFilter(t *testing.T) {
	filter := normalizeAuditListFilter(model.AuditListFilter{Limit: 1000, Offset: -5})
	if filter.Limit != defaultAuditListLimit {
		t.Fatalf("Limit = %d, want %d", filter.Limit, defaultAuditListLimit)
	}
	if filter.Offset != 0 {
		t.Fatalf("Offset = %d, want 0", filter.Offset)
	}
}

func TestEncodeAuditMetadataNilAsObject(t *testing.T) {
	data, err := encodeAuditMetadata(nil)
	if err != nil {
		t.Fatalf("encodeAuditMetadata() error = %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("metadata = %#v, want empty object", got)
	}
}
