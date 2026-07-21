package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIdentityFromContext(t *testing.T) {
	t.Run("returns nil for empty context", func(t *testing.T) {
		if got := IdentityFromContext(context.Background()); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("round-trips through context", func(t *testing.T) {
		id := &Identity{Login: "alice@example.com", Name: "Alice"}
		ctx := context.WithValue(context.Background(), identityKey, id)
		got := IdentityFromContext(ctx)
		if got == nil || got.Login != "alice@example.com" {
			t.Errorf("expected alice, got %+v", got)
		}
	})
}

func TestIdentityMiddleware(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8")

	captureIdentity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := IdentityFromContext(r.Context())
		if id == nil {
			t.Error("identity should be set in context")
			return
		}
		w.Header().Set("X-Login", id.Login)
		w.Header().Set("X-Name", id.Name)
		w.WriteHeader(http.StatusOK)
	})

	handler := IdentityMiddleware(trusted, captureIdentity)

	t.Run("trusted source with headers passes through", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Tailscale-User-Login", "alice@example.com")
		req.Header.Set("Tailscale-User-Name", "Alice Smith")
		req.Header.Set("Tailscale-User-Profile-Pic", "https://pic.example.com/alice.jpg")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rec.Code)
		}
		if rec.Header().Get("X-Login") != "alice@example.com" {
			t.Errorf("expected alice login, got %q", rec.Header().Get("X-Login"))
		}
		if rec.Header().Get("X-Name") != "Alice Smith" {
			t.Errorf("expected Alice Smith, got %q", rec.Header().Get("X-Name"))
		}
	})

	t.Run("untrusted source is rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "192.168.1.1:12345"
		req.Header.Set("Tailscale-User-Login", "attacker@evil.com")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", rec.Code)
		}
	})

	t.Run("trusted source without identity header is rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:12345"

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", rec.Code)
		}
	})

	t.Run("spoofed headers from untrusted source are blocked", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "10.0.0.5:9999"
		req.Header.Set("Tailscale-User-Login", "admin@corp.com")
		req.Header.Set("Tailscale-User-Name", "Admin")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for spoofed headers, got %d", rec.Code)
		}
	})
}
