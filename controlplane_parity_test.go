package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestCrossProviderMatcherParity(t *testing.T) {
	policyDocument := `{"groups":{"group:admin":["alice@example.com"]},"acls":[{"action":"accept","src":["group:admin"],"dst":["tag:app:443"]}]}`
	headscaleMux := http.NewServeMux()
	headscaleMux.HandleFunc("/api/v1/policy", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"policy": policyDocument})
	})
	headscaleMux.HandleFunc("/api/v1/node", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"nodes": []map[string]any{{
			"id": "device-1", "name": "app.tailnet.ts.net", "user": map[string]any{"id": "user-1", "name": "alice@example.com"},
			"tags": []string{"tag:app"}, "ipAddresses": []string{"100.64.0.10"},
		}}})
	})
	headscaleServer := httptest.NewServer(headscaleMux)
	defer headscaleServer.Close()
	headscale := NewHeadscaleClient(headscaleServer.URL, "headscale-key", headscaleServer.Client())
	headscaleResult, err := headscale.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Headscale Load() error = %v", err)
	}

	tailscaleMux := http.NewServeMux()
	tailscaleMux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "token", "expires_in": 3600})
	})
	tailscaleMux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		var policy any
		if err := json.Unmarshal([]byte(policyDocument), &policy); err != nil {
			t.Fatalf("Unmarshal policy fixture: %v", err)
		}
		writeJSON(t, w, policy)
	})
	tailscaleMux.HandleFunc("/tailnet/-/users", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"users": []map[string]any{{"id": "user-1", "loginName": "alice@example.com", "type": "member", "role": "member"}}})
	})
	tailscaleMux.HandleFunc("/tailnet/-/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"devices": []map[string]any{{
			"id": "device-1", "name": "app.tailnet.ts.net", "user": "user-1",
			"tags": []string{"tag:app"}, "addresses": []string{"100.64.0.10"},
		}}})
	})
	tailscaleServer := httptest.NewServer(tailscaleMux)
	defer tailscaleServer.Close()
	tailscale := newTailscaleClientForTest(tailscaleServer.URL, "client", "secret", tailscaleServer.Client(), nil)
	tailscaleResult, err := tailscale.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Tailscale Load() error = %v", err)
	}

	proxyHosts := []ProxyHost{{ID: 1, DomainNames: []string{"app.example.com"}, ForwardScheme: "https", ForwardHost: "100.64.0.10", Enabled: true, Meta: ProxyHostMeta{NginxOnline: true}}}
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		headscaleData := &CacheData{Policy: headscaleResult.Policy, Nodes: headscaleResult.Nodes, ProxyHosts: proxyHosts}
		tailscaleData := &CacheData{Policy: tailscaleResult.Policy, Nodes: tailscaleResult.Nodes, ProxyHosts: proxyHosts}
		headscaleMatches := evaluateServices(&Identity{Login: login}, headscaleData)
		tailscaleMatches := evaluateServices(&Identity{Login: login}, tailscaleData)
		if !reflect.DeepEqual(headscaleMatches, tailscaleMatches) {
			t.Fatalf("%s parity mismatch:\nHeadscale: %#v\nTailscale: %#v", login, headscaleMatches, tailscaleMatches)
		}
	}
}

func TestLegacyACLAndSafeGrantMatcherParity(t *testing.T) {
	legacy, err := validatePolicyDocument([]byte(`{"groups":{"group:admin":["alice@example.com"]},"acls":[{"action":"accept","src":["group:admin"],"dst":["tag:app:443"]}]}`))
	if err != nil {
		t.Fatalf("legacy policy error = %v", err)
	}
	modern, err := validatePolicyDocument([]byte(`{"groups":{"group:admin":["alice@example.com"]},"grants":[{"src":["group:admin"],"dst":["tag:app"],"ip":["tcp:443"]}],"nodeAttrs":[{"target":["autogroup:member"],"attr":["funnel"]}]}`))
	if err != nil {
		t.Fatalf("modern policy error = %v", err)
	}
	nodes := []Node{{Tags: []string{"tag:app"}, Addresses: []string{"100.64.0.10"}}}
	proxyHosts := []ProxyHost{{ID: 1, DomainNames: []string{"app.example.com"}, ForwardScheme: "https", ForwardHost: "100.64.0.10", ForwardPort: 443, Enabled: true}}
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		legacyMatches := MatchServices(&Identity{Login: login}, &CacheData{Policy: legacy.Policy, Nodes: nodes, ProxyHosts: proxyHosts})
		modernMatches := MatchServices(&Identity{Login: login}, &CacheData{Policy: modern.Policy, Nodes: nodes, ProxyHosts: proxyHosts})
		if !reflect.DeepEqual(legacyMatches, modernMatches) {
			t.Fatalf("%s parity mismatch:\nlegacy=%#v\nmodern=%#v", login, legacyMatches, modernMatches)
		}
	}
}
