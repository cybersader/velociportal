package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPersonalRenderSameSnapshotNoJSAndIdentity(t *testing.T) {
	store := personalTestStore(t)
	identity, data, _, _ := personalProjectionFixture(t)
	data.UpdatedAt = time.Now()
	cache := &Cache{}
	cache.data.Store(data)
	state, _ := store.Read(identity.Login)
	state, err := store.Mutate(identity.Login, personalTestMutation(state, "create"))
	if err != nil {
		t.Fatal(err)
	}
	portal := NewPortalHandler(cache)
	portal.personal, portal.pollInterval = store, 30*time.Second
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	handler := IdentityMiddleware(trusted, portal)
	request := func(login, host string, duplicate bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "http://"+host+"/portal", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Tailscale-User-Login", login)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "document")
		if duplicate {
			r.Header.Add("Tailscale-User-Login", login)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := request(identity.Login, "portal.example:8081", false)
	body := w.Body.String()
	for _, want := range []string{`href="https://server.tailnet.ts.net/app"`, `data-personal-service="` + state.Document.Entries[0].ID + `"`, `data-service-hostname="server.tailnet.ts.net"`, `data-personal-generation="` + state.Generation + `"`, `data-personal-revision="1"`, `data-personal-initialized="true"`, `personal-services-saved from:body`, `id="personal-manager"`, `id="personal-editor"`, `data-add-personal`, `/static/personal-services.js`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, `data-service-id="0"`) || strings.Contains(body, `data-edit-service="0"`) || strings.Contains(body, `data-default-name="Tool"`) {
		t.Fatal("personal service entered numeric NPM path")
	}
	if w.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("personal page security headers")
	}
	if strings.Index(body, `id="personal-editor"`) < strings.Index(body, `</main>`) || strings.Index(body, `id="personal-manager"`) > strings.Index(body, `<main>`) {
		t.Fatal("editor and account manager must survive poll region swaps")
	}
	for _, test := range []struct {
		login, host string
		duplicate   bool
	}{{"bob@example.com", "portal.example:8081", false}, {identity.Login, "wrong.example", false}, {identity.Login, "portal.example:8081", true}, {" alice@example.com", "portal.example:8081", false}} {
		other := request(test.login, test.host, test.duplicate).Body.String()
		if strings.Contains(other, `href="https://server.tailnet.ts.net/app"`) {
			t.Fatal("private card leaked across exact identity/Host guard")
		}
	}
	// Suppression uses the same cache snapshot at request time; stored authored
	// entries remain untouched, and NPM projection functions remain NPM-only.
	data.UpdatedAt = time.Now().Add(-91 * time.Second)
	stale := request(identity.Login, "portal.example:8081", false).Body.String()
	if strings.Contains(stale, `href="https://server.tailnet.ts.net/app"`) || !strings.Contains(stale, `data-personal-revision="1"`) {
		t.Fatal("stale personal card not suppressed independently of stored document")
	}
	stored, _ := store.Read(identity.Login)
	if len(stored.Document.Entries) != 1 || len(MatchServices(identity, data)) != 0 {
		t.Fatal("render changed store or NPM evaluation")
	}
	portal.personal = nil
	disabled := request(identity.Login, "portal.example:8081", false).Body.String()
	if strings.Contains(disabled, `id="personal-editor"`) || strings.Contains(disabled, `data-add-personal`) || strings.Contains(disabled, `src="/static/personal-services.js"`) {
		t.Fatal("disabled feature rendered controls")
	}
}

func TestPersonalRenderSourceIsolationAndOrganization(t *testing.T) {
	health := NewServiceHealthStore()
	health.publish(map[int]ServiceHealthResult{0: {State: ServiceHealthStateReachable}, 7: {State: ServiceHealthStateReachable}}, time.Minute)
	personal := ServiceCard{PersonalID: personalTestEntry(0).ID, Name: `Personal <tool>`, URL: "https://server.tailnet.ts.net/app", LinkState: serviceLinkReady}
	var card strings.Builder
	renderServiceCard(&card, personal, health, true, true, true, true)
	text := card.String()
	for _, bad := range []string{`data-service-id=`, `data-edit-service=`, `data-default-`, `backend check passed`, `<tool>`} {
		if strings.Contains(text, bad) {
			t.Errorf("personal card contains %s", bad)
		}
	}
	for _, want := range []string{`data-edit-personal=`, `Personal &lt;tool&gt;`, `aria-controls="personal-editor" hidden`, `href="https://server.tailnet.ts.net/app"`} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %s", want)
		}
	}
	cards := []ServiceCard{{ID: 7, Name: "NPM category", Category: "Tools", URL: "https://npm.example", LinkState: serviceLinkReady}, {ID: 8, Name: "NPM uncategorized", URL: "https://other.example", LinkState: serviceLinkReady}, personal}
	state := PersonalServicesState{Generation: strings.Repeat("a", 32), Revision: "1", Initialized: true}
	var out bytes.Buffer
	if err := renderPortalWithOptions(&out, &Identity{Login: "alice@example.com"}, cards, portalRenderOptions{Personal: &state, Organized: true, Health: health, ServiceEditing: true, ServiceEditorAvailable: true, ServiceEditMode: serviceEditBrowser}); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	if strings.Count(page, `class="service-category"`) != 2 || strings.Index(page, `data-service="NPM category"`) > strings.Index(page, `data-service="NPM uncategorized"`) || strings.Index(page, `data-service="NPM uncategorized"`) > strings.Index(page, `data-service="Personal &lt;tool&gt;"`) {
		t.Fatal("NPM categories/order not preserved with personal entries last")
	}
	if !strings.Contains(page, `data-edit-service="7"`) || !strings.Contains(page, `data-default-name="NPM category"`) || strings.Count(page, `backend check passed`) < 1 {
		t.Fatal("NPM editor/defaults/health lost")
	}
}

func TestPersonalRenderAbsentVersusInitializedEmpty(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		revision := "0"
		if initialized {
			revision = "2"
		}
		state := PersonalServicesState{Generation: strings.Repeat("a", 32), Revision: revision, Initialized: initialized}
		markers, _, _, _ := renderPersonalServicesUI(&state)
		if !strings.Contains(markers, `data-personal-revision="`+revision+`"`) || !strings.Contains(markers, `data-personal-initialized="`+map[bool]string{false: "false", true: "true"}[initialized]+`"`) {
			t.Fatal("absence conflated with initialized empty")
		}
	}
}
