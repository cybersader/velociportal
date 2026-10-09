package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServiceEditorRenderedControlsAndAuthorizedSearch(t *testing.T) {
	data := standardTestData()
	data.ProxyHosts[0].DomainNames = []string{"*.example.com"}
	cards := MatchServices(&Identity{Login: "alice@example.com"}, data)
	var output bytes.Buffer
	if err := renderPortalWithOptions(&output, &Identity{Login: "alice@example.com"}, cards, portalRenderOptions{ServiceEditing: true, LogoDefaultVisible: true}); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, want := range []string{`data-edit-service="1"`, `data-edit-service="3"`, `data-service-hostname="wiki.example.com"`, `data-service-editing="true"`, `aria-controls="service-editor"`, `Save for everyone`, `Reset name/link/icon`, `data-service-id="1"`, `data-identity-scope="` + logoPreferenceScope("alice@example.com") + `"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, `data-service-id="2"`) || strings.Contains(body, "jenkins.example.com") {
		t.Fatal("unauthorized card indexed")
	}
	if strings.Count(body, `<dialog id="service-editor"`) != 1 || strings.Index(body, `<dialog id="service-editor"`) < strings.Index(body, `</main>`) {
		t.Fatal("dialog must be single and outside swap/main")
	}
	if strings.Contains(body, `card.tagName === "A"`) {
		t.Fatal("anchor-only search assumption retained")
	}
	cardStart := strings.Index(body, `<article class="card card-editable" data-service="wiki.example.com"`)
	cardEnd := strings.Index(body[cardStart:], `</article>`) + cardStart
	card := body[cardStart:cardEnd]
	if strings.Index(card, `</a>`) > strings.Index(card, `<button`) {
		t.Fatal("edit nested inside link")
	}
	output.Reset()
	if err := renderPortal(&output, &Identity{Login: "alice@example.com"}, cards); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), `data-edit-service=`) || strings.Contains(output.String(), `<dialog id="service-editor"`) {
		t.Fatal("viewer receives editor controls")
	}
	if !strings.Contains(output.String(), `<a class="card" href="https://wiki.example.com"`) {
		t.Fatal("no-JS link lost")
	}
}

func TestServiceEditorControlsUseExactIdentityAndHost(t *testing.T) {
	e, cache, _ := editorFixture(t)
	e.config.Logins = map[string]bool{"alice@example.com": true}
	portal := NewPortalHandler(cache)
	portal.editor = e
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	handler := IdentityMiddleware(trusted, portal)
	for _, test := range []struct {
		login, host     string
		duplicate, want bool
	}{
		{"alice@example.com", "portal.example:8081", false, true},
		{"bob@example.com", "portal.example:8081", false, false},
		{"Alice@example.com", "portal.example:8081", false, false},
		{"alice@example.com", "wrong.example", false, false},
		{"alice@example.com", "portal.example:8081", true, false},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://"+test.host+"/portal", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Tailscale-User-Login", test.login)
		if test.duplicate {
			r.Header.Add("Tailscale-User-Login", test.login)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
			t.Fatal("editing page must be frame-denied and no-store")
		}
		if got := strings.Contains(w.Body.String(), `data-edit-service=`); got != test.want {
			t.Errorf("identity/Host editor eligibility got %t want %t", got, test.want)
		}
	}
}

func TestServiceIconRegistryIsFiniteEmbeddedAndPinned(t *testing.T) {
	if len(serviceIcons) < 13 || len(serviceIcons) > 21 {
		t.Fatal("unexpected catalog bound")
	}
	var provenance struct {
		Revision string `json:"revision"`
		Files    []struct {
			Path, SHA256 string
			Bytes        int
		} `json:"files"`
	}
	b, err := assetsFS.ReadFile("assets/service-icons/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &provenance); err != nil {
		t.Fatal(err)
	}
	if provenance.Revision != "57e939e504eda0ea764098015da93aa666ad6f31" {
		t.Fatal("icon pin changed")
	}
	digests := map[string]string{}
	for _, f := range provenance.Files {
		digests[filepath.Base(f.Path)] = f.SHA256
	}
	for _, icon := range serviceIcons {
		if !validServiceIcon(icon.ID) || !strings.HasPrefix(icon.Path, "/static/service-icons/") {
			t.Fatal("unsafe registry")
		}
		data, err := assetsFS.ReadFile("assets/" + strings.TrimPrefix(icon.Path, "/static/"))
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 256*1024 {
			t.Fatal("oversized icon")
		}
		if icon.ID == "generic" {
			for _, active := range []string{"<script", "<foreignObject", "href=", "onload=", "http:"} {
				if strings.Contains(string(data), active) && active != "http:" {
					t.Fatal("active generic SVG")
				}
			}
			continue
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if config.Width > 2048 || config.Height > 2048 {
			t.Fatal("oversized image dimensions")
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		if digests[filepath.Base(icon.Path)] != hex.EncodeToString(sum[:]) {
			t.Fatal("icon checksum mismatch")
		}
	}
	for _, bad := range []string{"https://icons.example/icon.png", "../generic", "GENERIC", "unknown"} {
		if validServiceIcon(bad) {
			t.Fatal("arbitrary icon accepted")
		}
	}
	metadata, err := parseServiceMetadata([]byte(`{"version":3,"services":[{"proxy_host_id":3,"icon":"grafana","category":"Tools","order":8}]}`))
	if err != nil {
		t.Fatal(err)
	}
	cards := MatchServices(&Identity{Login: "alice@example.com"}, &CacheData{Policy: standardTestData().Policy, ProxyHosts: standardTestData().ProxyHosts, ServiceMetadata: metadata})
	var body strings.Builder
	for _, card := range cards {
		renderServiceCard(&body, card, nil)
	}
	if !strings.Contains(body.String(), `/static/service-icons/grafana.png`) {
		t.Fatal("selected icon not rendered")
	}
}

// Opt-in synthetic fixture output for the final bounded renderer/browser stage.
// No server, upstream clients, real identities, credentials or private inventory.
func TestServiceEditorRenderFixture(t *testing.T) {
	dir := os.Getenv("VELOCIPORTAL_TEST_RENDER_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	cards := []ServiceCard{
		{ID: 1, Name: "A deliberately long service name that must keep its full-width wrapping", URL: "https://long-service.example.test/app", Domain: "long-service.example.test", LinkState: serviceLinkReady, Icon: "grafana"},
		{ID: 3, Name: "*.example.test", Domain: "*.example.test", LinkState: serviceLinkNeedsMetadata},
	}
	health := NewServiceHealthStore()
	health.publish(map[int]ServiceHealthResult{
		1: {ProxyHostID: 1, State: ServiceHealthStateReachable, CheckedAt: time.Now()},
		3: {ProxyHostID: 3, State: ServiceHealthStateAuthRequired, CheckedAt: time.Now()},
	}, time.Hour)
	machines := []MachineCard{{ID: "synthetic-machine", Target: "synthetic-server.tailnet.ts.net", Access: []MachineAccess{{User: "operator", Action: "accept"}, {User: machineNonrootSelector, Action: "check"}}}}
	for _, mode := range []string{"editor", "viewer"} {
		var output bytes.Buffer
		if err := renderPortalWithOptions(&output, &Identity{Login: "synthetic@example.test", Name: "Synthetic viewer"}, cards, portalRenderOptions{ServiceEditing: mode == "editor", ServiceEditorAvailable: true, LogoDefaultVisible: true, Health: health, Machines: machines, MachinesAvailable: true}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, mode+".html"), output.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServiceEditorSetupAndReadOnlyDiagnostics(t *testing.T) {
	metadata := editorTestFile(t, editorTestDocument)
	before, err := os.ReadFile(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := inspectServiceMetadataEditorStorage(metadata); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(metadata))
	if len(entries) != 1 {
		t.Fatal("Doctor created synchronization/probe files")
	}
	env := filepath.Join(t.TempDir(), "test.env")
	if err := writeEnvFile(env, map[string]string{"SERVICE_METADATA_FILE": metadata, "UNKNOWN": "preserve"}); err != nil {
		t.Fatal(err)
	}
	if err := configureSetupServiceEditor(env, `["alice@example.com"]`, "http://portal.example:8081", false); err != nil {
		t.Fatal(err)
	}
	values, _ := readEnvFile(env)
	if values["UNKNOWN"] != "preserve" || values["PORTAL_PUBLIC_ORIGIN"] != "http://portal.example:8081" {
		t.Fatal("setup failed preservation")
	}
	good, _ := os.ReadFile(env)
	if err := configureSetupServiceEditor(env, `[" alice@example.com"]`, "http://portal.example:8081", false); err == nil {
		t.Fatal("padded editor accepted")
	}
	after, _ := os.ReadFile(env)
	if !bytes.Equal(good, after) {
		t.Fatal("invalid setup changed file")
	}
	if err := configureSetupServiceEditor(env, "", "", true); err != nil {
		t.Fatal(err)
	}
	values, _ = readEnvFile(env)
	if _, present := values["PORTAL_EDITORS"]; present {
		t.Fatal("disable retained editor")
	}
	after, _ = os.ReadFile(metadata)
	if !bytes.Equal(before, after) {
		t.Fatal("setup/Doctor wrote metadata")
	}
	if err := os.Symlink(metadata, filepath.Join(filepath.Dir(metadata), "link.json")); err != nil {
		t.Fatal(err)
	}
	if inspectServiceMetadataEditorStorage(filepath.Join(filepath.Dir(metadata), "link.json")) == nil {
		t.Fatal("Doctor accepted unsafe target")
	}
}
