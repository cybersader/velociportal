package main

import (
	"bytes"
	"log/slog"
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
	return newTestHandlerWithHealth(data, nil)
}

func newTestHandlerWithHealth(data *CacheData, health *ServiceHealthStore) http.Handler {
	_, trusted, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		panic(err)
	}
	return IdentityMiddleware(trusted, NewPortalHandlerWithHealth(newTestCache(data), health))
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

func TestPortalRequestLogsDoNotExposeIdentityOrTopology(t *testing.T) {
	original := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	const login = "identity-log-canary@example.com"
	data := standardTestData()
	handler := newTestHandler(data)
	response := doPortalRequest(handler, "127.0.0.1:12345", login)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	logs := output.String()
	for _, sensitive := range []string{login, "grafana.example.com", "10.0.0.1"} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("logs exposed %q: %s", sensitive, logs)
		}
	}
	if !strings.Contains(logs, `"msg":"portal request"`) || !strings.Contains(logs, `"cards":`) {
		t.Fatalf("logs omitted useful request metadata: %s", logs)
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

func TestPortalHandler_LegacyCardsRemainFlatAndAlphabetical(t *testing.T) {
	rec := doPortalRequest(
		newTestHandler(standardTestData()),
		"127.0.0.1:12345",
		"alice@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `<div class="grid" id="services"`) {
		t.Fatal("legacy cards should keep the flat services grid")
	}
	if strings.Contains(body, `class="service-category"`) {
		t.Fatal("legacy cards should not gain category sections")
	}
	grafana := strings.Index(body, `data-service="grafana.example.com"`)
	wiki := strings.Index(body, `data-service="wiki.example.com"`)
	if grafana < 0 || wiki < 0 || grafana >= wiki {
		t.Fatalf("legacy card order was not alphabetical: grafana=%d wiki=%d", grafana, wiki)
	}
}

func TestPortalHandler_OrganizationSectionsAreAccessibleAndDeterministic(t *testing.T) {
	data := standardTestData()
	zero, five := 0, 5
	data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{
		1: {Name: "Grafana", Category: "Admin & Ops", Order: &five},
		3: {Name: "Wiki", Order: &zero},
	}}

	rec := doPortalRequest(
		newTestHandler(data),
		"127.0.0.1:12345",
		"alice@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, expected := range []string{
		`<div class="services-organized" id="services"`,
		`<section class="service-category" aria-labelledby="service-category-1">`,
		`<h2 class="service-category-title" id="service-category-1">Admin &amp; Ops</h2>`,
		`<h2 class="service-category-title" id="service-category-2">Uncategorized</h2>`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("organized portal omitted %q", expected)
		}
	}
	admin := strings.Index(body, `>Admin &amp; Ops</h2>`)
	grafana := strings.Index(body, `data-service="Grafana"`)
	uncategorized := strings.Index(body, `>Uncategorized</h2>`)
	wiki := strings.Index(body, `data-service="Wiki"`)
	if admin < 0 || grafana < admin || uncategorized < grafana || wiki < uncategorized {
		t.Fatalf("organized section order was not deterministic: admin=%d grafana=%d uncategorized=%d wiki=%d", admin, grafana, uncategorized, wiki)
	}
}

func TestPortalHandler_OrganizationModeUsesCompleteMetadata(t *testing.T) {
	data := standardTestData()
	data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{
		1: {Category: "Operations"},
	}}

	rec := doPortalRequest(
		newTestHandler(data),
		"127.0.0.1:12345",
		"bob@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<div class="services-organized" id="services"`) ||
		!strings.Contains(body, `>Uncategorized</h2>`) {
		t.Fatal("complete organization metadata should place a viewer's unclassified cards in an uncategorized section")
	}
	if strings.Contains(body, `>Operations</h2>`) || strings.Contains(body, "grafana.example.com") {
		t.Fatal("organization metadata changed card authorization")
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

func TestPortalHandler_FullDomainIdentitiesDoNotCollide(t *testing.T) {
	data := &CacheData{
		Policy: &Policy{
			Groups: map[string][]string{
				"group:example": {"alice@example.com"},
				"group:other":   {"alice@other.example"},
			},
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"group:example"}, Dst: []string{"10.0.0.10:*"}},
				{Action: "accept", Src: []string{"group:other"}, Dst: []string{"10.0.0.11:*"}},
			},
		},
		ProxyHosts: []ProxyHost{
			{ID: 10, DomainNames: []string{"example-service.test"}, ForwardHost: "10.0.0.10", Enabled: true},
			{ID: 11, DomainNames: []string{"other-service.test"}, ForwardHost: "10.0.0.11", Enabled: true},
		},
		UpdatedAt: time.Now(),
	}
	h := newTestHandler(data)

	tests := []struct {
		login      string
		visible    string
		notVisible string
	}{
		{login: "alice@example.com", visible: "example-service.test", notVisible: "other-service.test"},
		{login: "alice@other.example", visible: "other-service.test", notVisible: "example-service.test"},
	}
	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			rec := doPortalRequest(h, "127.0.0.1:12345", tt.login)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tt.visible) {
				t.Errorf("%s should see %s", tt.login, tt.visible)
			}
			if strings.Contains(body, tt.notVisible) {
				t.Errorf("%s must not see %s", tt.login, tt.notVisible)
			}
		})
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

func TestPortalHandler_BlankLoginRendersNoServices(t *testing.T) {
	h := newTestHandler(standardTestData())
	// Whitespace reaches the matcher because middleware only checks an empty header;
	// matcher identity handling must still fail closed and render no wildcard cards.
	rec := doPortalRequest(h, "127.0.0.1:12345", "   ")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a present header, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, service := range []string{"grafana.example.com", "jenkins.example.com", "wiki.example.com"} {
		if strings.Contains(body, service) {
			t.Errorf("blank login must not render %s", service)
		}
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
	// A malicious NPM entry with a javascript: backend scheme remains visible as
	// an informational card but never becomes a clickable browser URL.
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
	if !strings.Contains(body, "evil.example.com") || !strings.Contains(body, "link needed") {
		t.Error("the disallowed-scheme card should remain visible and unlinked")
	}
	if strings.Contains(body, `<a class="card"`) {
		t.Error("no card anchor should be emitted for the disallowed-scheme host")
	}
	if strings.Contains(body, "data-online") || strings.Contains(body, "status-dot") {
		t.Error("NPM route state must not be rendered as backend health")
	}
}

func TestPortalHandler_WildcardDomainNeverBecomesLink(t *testing.T) {
	data := &CacheData{
		Policy: &Policy{
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.9:*"}},
			},
		},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"*.rader.wiki"}, ForwardScheme: "https", ForwardHost: "10.0.0.9", Enabled: true},
		},
		UpdatedAt: time.Now(),
	}

	rec := doPortalRequest(
		newTestHandler(data),
		"127.0.0.1:12345",
		"alice@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "*.rader.wiki") ||
		!strings.Contains(body, "link needed") {
		t.Fatal("wildcard card should remain visible and unlinked")
	}
	for _, forbidden := range []string{
		`href="https://%2A.rader.wiki/"`,
		`href="https://*.rader.wiki/"`,
		`<a class="card"`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("wildcard card emitted forbidden link markup %q", forbidden)
		}
	}
}

func TestPortalHandler_HealthJoinsOnlyAuthorizedCards(t *testing.T) {
	store := NewServiceHealthStore()
	store.publish(map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: time.Now()},
		2: {ProxyHostID: 2, State: ServiceHealthStateAuthRequired, CheckedAt: time.Now()},
		3: {ProxyHostID: 3, State: ServiceHealthStateStale, CheckedAt: time.Now()},
	}, time.Hour)

	data := standardTestData()
	order := 0
	data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{
		1: {Category: "Operations", Order: &order},
		2: {Category: "Must remain unauthorized", Order: &order},
	}}
	rec := doPortalRequest(
		newTestHandlerWithHealth(data, store),
		"127.0.0.1:12345",
		"alice@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`aria-label="Service health: reachable"`,
		`aria-label="Service health: stale"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("authorized health status %q was not rendered", expected)
		}
	}
	if strings.Contains(body, `aria-label="Service health: authentication required"`) {
		t.Fatal("health for an unauthorized card was rendered")
	}
	if strings.Contains(body, "jenkins.example.com") {
		t.Fatal("health integration changed card authorization")
	}
}

func TestRenderServiceHealthStatusLabels(t *testing.T) {
	tests := []struct {
		state ServiceHealthState
		label string
		class string
	}{
		{ServiceHealthStateUnknown, "unknown", "health-unknown"},
		{ServiceHealthStateReachable, "reachable", "health-reachable"},
		{ServiceHealthStateAuthRequired, "authentication required", "health-auth-required"},
		{ServiceHealthStateResponseError, "response error", "health-response-error"},
		{ServiceHealthStateUnreachable, "unreachable", "health-unreachable"},
		{ServiceHealthStateStale, "stale", "health-stale"},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			store := NewServiceHealthStore()
			store.publish(map[int]ServiceHealthResult{
				1: {ProxyHostID: 1, State: test.state, CheckedAt: time.Now()},
			}, time.Hour)
			markup := renderServiceHealthStatus(store, 1)
			if !strings.Contains(markup, test.label) || !strings.Contains(markup, test.class) {
				t.Fatalf("markup = %q", markup)
			}
		})
	}
	if markup := renderServiceHealthStatus(NewServiceHealthStore(), 1); markup != "" {
		t.Fatalf("unconfigured health markup = %q", markup)
	}
}

func TestPortalHandler_MachinesRenderAsAccessibleNonLinkablePolicyCards(t *testing.T) {
	data := machineMatcherFixture(t)
	data.Policy.SSH.Rules = []SSHRule{
		{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{machineNonrootSelector, "deploy", "root"}},
		{Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{"deploy"}, CheckPeriod: 12 * time.Hour},
	}

	rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, expected := range []string{
		`<section class="portal-section" aria-labelledby="services-heading">`,
		`<section class="portal-section" aria-labelledby="machines-heading">`,
		`<h2 class="section-title" id="machines-heading">Machines</h2>`,
		`<article class="card machine-card" data-machine="server.tailnet.ts.net" aria-labelledby="machine-1-name">`,
		`Policy allows SSH access to this machine.`,
		`<span class="machine-user-summary">any non-root account</span><span class="badge machine-action">accept</span>`,
		`<span class="machine-user-summary">deploy</span><span class="badge machine-action">check</span>`,
		`<label for="machine-1-user">SSH account</label>`,
		`<option value="deploy" data-command="tailscale ssh deploy@server.tailnet.ts.net">deploy</option>`,
		`<option value="root" data-command="tailscale ssh root@server.tailnet.ts.net">root</option>`,
		`data-copy-ssh-command data-user-select="machine-1-user" aria-describedby="machine-1-copy-feedback"`,
		`role="status" aria-live="polite"`,
		`document.addEventListener("click", function (event)`,
		`feedback.textContent = "Command copied."`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("machine portal omitted %q", expected)
		}
	}

	start := strings.Index(body, `<article class="card machine-card"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	for _, forbidden := range []string{`<a `, `href=`, `ssh://`, machineNonrootSelector, `reachable`, `health`, `NPM`} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("machine article included forbidden claim or control %q: %s", forbidden, article)
		}
	}
}

func TestPortalHandler_MachineSectionTracksProjectionAvailability(t *testing.T) {
	tests := []struct {
		name      string
		provider  controlPlaneProvider
		sshState  sshPolicyState
		available bool
	}{
		{name: "Headscale with supported-shaped SSH", provider: controlPlaneHeadscale, sshState: sshPolicySupported},
		{name: "Tailscale with absent SSH", provider: controlPlaneTailscale, sshState: sshPolicyAbsent},
		{name: "Tailscale with unsupported SSH", provider: controlPlaneTailscale, sshState: sshPolicyUnsupported},
		{name: "Tailscale with supported SSH and zero matches", provider: controlPlaneTailscale, sshState: sshPolicySupported, available: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := standardTestData()
			data.ControlPlane.Provider = test.provider
			data.Policy.SSH = SSHPolicy{State: test.sshState}

			rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			hasSection := strings.Contains(body, `<section class="portal-section" aria-labelledby="machines-heading">`)
			hasEmptyState := strings.Contains(body, `No machines are available from the supported SSH policy view.`)
			if hasSection != test.available || hasEmptyState != test.available {
				t.Fatalf("machine projection available=%t, section=%t, empty_state=%t", test.available, hasSection, hasEmptyState)
			}
		})
	}
}

func TestPortalHandler_NonrootPolicyDoesNotInventCommandAccount(t *testing.T) {
	data := machineMatcherFixture(t)
	data.Policy.SSH.Rules = []SSHRule{{
		Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{machineNonrootSelector},
	}}

	rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	start := strings.Index(body, `<article class="card machine-card"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	if !strings.Contains(article, `any non-root account`) || !strings.Contains(article, `>check</span>`) {
		t.Fatalf("nonroot policy summary missing: %s", article)
	}
	for _, forbidden := range []string{`<select`, `data-copy-ssh-command`, `data-command=`, machineNonrootSelector} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("nonroot-only policy invented a copyable account via %q: %s", forbidden, article)
		}
	}
}

func TestMachineSSHCommandRequiresValidatedLiteralInputs(t *testing.T) {
	if got, ok := machineSSHCommand("deploy", "server.tailnet.ts.net"); !ok || got != "tailscale ssh deploy@server.tailnet.ts.net" {
		t.Fatalf("machineSSHCommand() = %q, %t", got, ok)
	}
	if got, ok := machineSSHCommand("machine$", "server.tailnet.ts.net"); !ok || got != `tailscale ssh machine\$@server.tailnet.ts.net` {
		t.Fatalf("machineSSHCommand() did not protect a literal trailing dollar: %q, %t", got, ok)
	}
	for _, test := range []struct{ user, target string }{
		{machineNonrootSelector, "server.tailnet.ts.net"},
		{"bad user", "server.tailnet.ts.net"},
		{"deploy", "server"},
		{"deploy", "public.example.com"},
		{"deploy", "192.168.1.10"},
	} {
		if command, ok := machineSSHCommand(test.user, test.target); ok || command != "" {
			t.Fatalf("machineSSHCommand(%q, %q) = %q, %t", test.user, test.target, command, ok)
		}
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

func TestPortalHandler_LocalLogoControlAndHTMXRefresh(t *testing.T) {
	rec := doPortalRequest(
		newTestHandler(standardTestData()),
		"127.0.0.1:12345",
		"alice@example.com",
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	for _, expected := range []string{
		`id="logo-toggle" type="button" aria-controls="brand-logo" aria-pressed="true" hidden`,
		`window.localStorage.getItem(storageKey)`,
		`window.localStorage.setItem(storageKey, String(visible))`,
		`} catch (_) {`,
		`id="portal-content" hx-get="/portal" hx-trigger="every 60s" hx-target="#portal-content" hx-select="#portal-content" hx-swap="outerHTML"`,
		`.grid { grid-template-columns: minmax(0, 1fr); }`,
		`.card-head { align-items: flex-start; flex-wrap: wrap; }`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("portal omitted guarded preference or refresh markup %q", expected)
		}
	}
	if strings.Contains(body, `hx-select="#services`) || strings.Contains(body, `hx-target="#services`) || strings.Contains(body, `hx-swap="innerHTML"`) {
		t.Fatal("portal refresh must replace the complete services-and-machines region")
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
