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
		`<div class="machine-list" id="machines">`,
		`<article class="machine-row" data-machine="server.tailnet.ts.net" aria-labelledby="machine-1-name">`,
		`<div class="machine-identity"><h3 class="machine-name" id="machine-1-name">server</h3>`,
		`<p class="machine-target">server.tailnet.ts.net</p>`,
		`<label class="machine-field-label" for="machine-1-user">SSH as</label>`,
		`<select id="machine-1-user" data-machine-account-select aria-controls="machine-1-custom-account" aria-expanded="false">`,
		`<option value="deploy" data-command="tailscale ssh deploy@server.tailnet.ts.net">deploy</option>`,
		`<option value="root" data-command="tailscale ssh root@server.tailnet.ts.net">root</option>`,
		`<option value="" data-custom-account="true">Other non-root account&hellip;</option>`,
		`<div class="machine-custom-account" id="machine-1-custom-account" hidden>`,
		`<input type="text" id="machine-1-account" class="machine-account-input" list="ssh-account-suggestions" autocomplete="off" spellcheck="false" maxlength="256" placeholder="Account name" data-account-target="server.tailnet.ts.net">`,
		`<button type="button" class="copy-command machine-copy" data-copy-machine-ssh aria-describedby="machine-1-copy-feedback" data-user-select="machine-1-user" data-account-input="machine-1-account">Copy SSH</button>`,
		`<details class="machine-policy-details"><summary>Policy details</summary>`,
		`Visible because SSH policy and a network Grant permit TCP port 22.`,
		`<li><span class="machine-account-name">Any non-root account</span><span class="machine-account-detail">No extra sign-in</span></li>`,
		`<li><span class="machine-account-name">deploy</span><span class="machine-account-detail">Reauthenticate every 12h</span></li>`,
		`Local account existence and machine health are not verified.`,
		`role="status" aria-live="polite"`,
		`var button = event.target.closest("[data-copy-machine-ssh]");`,
		`feedback.textContent = "Command copied."`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("machine portal omitted %q", expected)
		}
	}

	start := strings.Index(body, `<article class="machine-row"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	for _, forbidden := range []string{`<a `, `href=`, `ssh://`, machineNonrootSelector, `reachable`, `NPM`, `machine-user-summary`, `badge machine-action`, `<p class="machine-policy">`, `machine-accounts-heading`, `Copy command`} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("machine article included forbidden claim or control %q: %s", forbidden, article)
		}
	}
}

func TestPortalHandler_MixedMachineUsesSingleAccountWorkflow(t *testing.T) {
	data := machineMatcherFixture(t)
	data.Policy.SSH.Rules = []SSHRule{{
		Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{machineNonrootSelector, "deploy", "root"},
	}}

	rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	start := strings.Index(body, `<article class="machine-row"`)
	if start < 0 {
		t.Fatal("machine row was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine row was not closed")
	}
	article := body[start : start+end]
	for marker, want := range map[string]int{
		`data-machine-account-select`:              1,
		`class="machine-account-input"`:            1,
		`data-copy-machine-ssh`:                    1,
		`<details class="machine-policy-details">`: 1,
	} {
		if got := strings.Count(article, marker); got != want {
			t.Fatalf("%q count = %d, want %d: %s", marker, got, want, article)
		}
	}
	if strings.Contains(article, `<details class="machine-policy-details" open`) {
		t.Fatal("policy details must be collapsed by default")
	}
	for _, expected := range []string{
		`function syncMachineAccountControl(select, focusInput)`,
		`panel.hidden = !custom;`,
		`option.getAttribute("data-command")`,
		`if (custom) rememberAccount(customValue);`,
		`document.body.addEventListener("htmx:afterSwap", function ()`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("mixed workflow omitted script behavior %q", expected)
		}
	}
	for _, forbidden := range []string{`data-copy-ssh-account`, `data-copy-ssh-command`, `Copy command`} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("mixed workflow retained competing control %q: %s", forbidden, article)
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
	start := strings.Index(body, `<article class="machine-row"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	if !strings.Contains(article, `Any non-root account`) || !strings.Contains(article, `Reauthenticate every 12h (default)`) {
		t.Fatalf("nonroot policy summary missing: %s", article)
	}
	// The safe custom account input is allowed only because the viewer must type
	// and confirm the account themselves; it must never pre-fill or imply one.
	for _, expected := range []string{
		`<div class="machine-custom-account" id="machine-1-custom-account">`,
		`<label class="machine-field-label" for="machine-1-account">SSH as</label>`,
		`<input type="text" id="machine-1-account" class="machine-account-input" list="ssh-account-suggestions" autocomplete="off" spellcheck="false" maxlength="256" placeholder="Account name" data-account-target="server.tailnet.ts.net">`,
		`data-copy-machine-ssh aria-describedby="machine-1-copy-feedback" data-account-input="machine-1-account">Copy SSH</button>`,
		`<details class="machine-policy-details"><summary>Policy details</summary>`,
		`Local account existence and machine health are not verified.`,
	} {
		if !strings.Contains(article, expected) {
			t.Fatalf("nonroot-only card omitted safe custom account input %q: %s", expected, article)
		}
	}
	for _, forbidden := range []string{`<select`, `data-user-select=`, `data-command=`, ` hidden>`, machineNonrootSelector} {
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

func TestPortalHandler_LiteralOnlyMachineNeverRendersCustomAccountField(t *testing.T) {
	// The fixture's default SSH rule (deploy, root) carries no autogroup:nonroot
	// evidence at all, so the safe custom-account input must never appear.
	data := machineMatcherFixture(t)

	rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	start := strings.Index(body, `<article class="machine-row"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	for _, expected := range []string{
		`<label class="machine-field-label" for="machine-1-user">SSH as</label>`,
		`<select id="machine-1-user" data-machine-account-select>`,
		`<option value="deploy" data-command="tailscale ssh deploy@server.tailnet.ts.net">deploy</option>`,
		`<option value="root" data-command="tailscale ssh root@server.tailnet.ts.net">root</option>`,
		`data-copy-machine-ssh aria-describedby="machine-1-copy-feedback" data-user-select="machine-1-user">Copy SSH</button>`,
	} {
		if !strings.Contains(article, expected) {
			t.Fatalf("literal-only machine omitted %q: %s", expected, article)
		}
	}
	for _, forbidden := range []string{
		`machine-custom-account`,
		`data-account-input`,
		`data-account-target`,
		`data-custom-account`,
		`machine-account-input`,
	} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("literal-only machine rendered the nonroot-only custom account field via %q: %s", forbidden, article)
		}
	}
}

func TestMachineActionLabelAndCheckPeriodLabelAreTruthfulPlainLanguage(t *testing.T) {
	tests := map[string]struct {
		access MachineAccess
		want   string
	}{
		"accept means no extra sign-in": {
			access: MachineAccess{User: "deploy", Action: "accept"},
			want:   "No extra sign-in",
		},
		"check with no period falls back to Tailscale's 12h default": {
			access: MachineAccess{User: "deploy", Action: "check"},
			want:   "Reauthenticate every 12h (default)",
		},
		"check with an exact-hour period renders whole hours": {
			access: MachineAccess{User: "deploy", Action: "check", CheckPeriod: 2 * time.Hour},
			want:   "Reauthenticate every 2h",
		},
		"check with a sub-hour period renders minutes": {
			access: MachineAccess{User: "deploy", Action: "check", CheckPeriod: 30 * time.Minute},
			want:   "Reauthenticate every 30m",
		},
		"unexpected action never implies accept semantics": {
			access: MachineAccess{User: "deploy", Action: "unexpected"},
			want:   "SSH policy action unavailable",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := machineActionLabel(test.access); got != test.want {
				t.Fatalf("machineActionLabel(%#v) = %q, want %q", test.access, got, test.want)
			}
		})
	}
}

func TestMachineCardNamesSplitsShortAndFullNameOnlyForCanonicalTargets(t *testing.T) {
	tests := map[string]struct {
		target  string
		short   string
		full    string
		hasFull bool
	}{
		"canonical ts.net target splits into short and full": {
			target: "server.tailnet.ts.net", short: "server", full: "server.tailnet.ts.net", hasFull: true,
		},
		"validated IPv4 fallback has no separate full name": {
			target: "100.64.0.10", short: "100.64.0.10",
		},
		"validated IPv6 fallback has no separate full name": {
			target: "fd7a:115c:a1e0::13", short: "fd7a:115c:a1e0::13",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			short, full, hasFull := machineCardNames(test.target)
			if short != test.short || full != test.full || hasFull != test.hasFull {
				t.Fatalf("machineCardNames(%q) = %q, %q, %t; want %q, %q, %t", test.target, short, full, hasFull, test.short, test.full, test.hasFull)
			}
		})
	}
}

func TestPortalHandler_EligibleNonrootOnlyMachineShowsConsoleWithoutInventingCommand(t *testing.T) {
	data := machineMatcherFixture(t)
	data.Policy.SSH.Rules = []SSHRule{{
		Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{machineNonrootSelector},
	}}
	data.GrantRoleSelectorsByLogin["alice@example.com"] = []string{"autogroup:admin", "autogroup:member"}
	data.MachineSSHCapableByID = map[string]bool{"node-1": true}

	rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	start := strings.Index(body, `<article class="machine-row"`)
	if start < 0 {
		t.Fatal("machine article was not rendered")
	}
	end := strings.Index(body[start:], `</article>`)
	if end < 0 {
		t.Fatal("machine article was not closed")
	}
	article := body[start : start+end]
	if !strings.Contains(article, `aria-label="Open server in Tailscale Machines">Tailscale Machines</a>`) {
		t.Fatal("eligible nonroot-only machine must retain the browser console action")
	}
	for _, forbidden := range []string{`<select`, `data-user-select=`, `data-command=`} {
		if strings.Contains(article, forbidden) {
			t.Fatalf("nonroot-only policy invented a copyable account via %q", forbidden)
		}
	}
}

func TestRenderMachineConsoleLinkRevalidatesTarget(t *testing.T) {
	var body bytes.Buffer
	err := renderPortalWithOptions(&body, &Identity{Login: "owner@example.com"}, nil, portalRenderOptions{
		Machines: []MachineCard{{
			ID: "device-1", Name: "public.example.com", Target: "public.example.com",
			Access: []MachineAccess{{User: "deploy", Action: "accept"}},
		}},
		MachinesAvailable:         true,
		ConsoleEligible:           true,
		MachineConsoleCapableByID: map[string]bool{"device-1": true},
		LogoDefaultVisible:        true,
	})
	if err != nil {
		t.Fatalf("renderPortalWithOptions() error = %v", err)
	}
	if strings.Contains(body.String(), `console.tailscale.com`) || strings.Contains(body.String(), `aria-label="Open public.example.com in Tailscale Machines"`) {
		t.Fatal("rendering must reject an invalid machine target even for an eligible viewer")
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
		`id="account-trigger" type="button" aria-label="Account settings for alice@example.com" aria-haspopup="dialog" aria-expanded="false" aria-controls="account-panel" hidden`,
		`id="account-fallback"`,
		`id="account-panel" role="dialog" aria-labelledby="account-panel-title" hidden`,
		`id="logo-visible-checkbox"`,
		`Show Velociportal logo`,
		`var scopedKey = "velociportal.logo.visible." + scope;`,
		`var legacyKey = "velociportal.logo.visible";`,
		`window.localStorage.getItem(scopedKey)`,
		`window.localStorage.getItem(legacyKey)`,
		`window.localStorage.setItem(scopedKey, String(visible))`,
		`window.localStorage.removeItem(legacyKey)`,
		`} catch (_) {`,
		`closePanel(true);`,
		`<div class="user-name">alice@example.com</div>`,
		`id="portal-content" hx-get="/portal" hx-trigger="every 60s" hx-target="#portal-content" hx-select="#portal-content" hx-swap="outerHTML"`,
		`.grid { grid-template-columns: minmax(0, 1fr); }`,
		`.card-head { align-items: flex-start; flex-wrap: wrap; }`,
		`.machine-list { display: grid; gap: .65rem; }`,
		`.machine-row { display: grid; grid-template-columns: minmax(12rem, 1fr) minmax(16rem, 1.25fr) auto;`,
		`.machine-connect select, .machine-account-input { width: 100%; min-width: 12rem; min-height: 2.75rem;`,
		`.machine-policy-details { grid-area: details;`,
		`.machine-actions > .machine-copy, .machine-actions > .btn-console { flex: 1 1 10rem; }`,
		`body { padding-bottom: calc(4.75rem + env(safe-area-inset-bottom)); }`,
		`.bottom-nav-item { display: flex; flex: 1 1 0; min-width: 0; min-height: 44px;`,
		`<div class="account-panel-heading">SSH accounts</div>`,
		`<button type="button" class="btn-clear-accounts" id="clear-ssh-accounts-button" hidden>Clear saved SSH accounts</button>`,
		`id="clear-ssh-accounts-status" role="status" aria-live="polite"`,
		`<datalist id="ssh-account-suggestions"></datalist>`,
		`var ACCOUNTS_KEY = "velociportal.ssh.accounts." + identityScope;`,
		`var MAX_ACCOUNTS = 10;`,
		`var seen = Object.create(null);`,
		`customValue = input && input.value;`,
		`value !== "root"`,
		`status.textContent = cleared ? "Saved SSH accounts cleared." : "Saved SSH accounts could not be cleared.";`,
		`document.body.addEventListener("htmx:afterSwap", function ()`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("portal omitted guarded preference or refresh markup %q", expected)
		}
	}
	if strings.Contains(body, `hx-select="#services`) || strings.Contains(body, `hx-target="#services`) || strings.Contains(body, `hx-swap="innerHTML"`) {
		t.Fatal("portal refresh must replace the complete services-and-machines region")
	}
	if strings.Contains(body, `id="logo-toggle"`) {
		t.Fatal("the old standalone logo-toggle control must be fully replaced")
	}
	if strings.Contains(body, `input.value.trim()`) {
		t.Fatal("custom SSH accounts must reject surrounding whitespace rather than silently trimming it")
	}
	accountIndex := strings.Index(body, `id="account"`)
	portalIndex := strings.Index(body, `id="portal-content"`)
	if accountIndex < 0 || portalIndex < 0 || accountIndex > portalIndex {
		t.Fatal("account settings must remain outside the htmx-swapped portal content")
	}

	// The rendered scope must be the fixed-length, opaque SHA-256 hex digest of
	// the login -- never the plaintext login itself.
	wantScope := logoPreferenceScope("alice@example.com")
	if !strings.Contains(body, `var scope = "`+wantScope+`";`) {
		t.Fatalf("portal did not render the expected scoped preference key %q", wantScope)
	}
	if strings.Contains(body, "alice@example.com") == false {
		t.Fatal("sanity: fixture login should still appear as visible account text")
	}
	if strings.Count(body, wantScope) < 1 {
		t.Fatal("scoped storage key must be present")
	}
	if len(wantScope) != 64 {
		t.Fatalf("logoPreferenceScope must be a 64-character hex digest, got %d chars", len(wantScope))
	}
}

func TestPortalHandler_LogoPreferenceScopeIsOpaqueAndPerIdentity(t *testing.T) {
	aliceScope := logoPreferenceScope("alice@example.com")
	bobScope := logoPreferenceScope("bob@example.com")
	if aliceScope == bobScope {
		t.Fatal("distinct logins must scope to distinct preference keys")
	}
	for _, scope := range []string{aliceScope, bobScope} {
		if len(scope) != 64 {
			t.Fatalf("scope %q is not a 64-character hex digest", scope)
		}
		for _, r := range scope {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("scope %q contains a non-hex character", scope)
			}
		}
	}
}

func TestPortalHandler_LogoDefaultDeploymentSettingReachesRenderedPage(t *testing.T) {
	tests := map[string]struct {
		visible bool
		want    string
	}{
		"visible default": {visible: true, want: `var defaultVisible = "true" !== "false";`},
		"hidden default":  {visible: false, want: `var defaultVisible = "false" !== "false";`},
	}
	_, trusted, err := net.ParseCIDR("127.0.0.0/8")
	if err != nil {
		t.Fatalf("ParseCIDR() error = %v", err)
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			handler := IdentityMiddleware(trusted, NewPortalHandlerWithOptions(newTestCache(standardTestData()), nil, test.visible))
			rec := doPortalRequest(handler, "127.0.0.1:12345", "alice@example.com")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			if body := rec.Body.String(); !strings.Contains(body, test.want) {
				t.Fatalf("portal did not render deployment default %q", test.want)
			}
		})
	}
}

func TestPortalHandler_MachineConsoleLinkOnlyForEligibleTailscaleRoles(t *testing.T) {
	buildData := func(role string) *CacheData {
		data := machineMatcherFixture(t)
		data.Policy.SSH.Rules = []SSHRule{
			{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{"deploy"}},
		}
		roles := []string{"autogroup:member"}
		if role != "" {
			roles = append(roles, role)
		}
		data.GrantRoleSelectorsByLogin["alice@example.com"] = roles
		data.MachineSSHCapableByID = map[string]bool{"node-1": true}
		return data
	}

	t.Run("owner-eligible role renders the console link", func(t *testing.T) {
		rec := doPortalRequest(newTestHandler(buildData("autogroup:owner")), "127.0.0.1:12345", "alice@example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		for _, expected := range []string{
			`<a class="btn-console" href="https://console.tailscale.com/admin/machines?q=server+property%3Atailscale-ssh" target="_blank" rel="noopener noreferrer" aria-label="Open server in Tailscale Machines">Tailscale Machines</a>`,
			`Tailscale Machines opens the admin console, not a session.`,
		} {
			if !strings.Contains(body, expected) {
				t.Fatalf("eligible role did not render console link %q", expected)
			}
		}
	})

	t.Run("eligible role without device capability hides only the console link", func(t *testing.T) {
		data := buildData("autogroup:admin")
		data.MachineSSHCapableByID = nil
		rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `data-machine="server.tailnet.ts.net"`) || !strings.Contains(body, `tailscale ssh deploy@server.tailnet.ts.net`) {
			t.Fatal("missing capability metadata must not hide the machine card or copy command")
		}
		if strings.Contains(body, `<a class="btn-console"`) || strings.Contains(body, `aria-label="Open server in Tailscale Machines"`) {
			t.Fatal("missing capability metadata must fail closed for the console link")
		}
	})

	t.Run("plain member role hides the console link", func(t *testing.T) {
		rec := doPortalRequest(newTestHandler(buildData("")), "127.0.0.1:12345", "alice@example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		body := rec.Body.String()
		// The .btn-console CSS rule and shared section explanation are always
		// present, so assert against actual link-only markup and attributes.
		for _, forbidden := range []string{
			`<a class="btn-console"`,
			`console.tailscale.com`,
			`target="_blank"`,
			`rel="noopener noreferrer"`,
			`aria-label="Open server in Tailscale Machines"`,
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("ineligible role must not render console markup %q", forbidden)
			}
		}
	})

	t.Run("Headscale never renders the console link regardless of role", func(t *testing.T) {
		data := buildData("autogroup:owner")
		data.ControlPlane.Provider = controlPlaneHeadscale
		data.Policy.SSH.State = sshPolicySupported
		rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		// Headscale never exposes the Machines projection at all, so neither the
		// section nor the console link should render.
		body := rec.Body.String()
		if strings.Contains(body, `<a class="btn-console"`) {
			t.Fatal("Headscale must never render the browser SSH console action")
		}
	})
}

func TestPortalIdentityResponsesAreNotCacheable(t *testing.T) {
	tests := []struct {
		name string
		rec  *httptest.ResponseRecorder
		code int
	}{
		{name: "success", rec: doPortalRequest(newTestHandler(standardTestData()), "127.0.0.1:12345", "alice@example.com"), code: http.StatusOK},
		{name: "missing identity", rec: doPortalRequest(newTestHandler(standardTestData()), "127.0.0.1:12345", ""), code: http.StatusUnauthorized},
		{name: "unavailable snapshot", rec: doPortalRequest(newTestHandler(nil), "127.0.0.1:12345", "alice@example.com"), code: http.StatusServiceUnavailable},
		{name: "untrusted source", rec: doPortalRequest(newTestHandler(standardTestData()), "192.168.1.1:12345", "alice@example.com"), code: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.rec.Code != test.code {
				t.Fatalf("status = %d, want %d", test.rec.Code, test.code)
			}
			if got := test.rec.Header().Get("Cache-Control"); got != "no-store, max-age=0" {
				t.Fatalf("Cache-Control = %q", got)
			}
			if got := test.rec.Header().Get("Pragma"); got != "no-cache" {
				t.Fatalf("Pragma = %q", got)
			}
			vary := test.rec.Header().Get("Vary")
			for _, field := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
				if !strings.Contains(vary, field) {
					t.Fatalf("Vary = %q, missing %q", vary, field)
				}
			}
		})
	}
}

func TestPortalHandler_MobileBottomNavigationTracksMachineProjection(t *testing.T) {
	t.Run("unavailable projection hides Machines", func(t *testing.T) {
		rec := doPortalRequest(newTestHandler(standardTestData()), "127.0.0.1:12345", "alice@example.com")
		body := rec.Body.String()
		if !strings.Contains(body, `href="#services-heading" data-bottom-nav-scroll="services-heading"`) ||
			!strings.Contains(body, `id="bottom-nav-machines" href="#machines-heading" data-bottom-nav-scroll="machines-heading" hidden`) {
			t.Fatal("mobile navigation did not preserve hash fallbacks or hide unavailable Machines")
		}
	})

	t.Run("available zero-match projection keeps Machines", func(t *testing.T) {
		data := standardTestData()
		data.ControlPlane.Provider = controlPlaneTailscale
		data.Policy.SSH = SSHPolicy{State: sshPolicySupported}
		data.GrantRoleSelectorsByLogin = map[string][]string{"alice@example.com": {"autogroup:member"}}
		rec := doPortalRequest(newTestHandler(data), "127.0.0.1:12345", "alice@example.com")
		body := rec.Body.String()
		if !strings.Contains(body, `id="machines-heading"`) || !strings.Contains(body, `id="bottom-nav-machines" href="#machines-heading" data-bottom-nav-scroll="machines-heading"><`) {
			t.Fatal("available empty Machines projection must retain mobile navigation")
		}
	})

	rec := doPortalRequest(newTestHandler(machineMatcherFixture(t)), "127.0.0.1:12345", "alice@example.com")
	body := rec.Body.String()
	for _, expected := range []string{
		`<nav class="bottom-nav" id="bottom-nav" aria-label="Portal navigation">`,
		`id="bottom-nav-more" aria-haspopup="dialog" aria-controls="account-panel" aria-expanded="false"`,
		`document.body.addEventListener("htmx:afterSwap", syncBottomNavMachines);`,
		`target.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });`,
		`if (moreButton) moreButton.setAttribute("aria-expanded", String(expanded));`,
		`bottom: calc(4.5rem + env(safe-area-inset-bottom))`,
		`min-height: 44px`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("mobile portal shell omitted %q", expected)
		}
	}
	contentEnd := strings.Index(body, `</main>`)
	navStart := strings.Index(body, `<nav class="bottom-nav"`)
	if contentEnd < 0 || navStart < contentEnd {
		t.Fatal("bottom navigation must remain outside the htmx swap boundary")
	}
}

func TestPortalHandler_RendersPrivacySafePWAMetadata(t *testing.T) {
	rec := doPortalRequest(newTestHandler(standardTestData()), "127.0.0.1:12345", "alice@example.com")
	body := rec.Body.String()
	for _, expected := range []string{
		`content="width=device-width, initial-scale=1, viewport-fit=cover"`,
		`<link rel="manifest" href="/static/manifest.json">`,
		`<link rel="apple-touch-icon" href="/static/icons/apple-touch-icon-180.png">`,
		`<meta name="apple-mobile-web-app-capable" content="yes">`,
		`navigator.serviceWorker.register("/static/sw.js", { scope: "/" })`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("PWA metadata omitted %q", expected)
		}
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
