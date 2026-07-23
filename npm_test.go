package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// futureExpiry returns an RFC3339 timestamp far enough in the future that the
// client's ensureToken (>1h remaining) treats the token as reusable.
func futureExpiry() string {
	return time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
}

func TestNPMClient_AuthenticateAndFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("/api/tokens method = %s, want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer jwt-abc" {
			t.Errorf("Authorization = %q, want Bearer jwt-abc", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 1, "domain_names": ["app.example.com"], "forward_scheme": "http", "forward_host": "10.0.0.5", "forward_port": 8080, "access_list_id": 2, "enabled": true, "meta": {"nginx_online": true}}]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewNPMClient(srv.URL, "admin@example.com", "changeme", srv.Client())

	hosts, err := client.FetchProxyHosts(context.Background())
	if err != nil {
		t.Fatalf("FetchProxyHosts returned error: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("len(hosts) = %d, want 1", len(hosts))
	}
	h := hosts[0]
	if h.ID != 1 {
		t.Errorf("ID = %d, want 1", h.ID)
	}
	if len(h.DomainNames) != 1 || h.DomainNames[0] != "app.example.com" {
		t.Errorf("DomainNames = %v, want [app.example.com]", h.DomainNames)
	}
	if h.ForwardScheme != "http" || h.ForwardHost != "10.0.0.5" || h.ForwardPort != 8080 {
		t.Errorf("forward = %s://%s:%d, unexpected", h.ForwardScheme, h.ForwardHost, h.ForwardPort)
	}
	if h.AccessListID != 2 {
		t.Errorf("AccessListID = %d, want 2", h.AccessListID)
	}
	if !h.Enabled || !h.Meta.NginxOnline {
		t.Errorf("Enabled=%v NginxOnline=%v, want both true", h.Enabled, h.Meta.NginxOnline)
	}
}

func TestNPMClient_TokenReuse(t *testing.T) {
	var authCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&authCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewNPMClient(srv.URL, "admin@example.com", "changeme", srv.Client())

	for i := 0; i < 2; i++ {
		if _, err := client.FetchProxyHosts(context.Background()); err != nil {
			t.Fatalf("FetchProxyHosts call %d returned error: %v", i, err)
		}
	}

	if n := atomic.LoadInt32(&authCalls); n != 1 {
		t.Errorf("auth calls = %d, want 1 (token should be cached)", n)
	}
}

func TestNPMClient_ReauthOn401(t *testing.T) {
	var authCalls int32
	var proxyCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&authCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		// First GET returns 401 (stale token); subsequent GETs succeed.
		if atomic.AddInt32(&proxyCalls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 7, "domain_names": ["ok.example.com"]}]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewNPMClient(srv.URL, "admin@example.com", "changeme", srv.Client())

	hosts, err := client.FetchProxyHosts(context.Background())
	if err != nil {
		t.Fatalf("FetchProxyHosts returned error: %v", err)
	}
	if len(hosts) != 1 || hosts[0].ID != 7 {
		t.Fatalf("hosts = %+v, want single host with ID 7", hosts)
	}
	// One initial auth (ensureToken) + one re-auth after the 401.
	if n := atomic.LoadInt32(&authCalls); n != 2 {
		t.Errorf("auth calls = %d, want 2", n)
	}
}

func TestNPMClient_FetchAccessLists(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token": "jwt-abc", "expires": "` + futureExpiry() + `"}`))
	})
	mux.HandleFunc("/api/nginx/access-lists", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id": 3, "name": "team", "items": [{"username": "alice", "hint": "admin"}], "clients": [{"address": "10.0.0.0/24", "directive": "allow"}]}]`))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewNPMClient(srv.URL, "admin@example.com", "changeme", srv.Client())

	lists, err := client.FetchAccessLists(context.Background())
	if err != nil {
		t.Fatalf("FetchAccessLists returned error: %v", err)
	}
	if len(lists) != 1 {
		t.Fatalf("len(lists) = %d, want 1", len(lists))
	}
	l := lists[0]
	if l.ID != 3 || l.Name != "team" {
		t.Errorf("list = %+v, want ID=3 Name=team", l)
	}
	if len(l.Items) != 1 || l.Items[0].Username != "alice" || l.Items[0].Hint != "admin" {
		t.Errorf("Items = %+v, unexpected", l.Items)
	}
	if len(l.Clients) != 1 || l.Clients[0].Address != "10.0.0.0/24" || l.Clients[0].Directive != "allow" {
		t.Errorf("Clients = %+v, unexpected", l.Clients)
	}
}

func TestNPMClient_AuthFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tokens", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "forbidden"}`))
	})
	mux.HandleFunc("/api/nginx/proxy-hosts", func(w http.ResponseWriter, r *http.Request) {
		t.Error("proxy-hosts should not be reached when auth fails")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := NewNPMClient(srv.URL, "admin@example.com", "changeme", srv.Client())

	_, err := client.FetchProxyHosts(context.Background())
	if err == nil {
		t.Fatal("expected error when authentication fails, got nil")
	}
}
