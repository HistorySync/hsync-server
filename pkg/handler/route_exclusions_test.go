package handler

import (
	"testing"

	"github.com/gofiber/fiber/v3"
)

func TestRouteExclusionsSkipOnlyMatchingFallbackRoutes(t *testing.T) {
	h := New(Deps{
		RouteExclusions: []RouteExclusion{
			{Method: fiber.MethodPost, Path: "/api/v1/auth/register"},
			{Method: fiber.MethodGet, Path: "/api/v1/admin/security/stats"},
			{Method: fiber.MethodGet, Path: "/admin/users"},
			{Method: fiber.MethodPut, Path: "/admin/settings/:key"},
			{Method: fiber.MethodPost, Path: "/admin/users/:id/recalculate-quota"},
		},
	})
	app := fiber.New()

	h.RegisterRoutes(app)

	for _, tt := range []struct {
		method string
		path   string
		want   bool
	}{
		{method: fiber.MethodPost, path: "/api/v1/auth/register", want: false},
		{method: fiber.MethodGet, path: "/api/v1/admin/security/stats", want: false},
		{method: fiber.MethodGet, path: "/admin/users", want: false},
		{method: fiber.MethodPut, path: "/admin/settings/:key", want: false},
		{method: fiber.MethodPost, path: "/admin/users/:id/recalculate-quota", want: false},
		{method: fiber.MethodPost, path: "/api/v1/auth/login", want: true},
		{method: fiber.MethodGet, path: "/admin/stats", want: true},
		{method: fiber.MethodGet, path: "/admin/settings", want: true},
		{method: fiber.MethodGet, path: "/readyz", want: true},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			got := routeRegistered(app, tt.method, tt.path)
			if got != tt.want {
				t.Fatalf("route registered = %t, want %t", got, tt.want)
			}
		})
	}
}

func routeRegistered(app *fiber.App, method, path string) bool {
	for _, route := range app.GetRoutes(true) {
		if route.Method == method && route.Path == path {
			return true
		}
	}
	return false
}
