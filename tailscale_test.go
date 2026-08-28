package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	tailscaleTestClientID     = "client-id"
	tailscaleTestClientSecret = "client secret&+-canary"
)

type tailscaleFixture struct {
	server    *httptest.Server
	client    *TailscaleClient
	now       time.Time
	tokenHits atomic.Int32
	mu        sync.Mutex
	paths     []string
	policy    any
}

func newTailscaleFixture(t *testing.T) *tailscaleFixture {
	t.Helper()
	fixture := &tailscaleFixture{
		now: time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC),
		policy: map[string]any{
			"groups": map[string][]string{"group:admin": {"alice@example.com"}},
			"acls":   []map[string]any{{"action": "accept", "src": []string{"group:admin"}, "dst": []string{"tag:app:443"}}},
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		fixture.tokenHits.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("token method = %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm() error = %v", err)
		}
		if r.Form.Get("grant_type") != "client_credentials" {
			t.Errorf("OAuth grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("client_id") != tailscaleTestClientID || r.Form.Get("client_secret") != tailscaleTestClientSecret {
			t.Errorf("OAuth credentials = %q / %q", r.Form.Get("client_id"), r.Form.Get("client_secret"))
		}
		if got, want := strings.Fields(r.Form.Get("scope")), tailscaleReadScopes; !reflect.DeepEqual(got, want) {
			t.Errorf("OAuth scopes = %v, want %v", got, want)
		}
		writeJSON(t, w, map[string]any{"access_token": "access-token-canary", "token_type": "Bearer", "expires_in": 3600})
	})
	mux.HandleFunc("/api/v2/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		fixture.recordPath(r)
		writeJSON(t, w, fixture.policy)
	})
	mux.HandleFunc("/api/v2/tailnet/-/users", func(w http.ResponseWriter, r *http.Request) {
		fixture.recordPath(r)
		writeJSON(t, w, map[string]any{"users": []map[string]any{{"id": "user-1", "loginName": "alice@example.com"}}})
	})
	mux.HandleFunc("/api/v2/tailnet/-/devices", func(w http.ResponseWriter, r *http.Request) {
		fixture.recordPath(r)
		writeJSON(t, w, map[string]any{"devices": []map[string]any{{
			"id": "device-1", "name": "app.tailnet.ts.net", "user": "user-1",
			"addresses": []string{"100.64.0.10", "100.64.0.10"},
		}}})
	})
	fixture.server = httptest.NewServer(mux)
	t.Cleanup(fixture.server.Close)
	fixture.client = newTailscaleClientForTest(fixture.server.URL+"/api/v2", tailscaleTestClientID, tailscaleTestClientSecret, fixture.server.Client(), func() time.Time { return fixture.now })
	return fixture
}

func (f *tailscaleFixture) recordPath(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("Encode() error = %v", err)
	}
}

func TestTailscaleLoadUsesApprovedEndpointsAndMapsOwners(t *testing.T) {
	fixture := newTailscaleFixture(t)
	var progress []controlPlaneLoadStage
	result, err := fixture.client.Load(context.Background(), func(stage controlPlaneLoadStage, count int) {
		progress = append(progress, stage)
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.Metadata.Provider != controlPlaneTailscale || result.Metadata.SupportLevel != controlPlanePreview || result.Metadata.PolicyMode != legacyACLVisibilityV1 {
		t.Fatalf("metadata = %#v", result.Metadata)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("nodes = %#v", result.Nodes)
	}
	node := result.Nodes[0]
	if node.ID != "device-1" || node.OwnerLogin != "alice@example.com" || len(node.Tags) != 0 || !reflect.DeepEqual(node.Addresses, []string{"100.64.0.10"}) {
		t.Fatalf("node = %#v", node)
	}
	if got, want := progress, []controlPlaneLoadStage{controlPlaneStageAuth, controlPlaneStagePolicy, controlPlaneStageUsers, controlPlaneStageDevices}; !reflect.DeepEqual(got, want) {
		t.Fatalf("progress = %v, want %v", got, want)
	}
	fixture.mu.Lock()
	paths := append([]string(nil), fixture.paths...)
	fixture.mu.Unlock()
	wantPaths := []string{"/api/v2/tailnet/-/acl", "/api/v2/tailnet/-/users", "/api/v2/tailnet/-/devices"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("paths = %v, want %v", paths, wantPaths)
	}
	if fixture.tokenHits.Load() != 1 {
		t.Fatalf("token requests = %d, want 1", fixture.tokenHits.Load())
	}
}

func TestTailscaleLoadSupportsSafeGrantsAndNodeAttrs(t *testing.T) {
	fixture := newTailscaleFixture(t)
	fixture.policy = map[string]any{
		"groups": map[string][]string{"group:admin": {"alice@example.com"}},
		"grants": []map[string]any{
			{"src": []string{"group:admin"}, "dst": []string{"100.64.0.10"}, "ip": []string{"tcp:443"}},
			{"src": []string{"tag:client"}, "dst": []string{"tag:server"}, "ip": []string{"*"}},
		},
		"nodeAttrs": []map[string]any{{"target": []string{"autogroup:member"}, "attr": []string{"funnel"}}},
	}

	result, err := fixture.client.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if result.Metadata.PolicyMode != networkAccessVisibilityV1 || len(result.Policy.Grants) != 2 || result.Policy.accessRuleCount() != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestTailscaleTokenReuseAndEarlyRefresh(t *testing.T) {
	fixture := newTailscaleFixture(t)
	if _, err := fixture.client.Load(context.Background(), nil); err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	if _, err := fixture.client.Load(context.Background(), nil); err != nil {
		t.Fatalf("second Load() error = %v", err)
	}
	if fixture.tokenHits.Load() != 1 {
		t.Fatalf("token requests after reuse = %d, want 1", fixture.tokenHits.Load())
	}
	fixture.now = fixture.now.Add(56 * time.Minute)
	if _, err := fixture.client.Load(context.Background(), nil); err != nil {
		t.Fatalf("refresh Load() error = %v", err)
	}
	if fixture.tokenHits.Load() != 2 {
		t.Fatalf("token requests after early refresh = %d, want 2", fixture.tokenHits.Load())
	}
}

func TestTailscaleConcurrentTokenRefreshIsCoalesced(t *testing.T) {
	fixture := newTailscaleFixture(t)
	const callers = 12
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wait sync.WaitGroup
	for i := 0; i < callers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := fixture.client.token(context.Background(), "")
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("token() error = %v", err)
		}
	}
	if fixture.tokenHits.Load() != 1 {
		t.Fatalf("token requests = %d, want 1", fixture.tokenHits.Load())
	}
}

func TestTailscaleRetriesOnceAfterUnauthorized(t *testing.T) {
	var tokenHits atomic.Int32
	var policyHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		hit := tokenHits.Add(1)
		writeJSON(t, w, map[string]any{"access_token": "token-" + string(rune('0'+hit)), "expires_in": 3600})
	})
	mux.HandleFunc("/api/v2/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		policyHits.Add(1)
		if r.Header.Get("Authorization") == "Bearer token-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"acls": []any{}})
	})
	mux.HandleFunc("/api/v2/tailnet/-/users", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"users": []map[string]any{{"id": "1", "loginName": "alice@example.com"}}})
	})
	mux.HandleFunc("/api/v2/tailnet/-/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"devices": []any{}})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := newTailscaleClientForTest(server.URL+"/api/v2", "id", "secret", server.Client(), time.Now)

	if _, err := client.Load(context.Background(), nil); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if tokenHits.Load() != 2 || policyHits.Load() != 2 {
		t.Fatalf("token hits = %d, policy hits = %d; want 2, 2", tokenHits.Load(), policyHits.Load())
	}
}

func TestTailscaleLoadPreservesTypedStageAfterRedaction(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "private-access-token", "expires_in": 3600})
	})
	mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"token":"private-access-token"}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := newTailscaleClientForTest(server.URL, "private-client-id", "private-client-secret", server.Client(), time.Now)

	_, err := client.Load(context.Background(), nil)
	var loadErr *controlPlaneLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load() error type = %T, want *controlPlaneLoadError: %v", err, err)
	}
	if loadErr.Provider != controlPlaneTailscale || loadErr.Stage != controlPlaneStagePolicy {
		t.Fatalf("load error = %#v", loadErr)
	}
	for _, secret := range []string{"private-client-id", "private-client-secret", "private-access-token"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Load() error exposed %q: %v", secret, err)
		}
	}
}

func TestTailscaleLoadPreservesUnsupportedPolicyClassification(t *testing.T) {
	const selector = "svc:secret-internal-app"
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "private-access-token", "expires_in": 3600})
	})
	mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"grants": []map[string]any{{"src": []string{"*"}, "dst": []string{selector}, "ip": []string{"*"}}},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := newTailscaleClientForTest(server.URL, "private-client-id", "private-client-secret", server.Client(), time.Now)

	_, err := client.Load(context.Background(), nil)
	var unsupported *unsupportedPolicyError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Load() error type = %T, want wrapped *unsupportedPolicyError: %v", err, err)
	}
	summary := sanitizeValidationRuntimeError(err, validationPrivacySummary, nil)
	if strings.Contains(summary, selector) || summary != `selected control-plane policy section "grants" uses unsupported access-control semantics` {
		t.Fatalf("summary error = %q", summary)
	}
}

func TestTailscaleRejectsInvalidOwnerMappings(t *testing.T) {
	tests := map[string]struct {
		users   string
		devices string
		want    string
	}{
		"blank login": {
			users:   `{"users":[{"id":"1","loginName":" "}]}`,
			devices: `{"devices":[]}`,
			want:    "blank loginName",
		},
		"duplicate login": {
			users:   `{"users":[{"id":"1","loginName":"alice@example.com"},{"id":"2","loginName":"alice@example.com"}]}`,
			devices: `{"devices":[]}`,
			want:    "duplicate loginName",
		},
		"unresolved owner": {
			users:   `{"users":[{"id":"1","loginName":"alice@example.com"}]}`,
			devices: `{"devices":[{"id":"d1","user":"missing"}]}`,
			want:    "does not resolve",
		},
		"ambiguous owner": {
			users:   `{"users":[{"id":"alice@example.com","loginName":"bob@example.com"},{"id":"2","loginName":"alice@example.com"}]}`,
			devices: `{"devices":[{"id":"d1","user":"alice@example.com"}]}`,
			want:    "ambiguous",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client, closeServer := tailscaleClientWithBodies(t, test.users, test.devices, "")
			defer closeServer()
			_, err := client.Load(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTailscaleAllowsTaggedDeviceWithoutUser(t *testing.T) {
	client, closeServer := tailscaleClientWithBodies(t,
		`{"users":[{"id":"1","loginName":"alice@example.com"}]}`,
		`{"devices":[{"id":"tagged-1","name":"service","tags":["tag:server"],"addresses":["100.64.0.20"]}]}`,
		"")
	defer closeServer()

	result, err := client.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(result.Nodes) != 1 {
		t.Fatalf("nodes = %#v", result.Nodes)
	}
	node := result.Nodes[0]
	if node.OwnerLogin != "" || !reflect.DeepEqual(node.Tags, []string{"tag:server"}) || !reflect.DeepEqual(node.Addresses, []string{"100.64.0.20"}) {
		t.Fatalf("tagged node = %#v", node)
	}
}

func TestTailscaleIgnoresUserOnTaggedDevice(t *testing.T) {
	client, closeServer := tailscaleClientWithBodies(t,
		`{"users":[{"id":"1","loginName":"alice@example.com"}]}`,
		`{"devices":[{"id":"tagged-1","name":"service","user":"1","tags":["tag:server"],"addresses":["100.64.0.20"]}]}`,
		"")
	defer closeServer()

	result, err := client.Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(result.Nodes) != 1 || result.Nodes[0].OwnerLogin != "" {
		t.Fatalf("tagged node retained a human owner: %#v", result.Nodes)
	}
}

func TestTailscaleRejectsUntaggedDeviceWithoutUser(t *testing.T) {
	client, closeServer := tailscaleClientWithBodies(t,
		`{"users":[{"id":"1","loginName":"alice@example.com"}]}`,
		`{"devices":[{"id":"device-1","name":"unowned"}]}`,
		"")
	defer closeServer()

	_, err := client.Load(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "untagged device") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestTailscaleRejectsNullPolicyAndCollections(t *testing.T) {
	tests := map[string]struct {
		users   string
		devices string
		policy  string
		want    string
	}{
		"policy":  {users: `{"users":[]}`, devices: `{"devices":[]}`, policy: `null`, want: "expected a JSON object"},
		"users":   {users: `{"users":null}`, devices: `{"devices":[]}`, policy: `{"acls":[]}`, want: "users must be an array"},
		"devices": {users: `{"users":[]}`, devices: `{"devices":null}`, policy: `{"acls":[]}`, want: "devices must be an array"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			client, closeServer := tailscaleClientWithBodies(t, test.users, test.devices, test.policy)
			defer closeServer()
			_, err := client.Load(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTailscaleRejectsPartialResponses(t *testing.T) {
	for _, collection := range []string{"users", "devices"} {
		t.Run(collection, func(t *testing.T) {
			users := `{"users":[{"id":"1","loginName":"alice@example.com"}]}`
			devices := `{"devices":[]}`
			if collection == "users" {
				users = `{"users":[],"next":"cursor"}`
			} else {
				devices = `{"devices":[],"hasMore":true}`
			}
			client, closeServer := tailscaleClientWithBodies(t, users, devices, "")
			defer closeServer()
			_, err := client.Load(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), "paginated or partial") {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestTailscaleRejectsContentRange(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "token", "expires_in": 3600})
	})
	mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"acls":[]}`)
	})
	mux.HandleFunc("/tailnet/-/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "users 0-0/2")
		_, _ = io.WriteString(w, `{"users":[{"id":"1","loginName":"alice@example.com"}]}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := newTailscaleClientForTest(server.URL, "id", "secret", server.Client(), time.Now)

	_, err := client.Load(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "paginated or partial") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestTailscaleRedactsClientSecretAndAccessToken(t *testing.T) {
	t.Run("OAuth error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"client_id":"`+tailscaleTestClientID+`","secret":"`+url.QueryEscape(tailscaleTestClientSecret)+`"}`)
		}))
		defer server.Close()
		client := newTailscaleClientForTest(server.URL, "id", tailscaleTestClientSecret, server.Client(), time.Now)
		_, err := client.Load(context.Background(), nil)
		if err == nil || strings.Contains(err.Error(), tailscaleTestClientID) || strings.Contains(err.Error(), tailscaleTestClientSecret) || strings.Contains(err.Error(), url.QueryEscape(tailscaleTestClientSecret)) {
			t.Fatalf("Load() error exposed OAuth credential material: %v", err)
		}
	})

	t.Run("API error", func(t *testing.T) {
		const token = "access-token-secret-canary"
		mux := http.NewServeMux()
		mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(t, w, map[string]any{"access_token": token, "expires_in": 3600})
		})
		mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"token":"`+token+`"}`)
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		client := newTailscaleClientForTest(server.URL, "id", tailscaleTestClientSecret, server.Client(), time.Now)
		_, err := client.Load(context.Background(), nil)
		if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), tailscaleTestClientSecret) {
			t.Fatalf("Load() error exposed credentials: %v", err)
		}
	})

	t.Run("rejected and replacement tokens", func(t *testing.T) {
		const rejectedToken = "rejected-token-secret-canary"
		const replacementToken = "replacement-token-secret-canary"
		var tokenHits atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
			token := rejectedToken
			if tokenHits.Add(1) > 1 {
				token = replacementToken
			}
			writeJSON(t, w, map[string]any{"access_token": token, "expires_in": 3600})
		})
		mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer "+rejectedToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, rejectedToken+" "+replacementToken)
		})
		server := httptest.NewServer(mux)
		defer server.Close()
		client := newTailscaleClientForTest(server.URL, "id", tailscaleTestClientSecret, server.Client(), time.Now)
		_, err := client.Load(context.Background(), nil)
		if err == nil || strings.Contains(err.Error(), rejectedToken) || strings.Contains(err.Error(), replacementToken) {
			t.Fatalf("Load() error exposed old or current access token: %v", err)
		}
	})
}

func TestTailscaleProductionClientUsesFixedHardenedOrigin(t *testing.T) {
	client := NewTailscaleClient("id", "secret", nil)
	if client.baseURL != tailscaleAPIOrigin {
		t.Fatalf("baseURL = %q, want %q", client.baseURL, tailscaleAPIOrigin)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", client.httpClient.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("transport honors environment proxies")
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v", transport.TLSClientConfig)
	}
	if err := client.httpClient.CheckRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v", err)
	}
}

func TestTailscaleRequestTimeoutAndResponseBound(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(100 * time.Millisecond)
			writeJSON(t, w, map[string]any{"access_token": "token", "expires_in": 3600})
		}))
		defer server.Close()
		httpClient := server.Client()
		httpClient.Timeout = 20 * time.Millisecond
		client := newTailscaleClientForTest(server.URL, "id", "secret", httpClient, time.Now)
		if _, err := client.Load(context.Background(), nil); err == nil {
			t.Fatal("Load() error = nil")
		}
	})

	t.Run("body bound", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maxUpstreamResponseBody+1))
		}))
		defer server.Close()
		client := newTailscaleClientForTest(server.URL, "id", "secret", server.Client(), time.Now)
		_, err := client.Load(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), "response body exceeds") {
			t.Fatalf("Load() error = %v", err)
		}
	})
}

func tailscaleClientWithBodies(t *testing.T, users, devices, policy string) (*TailscaleClient, func()) {
	t.Helper()
	if policy == "" {
		policy = `{"acls":[]}`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"access_token": "token", "expires_in": 3600})
	})
	mux.HandleFunc("/tailnet/-/acl", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, policy) })
	mux.HandleFunc("/tailnet/-/users", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, users) })
	mux.HandleFunc("/tailnet/-/devices", func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, devices) })
	server := httptest.NewServer(mux)
	client := newTailscaleClientForTest(server.URL, "id", "secret", server.Client(), time.Now)
	return client, server.Close
}

func TestTailscaleOAuthFormEncoding(t *testing.T) {
	values := url.Values{}
	values.Set("scope", strings.Join(tailscaleReadScopes, " "))
	encoded := values.Encode()
	if strings.Contains(encoded, " ") || !strings.Contains(encoded, "+") {
		t.Fatalf("encoded scope = %q", encoded)
	}
}
