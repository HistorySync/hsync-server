package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/historysync/hsync-server/pkg/config"
	"github.com/historysync/hsync-server/pkg/service"
	"github.com/historysync/hsync-server/pkg/storage"
)

type handlerOpsBlobStore struct {
	data map[string][]byte
}

func newHandlerOpsBlobStore() *handlerOpsBlobStore {
	return &handlerOpsBlobStore{data: map[string][]byte{}}
}

func (s *handlerOpsBlobStore) Put(_ context.Context, key string, reader io.Reader, _ int64, _ string) error {
	body, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.data[key] = body
	return nil
}

func (s *handlerOpsBlobStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s.data[key]))), nil
}

func (s *handlerOpsBlobStore) Delete(_ context.Context, key string) error {
	delete(s.data, key)
	return nil
}

func (s *handlerOpsBlobStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := s.data[key]
	return ok, nil
}

func (s *handlerOpsBlobStore) Size(_ context.Context, key string) (int64, bool, error) {
	body, ok := s.data[key]
	return int64(len(body)), ok, nil
}

func (s *handlerOpsBlobStore) List(context.Context, string) ([]storage.ObjectInfo, error) {
	return []storage.ObjectInfo{}, nil
}

func TestAdminOpsSummaryAndCheck(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.DatabaseURL = "postgres://hsync:secret@db.example:5432/hsync?sslmode=disable"
	cfg.RedisURL = "redis://redis.example:6379/0"
	cfg.S3Endpoint = "minio.example:9000"
	cfg.S3Bucket = "hsync-bundles"
	ops := service.NewOpsService(service.OpsDeps{
		Config:       cfg,
		BlobStore:    newHandlerOpsBlobStore(),
		DatabasePing: func(context.Context) error { return nil },
		RedisPing:    func(context.Context) error { return nil },
	})
	h := New(Deps{
		Services: &service.Services{Ops: ops},
		AdminKey: "secret",
	})
	app := fiber.New(fiber.Config{ErrorHandler: h.ErrorHandler})
	h.RegisterRoutes(app)

	summaryReq := httptest.NewRequest("GET", "/admin/ops/summary", nil)
	summaryReq.Header.Set("X-Admin-Key", "secret")
	summaryResp, err := app.Test(summaryReq)
	if err != nil {
		t.Fatalf("summary app.Test: %v", err)
	}
	if summaryResp.StatusCode != fiber.StatusOK {
		t.Fatalf("summary status = %d, want %d", summaryResp.StatusCode, fiber.StatusOK)
	}
	var summary service.OpsSummary
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if summary.Readiness.LastDependencyCheck != nil {
		t.Fatalf("initial last dependency check = %+v, want nil", summary.Readiness.LastDependencyCheck)
	}
	if len(summary.Backup.Components) == 0 {
		t.Fatal("summary backup guidance is empty")
	}

	checkReq := httptest.NewRequest("POST", "/admin/ops/check", nil)
	checkReq.Header.Set("X-Admin-Key", "secret")
	checkResp, err := app.Test(checkReq)
	if err != nil {
		t.Fatalf("check app.Test: %v", err)
	}
	if checkResp.StatusCode != fiber.StatusOK {
		t.Fatalf("check status = %d, want %d", checkResp.StatusCode, fiber.StatusOK)
	}
	var check service.OpsDependencyReport
	if err := json.NewDecoder(checkResp.Body).Decode(&check); err != nil {
		t.Fatalf("decode check: %v", err)
	}
	if check.Overall != service.OpsStatusOK {
		t.Fatalf("check overall = %q, want ok: %+v", check.Overall, check.Checks)
	}

	summaryReq = httptest.NewRequest("GET", "/api/v1/admin/ops/summary", nil)
	summaryReq.Header.Set("X-Admin-Key", "secret")
	summaryResp, err = app.Test(summaryReq)
	if err != nil {
		t.Fatalf("v1 summary app.Test: %v", err)
	}
	if summaryResp.StatusCode != fiber.StatusOK {
		t.Fatalf("v1 summary status = %d, want %d", summaryResp.StatusCode, fiber.StatusOK)
	}
	if err := json.NewDecoder(summaryResp.Body).Decode(&summary); err != nil {
		t.Fatalf("decode v1 summary: %v", err)
	}
	if summary.Readiness.LastDependencyCheck == nil || summary.Readiness.LastDependencyCheck.Overall != service.OpsStatusOK {
		t.Fatalf("summary last dependency check = %+v, want ok report", summary.Readiness.LastDependencyCheck)
	}
}
