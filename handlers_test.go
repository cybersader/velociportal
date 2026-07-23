package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestCache builds a *Cache whose in-memory data pointer is pre-loaded with
// the supplied CacheData. It bypasses the real Headscale/NPM clients entirely by
// writing directly to the unexported atomic.Pointer — safe because the test lives
// in package main. Passing nil leaves the pointer empty so Cache.Get() returns nil.
func newTestCache(data *CacheData) *Cache {
	c := &Cache{}
	if data != nil {
		c.data.Store(data)
	}
	return c
}

// newTestHandler wraps a PortalHandler (fed the given cache data) in the real
// IdentityMiddleware, trusting only 127.0.0.0/8 — the exact production wiring.
func newTestHandler(data *CacheData) http.Handler {
	_, trusted, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		panic(err)
	}
	return IdentityMiddleware(trusted, NewPortalHandler(newTestCache(data)))
}

// doPortalRequest drives a single request through the middleware+handler stack.
// A non-empty login sets the trusted-proxy identity header; an empty login omits it.
func doPortalRequest(h http.Handler, remoteAddr, login string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/portal", nil)
	req.RemoteAddr = remoteAddr
	if login != "" {
		req.Header.Set("Tailscale-User-Login", login)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// standardTestData is the shared fixture for the happy-path request-flow tests.
//
//	group:admin (alice@example.com) -> 10.0.0.1 (grafana) + 10.0.0.3 (wiki)
//	group:dev   (bob@example.com)   -> 10.0.0.2 (jenkins)
//	src "*"                          -> 10.0.0.3 (wiki), visible to everyone
//
// Proxy hosts: grafana/jenkins/wiki enabled; a disabled host that must never render.
func standardTestData() *CacheData {
	return &CacheData{
		Policy: &Policy{
			Groups: map[string][]string{
				"group:admin": {"alice@example.com"},
				"group:dev":   {"bob@example.com"},
			},
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"10.0.0.1:*", "10.0.0.3:*"}},
				{Action: "accept", Src: []string{"group:dev"}, Dst: []string{"10.0.0.2:*"}},
				{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.3:*"}},
			},
		},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"grafana.example.com"}, ForwardScheme: "http", ForwardHost: "10.0.0.1", ForwardPort: 3000, Enabled: true, Meta: ProxyHostMeta{NginxOnline: true}},
			{ID: 2, DomainNames: []string{"jenkins.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.2", ForwardPort: 8080, Enabled: true, Meta: ProxyHostMeta{NginxOnline: true}},
			{ID: 3, DomainNames: []string{"wiki.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.3", ForwardPort: 443, Enabled: true, Meta: ProxyHostMeta{NginxOnline: false}},
			{ID: 4, DomainNames: []string{"disabled.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.1", ForwardPort: 443, Enabled: false},
		},
		UpdatedAt: time.Now(),
	}
}

func TestPortalHandler_AdminUser(t *testing.T) {
	h := newTestHandler(standardTestData())
	rec := doPortalRequest(h, "127.0.0.1:12345", "alice@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected text/html Content-Type, got %q", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "grafana.example.com") {
		t.Error("admin should see grafana.example.com")
	}
	if !strings.Contains(body, "wiki.example.com") {
		t.Error("admin should see wiki.example.com")
	}
	if strings.Contains(body, "jenkins.example.com") {
		t.Error("admin must NOT see jenkins.example.com")
	}
	if strings.Contains(body, "disabled.example.com") {
		t.Error("disabled proxy host must never render")
	}
}

func TestPortalHandler_DevUser(t *testing.T) {
	h := newTestHandler(standardTestData())
	rec := doPortalRequest(h, "127.0.0.1:12345", "bob@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "jenkins.example.com") {
		t.Error("dev should see jenkins.example.com")
	}
	if !strings.Contains(body, "wiki.example.com") {
		t.Error("dev should see wiki.example.com (wildcard rule)")
	}
	if strings.Contains(body, "grafana.example.com") {
		t.Error("dev must NOT see grafana.example.com")
	}
}

func TestPortalHandler_UnknownUser(t *testing.T) {
	h := newTestHandler(standardTestData())
	rec := doPortalRequest(h, "127.0.0.1:12345", "nobody@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "wiki.example.com") {
		t.Error("unknown user should still see the wildcard wiki.example.com")
	}
	if strings.Contains(body, "grafana.example.com") {
		t.Error("unknown user must NOT see grafana.example.com")
	}
	if strings.Contains(body, "jenkins.example.com") {
		t.Error("unknown user must NOT see jenkins.example.com")
	}
}

func TestPortalHandler_NoIdentityHeader(t *testing.T) {
	h := newTestHandler(standardTestData())
	rec := doPortalRequest(h, "127.0.0.1:12345", "")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no identity header from trusted IP, got %d", rec.Code)
	}
}

func TestPortalHandler_UntrustedSource(t *testing.T) {
	h := newTestHandler(standardTestData())
	// Identity headers present, but the source IP is outside the trusted CIDR.
	rec := doPortalRequest(h, "192.168.1.1:12345", "alice@example.com")

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for untrusted source, got %d", rec.Code)
	}
	// The identity headers must never be honored from an untrusted path.
	if strings.Contains(rec.Body.String(), "grafana.example.com") {
		t.Error("untrusted source must not receive any rendered services")
	}
}

func TestPortalHandler_XSSEscaping(t *testing.T) {
	data := &CacheData{
		Policy: &Policy{
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.9:*"}},
			},
		},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"<script>alert(1)</script>.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.9", Enabled: true},
		},
		UpdatedAt: time.Now(),
	}

	h := newTestHandler(data)
	rec := doPortalRequest(h, "127.0.0.1:12345", "alice@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Error("HTML should contain the escaped script tag (&lt;script&gt;)")
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Error("HTML must NOT contain the raw, unescaped <script> tag")
	}
}

func TestPortalHandler_SchemeAllowlist(t *testing.T) {
	// A malicious NPM entry with a javascript: scheme. MatchServices builds
	// URL = "javascript://evil.example.com"; renderPortal's allowlist only emits
	// cards whose URL begins with http:// or https://, so this card is skipped.
	data := &CacheData{
		Policy: &Policy{
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.9:*"}},
			},
		},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"evil.example.com"}, ForwardScheme: "javascript", ForwardHost: "10.0.0.9", Enabled: true},
		},
		UpdatedAt: time.Now(),
	}

	h := newTestHandler(data)
	rec := doPortalRequest(h, "127.0.0.1:12345", "alice@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if strings.Contains(body, "javascript:") {
		t.Error("rendered HTML must not contain a javascript: URL scheme")
	}
	if strings.Contains(body, "evil.example.com") {
		t.Error("the disallowed-scheme card should be skipped entirely")
	}
	// The card is skipped at render time (renderPortal's allowlist), so no <a class="card">
	// anchor is emitted for it.
	if strings.Contains(body, `data-service="evil.example.com"`) {
		t.Error("no card anchor should be emitted for the disallowed-scheme host")
	}
}

func TestPortalHandler_EmptyCache(t *testing.T) {
	h := newTestHandler(nil) // Cache.Get() returns nil.
	rec := doPortalRequest(h, "127.0.0.1:12345", "alice@example.com")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for empty cache, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "portal unavailable") {
		t.Errorf("expected 'portal unavailable' body, got %q", rec.Body.String())
	}
}

func TestPortalHandler_FaviconInHTML(t *testing.T) {
	h := newTestHandler(standardTestData())
	rec := doPortalRequest(h, "127.0.0.1:12345", "alice@example.com")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "logo.svg") {
		t.Error("rendered HTML should contain the logo.svg favicon link")
	}
}
