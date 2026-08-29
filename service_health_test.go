package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeServiceHealthProber struct {
	mu      sync.Mutex
	calls   []int
	results map[int]ServiceHealthResult
}

func (p *fakeServiceHealthProber) Probe(_ context.Context, proxyHost ProxyHost, _ ServiceHealthService) ServiceHealthResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, proxyHost.ID)
	if result, ok := p.results[proxyHost.ID]; ok {
		return result
	}
	return ServiceHealthResult{ProxyHostID: proxyHost.ID, State: ServiceHealthStateUnknown}
}

func (p *fakeServiceHealthProber) calledIDs() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.calls...)
}

func testServiceHealthLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestServiceHealthStoreReportsStaleWithoutMutatingPublishedResult(t *testing.T) {
	base := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	current := base
	store := NewServiceHealthStore()
	store.now = func() time.Time { return current }
	store.publish(map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: base},
		2: {ProxyHostID: 2, State: ServiceHealthStateUnknown},
	}, time.Minute)

	current = base.Add(2 * time.Minute)
	result, ok := store.Get(1)
	if !ok || result.State != ServiceHealthStateStale {
		t.Fatalf("stale result = %#v, %v", result, ok)
	}
	unknown, ok := store.Get(2)
	if !ok || unknown.State != ServiceHealthStateUnknown {
		t.Fatalf("unknown result = %#v, %v", unknown, ok)
	}

	current = base.Add(30 * time.Second)
	result, ok = store.Get(1)
	if !ok || result.State != ServiceHealthStateReachable {
		t.Fatalf("published result was mutated = %#v, %v", result, ok)
	}
}

func TestServiceHealthPollerProbesOnlyConfiguredStructurallyMatchedHosts(t *testing.T) {
	checkedAt := time.Now()
	config := &ServiceHealthConfig{
		Enabled:      true,
		Interval:     time.Minute,
		Timeout:      time.Second,
		Workers:      2,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Services: []ServiceHealthService{
			{ProxyHostID: 1, Type: ServiceHealthProbeTCP},
			{ProxyHostID: 2, Type: ServiceHealthProbeTCP},
		},
	}
	updatedAt := time.Now().Add(-time.Second)
	cache := &Cache{}
	cache.data.Store(&CacheData{
		UpdatedAt: updatedAt,
		Policy: &Policy{ACLs: []ACLRule{{
			Action: "accept",
			Src:    []string{"*"},
			Dst:    []string{"10.0.0.5:443"},
		}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true},
			{ID: 2, ForwardHost: "10.0.0.6", ForwardPort: 443, Enabled: true},
		},
	})
	prober := &fakeServiceHealthProber{results: map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: checkedAt},
	}}
	poller := NewServiceHealthPoller(
		cache,
		func() (*ServiceHealthConfig, error) { return config, nil },
		func(*ServiceHealthConfig) serviceHealthProber { return prober },
		testServiceHealthLogger(),
	)

	if next := poller.runCycle(context.Background()); next != time.Minute {
		t.Fatalf("next interval = %v", next)
	}
	if got := prober.calledIDs(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("probed IDs = %v", got)
	}
	matched, ok := poller.Store().Get(1)
	if !ok || matched.State != ServiceHealthStateReachable {
		t.Fatalf("matched result = %#v, %v", matched, ok)
	}
	unmatched, ok := poller.Store().Get(2)
	if !ok || unmatched.State != ServiceHealthStateUnknown {
		t.Fatalf("unmatched result = %#v, %v", unmatched, ok)
	}
	if got := cache.LastUpdated(); !got.Equal(updatedAt) {
		t.Fatalf("health cycle changed cache freshness: %v", got)
	}
}

func TestServiceHealthPollerReloadRemovalAndInvalidRetention(t *testing.T) {
	currentConfig := &ServiceHealthConfig{
		Enabled:      true,
		Interval:     time.Minute,
		Timeout:      time.Second,
		Workers:      1,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Services:     []ServiceHealthService{{ProxyHostID: 1, Type: ServiceHealthProbeTCP}},
	}
	var loadErr error
	cache := &Cache{}
	cache.data.Store(&CacheData{
		Policy:     &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.5:443"}}}},
		ProxyHosts: []ProxyHost{{ID: 1, ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true}},
	})
	prober := &fakeServiceHealthProber{results: map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: time.Now()},
	}}
	poller := NewServiceHealthPoller(
		cache,
		func() (*ServiceHealthConfig, error) {
			if loadErr != nil {
				return nil, loadErr
			}
			return currentConfig, nil
		},
		func(*ServiceHealthConfig) serviceHealthProber { return prober },
		testServiceHealthLogger(),
	)

	poller.runCycle(context.Background())
	if _, ok := poller.Store().Get(1); !ok {
		t.Fatal("initial result was not published")
	}

	loadErr = errors.New("value-free invalid configuration")
	if next := poller.runCycle(context.Background()); next != defaultServiceHealthInterval {
		t.Fatalf("invalid config retry interval = %v", next)
	}
	if result, ok := poller.Store().Get(1); !ok || result.State != ServiceHealthStateReachable {
		t.Fatalf("invalid reload did not retain previous result = %#v, %v", result, ok)
	}

	loadErr = nil
	currentConfig = &ServiceHealthConfig{
		Enabled:      true,
		Interval:     2 * time.Minute,
		Timeout:      time.Second,
		Workers:      1,
		AllowedCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Services:     []ServiceHealthService{},
	}
	if next := poller.runCycle(context.Background()); next != 2*time.Minute {
		t.Fatalf("reloaded interval = %v", next)
	}
	if _, ok := poller.Store().Get(1); ok {
		t.Fatal("removed configured service remained published")
	}
}

func TestServiceHealthPollerDisabledConfigurationClearsResults(t *testing.T) {
	poller := NewServiceHealthPoller(
		nil,
		func() (*ServiceHealthConfig, error) { return emptyServiceHealthConfig(), nil },
		nil,
		testServiceHealthLogger(),
	)
	poller.store.publish(map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: time.Now()},
	}, time.Minute)

	poller.runCycle(context.Background())
	if _, ok := poller.Store().Get(1); ok {
		t.Fatal("disabled configuration retained a result")
	}
}
