package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
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

	// proxyHostsFail, when set, makes the NPM proxy-host endpoint return 500.
	proxyHostsFail atomic.Bool
	// nodeCount controls how many nodes the Headscale node endpoint returns.
	nodeCount atomic.Int32
	// policyHits counts requests to the Headscale policy endpoint. Because the
	// policy endpoint is the first call in refresh(), it doubles as a
	// "refresh cycles started" counter.
	policyHits atomic.Int32
	// userHits and accessListHits prove refresh does not call unused endpoints.
	userHits       atomic.Int32
	accessListHits atomic.Int32
	// sleepMS, when >0, makes every Headscale endpoint sleep that many
	// milliseconds before responding, to exercise the context-timeout path.
	sleepMS atomic.Int64
}

func newTestUpstreams(t *testing.T) *testUpstreams {
	t.Helper()
	u := &testUpstreams{}
	u.nodeCount.Store(1)

	hsMux := http.NewServeMux()
	hsMux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		u.policyHits.Add(1)
		if d := u.sleepMS.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"policy": "{\"groups\":{\"group:admin\":[\"alice@\"]},\"acls\":[],\"tagOwners\":{},\"hosts\":{}}", "updatedAt": "2024-01-01T00:00:00Z"}`))
	})
	hsMux.HandleFunc("/api/v1/user", func(w http.ResponseWriter, r *http.Request) {
		u.userHits.Add(1)
		http.Error(w, "unused endpoint called", http.StatusInternalServerError)
	})
	hsMux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, r *http.Request) {
		if d := u.sleepMS.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		n := int(u.nodeCount.Load())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes": [`))
		for i := 0; i < n; i++ {
			if i > 0 {
				_, _ = w.Write([]byte(","))
			}
			_, _ = w.Write([]byte(`{"id": "1", "name": "node1", "user": {"id": "1", "name": "alice"}}`))
		}
		_, _ = w.Write([]byte(`]}`))
	})
	hsSrv := httptest.NewServer(hsMux)
	t.Cleanup(hsSrv.Close)

	npmMux := http.NewServeMux()
	npmMux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	npmMux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if u.proxyHostsFail.Load() {
			http.Error(w, "proxy hosts boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1, "domain_names": ["app.example.com"], "access_list_id": 2, "enabled": true}]`))
	})
	npmMux.HandleFunc("/api/nginx/access-lists", func(w http.ResponseWriter, r *http.Request) {
		u.accessListHits.Add(1)
		http.Error(w, "unused endpoint called", http.StatusInternalServerError)
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
	if len(data.Nodes) != 1 {
		t.Errorf("len(Nodes) = %d, want 1", len(data.Nodes))
	}
	if len(data.ProxyHosts) != 1 {
		t.Errorf("len(ProxyHosts) = %d, want 1", len(data.ProxyHosts))
	}

	if since := time.Since(c.LastUpdated()); since > 5*time.Second || since < 0 {
		t.Errorf("LastUpdated() = %v ago, want within 5s", since)
	}
}

func TestCache_RefreshSkipsUnusedEndpoints(t *testing.T) {
	u := newTestUpstreams(t)
	c := NewCache(u.hs, u.npm, time.Hour, discardLogger())

	if err := c.refresh(context.Background()); err != nil {
		t.Fatalf("refresh returned error: %v", err)
	}
	if got := u.userHits.Load(); got != 0 {
		t.Errorf("Headscale user endpoint hits = %d, want 0", got)
	}
	if got := u.accessListHits.Load(); got != 0 {
		t.Errorf("NPM access-list endpoint hits = %d, want 0", got)
	}
}

func TestLoadSnapshot_AllOrNothing(t *testing.T) {
	u := newTestUpstreams(t)

	snapshot, err := loadSnapshot(context.Background(), u.hs, u.npm)
	if err != nil {
		t.Fatalf("loadSnapshot returned error: %v", err)
	}
	if snapshot == nil || snapshot.Policy == nil {
		t.Fatalf("loadSnapshot returned incomplete snapshot: %+v", snapshot)
	}
	if len(snapshot.Nodes) != 1 || len(snapshot.ProxyHosts) != 1 || snapshot.UpdatedAt.IsZero() {
		t.Fatalf("loadSnapshot returned unexpected data: %+v", snapshot)
	}

	u.proxyHostsFail.Store(true)
	failed, err := loadSnapshot(context.Background(), u.hs, u.npm)
	if err == nil {
		t.Fatal("loadSnapshot returned nil error after proxy-host failure")
	}
	if failed != nil {
		t.Fatalf("loadSnapshot returned partial data after failure: %+v", failed)
	}
	var loadErr *snapshotLoadError
	if !errors.As(err, &loadErr) || loadErr.Stage != snapshotStageNPMProxyHosts {
		t.Fatalf("loadSnapshot error = %v, want NPM proxy-host stage", err)
	}
}

func TestCache_RefreshUpdatesData(t *testing.T) {
	u := newTestUpstreams(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := NewCache(u.hs, u.npm, 50*time.Millisecond, discardLogger())
	c.Start(ctx)

	if got := len(c.Get().Nodes); got != 1 {
		t.Fatalf("initial len(Nodes) = %d, want 1", got)
	}

	// Change what the upstream returns; the next ticker refresh should pick it up.
	u.nodeCount.Store(3)

	deadline := time.Now().Add(3 * time.Second)
	for {
		if len(c.Get().Nodes) == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cache never reflected updated node count; len(Nodes) = %d, want 3", len(c.Get().Nodes))
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

	// Change an earlier required input, then fail the final required input. None
	// of the newly fetched data should be published.
	u.nodeCount.Store(3)
	u.proxyHostsFail.Store(true)
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
	if len(after.Nodes) != 1 {
		t.Errorf("len(Nodes) after failed refresh = %d, want stale value 1", len(after.Nodes))
	}
}

type fakeControlPlane struct {
	provider controlPlaneProvider
	result   *ControlPlaneResult
	err      error
}

func (f *fakeControlPlane) Provider() controlPlaneProvider { return f.provider }
func (f *fakeControlPlane) Load(context.Context, controlPlaneProgress) (*ControlPlaneResult, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func TestCache_ControlPlaneFailureRetainsExactSnapshot(t *testing.T) {
	u := newTestUpstreams(t)
	provider := &fakeControlPlane{
		provider: controlPlaneTailscale,
		result: &ControlPlaneResult{
			Policy:                    &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"*:*"}}}},
			GrantRoleSelectorsByLogin: map[string][]string{"alice@example.com": {"autogroup:admin", "autogroup:member"}},
			Metadata:                  ControlPlaneMetadata{Provider: controlPlaneTailscale, PolicyMode: legacyACLVisibilityV1, SupportLevel: controlPlanePreview},
		},
	}
	cache := NewCache(provider, u.npm, time.Hour, discardLogger())
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatalf("initial refresh error = %v", err)
	}
	good := cache.Get()
	if got := good.GrantRoleSelectorsByLogin["alice@example.com"]; !reflect.DeepEqual(got, []string{"autogroup:admin", "autogroup:member"}) {
		t.Fatalf("initial role selectors = %v", got)
	}
	provider.result = &ControlPlaneResult{
		Policy:                    &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"*:*"}}}},
		GrantRoleSelectorsByLogin: map[string][]string{"alice@example.com": {"autogroup:member"}},
		Metadata:                  ControlPlaneMetadata{Provider: controlPlaneTailscale, PolicyMode: legacyACLVisibilityV1, SupportLevel: controlPlanePreview},
	}
	provider.err = &controlPlaneLoadError{Provider: controlPlaneTailscale, Stage: controlPlaneStageUsers, Err: errors.New("users unavailable")}
	if err := cache.refresh(context.Background()); err == nil {
		t.Fatal("refresh error = nil")
	}
	if cache.Get() != good {
		t.Fatal("cache pointer changed after control-plane failure")
	}
	if got := cache.Get().GrantRoleSelectorsByLogin["alice@example.com"]; !reflect.DeepEqual(got, []string{"autogroup:admin", "autogroup:member"}) {
		t.Fatalf("stale role selectors changed = %v", got)
	}

	provider.err = nil
	if err := cache.refresh(context.Background()); err != nil {
		t.Fatalf("recovery refresh error = %v", err)
	}
	if cache.Get() == good {
		t.Fatal("cache pointer did not change after successful recovery")
	}
	if got := cache.Get().GrantRoleSelectorsByLogin["alice@example.com"]; !reflect.DeepEqual(got, []string{"autogroup:member"}) {
		t.Fatalf("recovered role selectors = %v", got)
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
