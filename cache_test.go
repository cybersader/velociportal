package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// discardLogger returns a slog.Logger that writes nowhere, so cache logging
// does not pollute test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testUpstreams bundles fake Headscale + NPM httptest servers together with the
// real clients pointed at them. The atomic fields let a test steer what the
// servers return and observe how often they are hit, all race-free.
type testUpstreams struct {
	hs  *HeadscaleClient
	npm *NPMClient

	// policyFail, when set, makes the Headscale policy endpoint return 500.
	policyFail atomic.Bool
	// userCount controls how many users the Headscale user endpoint returns.
	userCount atomic.Int32
	// policyHits counts requests to the Headscale policy endpoint. Because the
	// policy endpoint is the first call in refresh(), it doubles as a
	// "refresh cycles started" counter.
	policyHits atomic.Int32
	// sleepMS, when >0, makes every Headscale endpoint sleep that many
	// milliseconds before responding, to exercise the context-timeout path.
	sleepMS atomic.Int64
}

func newTestUpstreams(t *testing.T) *testUpstreams {
	t.Helper()
	u := &testUpstreams{}
	u.userCount.Store(1)

	hsMux := http.NewServeMux()
	hsMux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		u.policyHits.Add(1)
		if d := u.sleepMS.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		if u.policyFail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("policy boom"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policy": "{\"groups\":{\"group:admin\":[\"alice@\"]},\"acls\":[],\"tagOwners\":{},\"hosts\":{}}", "updatedAt": "2024-01-01T00:00:00Z"}`))
	})
	hsMux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		if d := u.sleepMS.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		n := int(u.userCount.Load())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users": [`))
		for i := 0; i < n; i++ {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"id": "1", "name": "alice"}`))
		}
		_, _ = w.Write([]byte(`]}`))
	})
	hsMux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, r *http.Request) {
		if d := u.sleepMS.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes": [{"id": "1", "name": "node1", "user": {"id": "1", "name": "alice"}}]}`))
	})
	hsSrv := httptest.NewServer(hsMux)
	t.Cleanup(hsSrv.Close)

	npmMux := http.NewServeMux()
	npmMux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	npmMux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1, "domain_names": ["app.example.com"], "access_list_id": 2, "enabled": true}]`))
	})
	npmMux.HandleFunc("/api/nginx/access-lists", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 2, "name": "team"}]`))
	})
	npmSrv := httptest.NewServer(npmMux)
	t.Cleanup(npmSrv.Close)

	u.hs = NewHeadscaleClient(hsSrv.URL, "test-api-key", hsSrv.Client())
	u.npm = NewNPMClient(npmSrv.URL, "admin@example.com", "changeme", npmSrv.Client())
	return u
}

func TestCache_InitialRefresh(t *testing.T) {
	u := newTestUpstreams(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())
	c.Start(ctx)

	data := c.Get()
	if data == nil {
		t.Fatal("Get() returned nil after Start()")
	}
	if data.Policy == nil {
		t.Error("Policy is nil")
	}
	if len(data.Users) != 1 {
		t.Errorf("len(Users) = %d, want 1", len(data.Users))
	}
	if len(data.Nodes) != 1 {
		t.Errorf("len(Nodes) = %d, want 1", len(data.Nodes))
	}
	if len(data.ProxyHosts) != 1 {
		t.Errorf("len(ProxyHosts) = %d, want 1", len(data.ProxyHosts))
	}
	if len(data.AccessLists) != 1 {
		t.Errorf("len(AccessLists) = %d, want 1", len(data.AccessLists))
	}

	if since := time.Since(c.LastUpdated()); since > 5*time.Second || since < 0 {
		t.Errorf("LastUpdated() = %v ago, want within 5s", since)
	}
}

func TestCache_RefreshUpdatesData(t *testing.T) {
	u := newTestUpstreams(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewCache(u.hs, u.npm, 50*time.Millisecond, discardLogger())
	c.Start(ctx)

	if got := len(c.Get().Users); got != 1 {
		t.Fatalf("initial len(Users) = %d, want 1", got)
	}

	// Change what the upstream returns; the next ticker refresh should pick it up.
	u.userCount.Store(3)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(c.Get().Users) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never reflected updated user count; len(Users) = %d, want 3", len(c.Get().Users))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCache_PartialFailure(t *testing.T) {
	u := newTestUpstreams(t)
	ctx := context.Background()

	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())

	// First refresh succeeds and populates the cache.
	if err := c.refresh(ctx); err != nil {
		t.Fatalf("initial refresh returned error: %v", err)
	}
	good := c.Get()
	if good == nil {
		t.Fatal("cache empty after successful refresh")
	}

	// Now break the policy endpoint (the first upstream call in refresh).
	u.policyFail.Store(true)
	err := c.refresh(ctx)
	if err == nil {
		t.Fatal("refresh returned nil error despite failing upstream")
	}

	// Old data must be preserved untouched — no partial results stored.
	after := c.Get()
	if after != good {
		t.Errorf("cache pointer changed after failed refresh; stale data was not preserved")
	}
	if !after.UpdatedAt.Equal(good.UpdatedAt) {
		t.Errorf("UpdatedAt changed after failed refresh: %v != %v", after.UpdatedAt, good.UpdatedAt)
	}
}

func TestCache_GetReturnsNilBeforeStart(t *testing.T) {
	u := newTestUpstreams(t)
	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())

	if got := c.Get(); got != nil {
		t.Errorf("Get() before Start() = %+v, want nil", got)
	}
	if got := c.LastUpdated(); !got.IsZero() {
		t.Errorf("LastUpdated() before Start() = %v, want zero time", got)
	}
}

func TestCache_ContextCancellation(t *testing.T) {
	u := newTestUpstreams(t)
	ctx, cancel := context.WithCancel(context.Background())

	c := NewCache(u.hs, u.npm, 20*time.Millisecond, discardLogger())
	c.Start(ctx)

	// Let a few ticker refreshes happen.
	time.Sleep(100 * time.Millisecond)
	cancel()

	// Give the goroutine a moment to observe cancellation and stop.
	time.Sleep(60 * time.Millisecond)
	before := u.policyHits.Load()

	// After cancellation, no further refreshes should occur.
	time.Sleep(120 * time.Millisecond)
	after := u.policyHits.Load()

	if after != before {
		t.Errorf("policy endpoint hit count grew after cancellation: before=%d after=%d", before, after)
	}
}

func TestCache_CallTimeout(t *testing.T) {
	u := newTestUpstreams(t)
	// Make upstreams slow enough to blow past our short parent deadline. The
	// upstreamTimeout constant (10s) is far too long for a test, so we drive the
	// timeout via a short-deadline parent context instead.
	u.sleepMS.Store(500)

	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.refresh(ctx)
	if err == nil {
		t.Fatal("refresh returned nil error despite context deadline being exceeded")
	}
	if c.Get() != nil {
		t.Errorf("cache populated despite timed-out refresh: %+v", c.Get())
	}
}

func TestCache_LastUpdated(t *testing.T) {
	u := newTestUpstreams(t)
	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())

	if got := c.LastUpdated(); !got.Equal(time.Time{}) {
		t.Errorf("LastUpdated() before refresh = %v, want time.Time{}", got)
	}

	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}

	if since := time.Since(c.LastUpdated()); since > 5*time.Second || since < 0 {
		t.Errorf("LastUpdated() after refresh = %v ago, want recent", since)
	}
}
