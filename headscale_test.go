package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newHeadscaleTestServer creates an httptest server whose handler returns the
// given status and body for any request, and a HeadscaleClient pointed at it.
func newHeadscaleTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *HeadscaleClient) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewHeadscaleClient(srv.URL, "test-api-key", srv.Client())
	return srv, client
}

func TestFetchPolicy_ValidResponse(t *testing.T) {
	body := `{"policy": "{\"groups\":{\"group:admin\":[\"alice@\"]},\"tagOwners\":{\"tag:server\":[\"group:admin\"]},\"acls\":[{\"action\":\"accept\",\"src\":[\"group:admin\"],\"dst\":[\"10.0.0.1:*\"]}],\"hosts\":{\"webserver\":\"10.0.0.5\"}}", "updatedAt": "2024-01-01T00:00:00Z"}`
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/policy" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	p, err := client.FetchPolicy(context.Background())
	if err != nil {
		t.Fatalf("FetchPolicy returned error: %v", err)
	}

	if got := p.Groups["group:admin"]; len(got) != 1 || got[0] != "alice@" {
		t.Errorf("Groups[group:admin] = %v, want [alice@]", got)
	}
	if got := p.TagOwners["tag:server"]; len(got) != 1 || got[0] != "group:admin" {
		t.Errorf("TagOwners[tag:server] = %v, want [group:admin]", got)
	}
	if len(p.ACLs) != 1 {
		t.Fatalf("len(ACLs) = %d, want 1", len(p.ACLs))
	}
	acl := p.ACLs[0]
	if acl.Action != "accept" || len(acl.Src) != 1 || acl.Src[0] != "group:admin" || len(acl.Dst) != 1 || acl.Dst[0] != "10.0.0.1:*" {
		t.Errorf("ACLs[0] = %+v, unexpected", acl)
	}
	if got := p.Hosts["webserver"]; got != "10.0.0.5" {
		t.Errorf("Hosts[webserver] = %q, want 10.0.0.5", got)
	}
}

func TestFetchPolicy_HuJSON(t *testing.T) {
	// The inner policy string contains huJSON features: a trailing comma inside
	// the groups object, a // comment, and a trailing comma inside acls.
	body := `{"policy": "{\"groups\":{\"group:admin\":[\"alice@\"],},// admin group\n\"acls\":[{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]},]}", "updatedAt": "2024-01-01T00:00:00Z"}`
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	p, err := client.FetchPolicy(context.Background())
	if err != nil {
		t.Fatalf("FetchPolicy returned error: %v", err)
	}

	if got := p.Groups["group:admin"]; len(got) != 1 || got[0] != "alice@" {
		t.Errorf("Groups[group:admin] = %v, want [alice@]", got)
	}
	if len(p.ACLs) != 1 {
		t.Fatalf("len(ACLs) = %d, want 1", len(p.ACLs))
	}
	if p.ACLs[0].Action != "accept" {
		t.Errorf("ACLs[0].Action = %q, want accept", p.ACLs[0].Action)
	}
}

func TestFetchPolicy_EmptyPolicy(t *testing.T) {
	body := `{"policy": "", "updatedAt": "2024-01-01T00:00:00Z"}`
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	p, err := client.FetchPolicy(context.Background())
	if err != nil {
		t.Fatalf("FetchPolicy returned error for empty policy: %v", err)
	}
	if p == nil {
		t.Fatal("FetchPolicy returned nil policy")
	}
	if len(p.Groups) != 0 || len(p.TagOwners) != 0 || len(p.ACLs) != 0 || len(p.Hosts) != 0 {
		t.Errorf("expected empty policy, got %+v", p)
	}
}

func TestFetchUsers(t *testing.T) {
	body := `{"users": [{"id": "1", "name": "alice"}, {"id": "2", "name": "bob"}]}`
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	users, err := client.FetchUsers(context.Background())
	if err != nil {
		t.Fatalf("FetchUsers returned error: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(users))
	}
	if users[0].Name != "alice" || users[0].ID != "1" {
		t.Errorf("users[0] = %+v, want {ID:1 Name:alice}", users[0])
	}
	if users[1].Name != "bob" || users[1].ID != "2" {
		t.Errorf("users[1] = %+v, want {ID:2 Name:bob}", users[1])
	}
}

func TestFetchNodes(t *testing.T) {
	body := `{"nodes": [{"id": "1", "name": "node1", "user": {"id": "1", "name": "alice"}, "forcedTags": ["tag:server"], "validTags": ["tag:web"], "ipAddresses": ["100.64.0.1"]}]}`
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/node" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	nodes, err := client.FetchNodes(context.Background())
	if err != nil {
		t.Fatalf("FetchNodes returned error: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.ID != "1" || n.Name != "node1" {
		t.Errorf("node = %+v, want ID=1 Name=node1", n)
	}
	if n.User.Name != "alice" {
		t.Errorf("node.User.Name = %q, want alice", n.User.Name)
	}
	if len(n.ForcedTags) != 1 || n.ForcedTags[0] != "tag:server" {
		t.Errorf("ForcedTags = %v, want [tag:server]", n.ForcedTags)
	}
	if len(n.ValidTags) != 1 || n.ValidTags[0] != "tag:web" {
		t.Errorf("ValidTags = %v, want [tag:web]", n.ValidTags)
	}
	if len(n.IPAddresses) != 1 || n.IPAddresses[0] != "100.64.0.1" {
		t.Errorf("IPAddresses = %v, want [100.64.0.1]", n.IPAddresses)
	}
}

func TestHeadscaleClient_NonOK(t *testing.T) {
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal boom"))
	})

	_, err := client.FetchPolicy(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "500") {
		t.Errorf("error %q does not contain status code 500", msg)
	}
	if !strings.Contains(msg, "internal boom") {
		t.Errorf("error %q does not contain response body excerpt", msg)
	}
}

func TestHeadscaleClient_AuthHeader(t *testing.T) {
	var gotAuth string
	_, client := newHeadscaleTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users": []}`))
	})

	if _, err := client.FetchUsers(context.Background()); err != nil {
		t.Fatalf("FetchUsers returned error: %v", err)
	}
	if want := "Bearer test-api-key"; gotAuth != want {
		t.Errorf("Authorization header = %q, want %q", gotAuth, want)
	}
}
