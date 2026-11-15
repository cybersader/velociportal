package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

const sharedIconTestDigest = "sha256:5cc8abe9c205a36d8442a36a51d41c22eb0e90f9623416ed1f3b2269030dfe5c"

// Strict syntax + binding fixtures BELOW the immutable runtime gate. No configured
// v4 file is activated and no production acceptance or writer path is bypassed.
func sharedIconTestMetadata(t *testing.T, npm, machine, app string) *ServiceMetadata {
	t.Helper()
	services, machines, apps := "[]", "[]", "[]"
	if npm != "" {
		services = fmt.Sprintf(`[{"proxy_host_id":3,"name":"Shared <Wiki> & tools","icon":%q},{"proxy_host_id":99,"icon":"jellyfin"}]`, npm)
	}
	if machine != "" {
		machines = fmt.Sprintf(`[{"device_id_type":"id","device_id":"device-1","icon":%q},{"device_id_type":"id","device_id":"invisible","icon":"jellyfin"}]`, machine)
	}
	if app != "" {
		apps = fmt.Sprintf(`[{"target_digest":%q,"icon":%q}]`, sharedIconTestDigest, app)
	}
	m, err := parseServiceMetadata([]byte(fmt.Sprintf(`{"version":4,"namespace":"portal-demo-01","control_plane":"tailscale","services":%s,"machines":%s,"applications":%s}`, services, machines, apps)))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateServiceMetadataBinding(m.document, serviceMetadataBinding{controlPlaneTailscale, "portal-demo-01"}); err != nil {
		t.Fatal(err)
	}
	return m
}

func sharedIconTestFixture(t *testing.T) (*Config, *Cache, *PersonalServicesStore) {
	t.Helper()
	_, data, state, _, binding := personalApplicationFixture(t)
	data.UpdatedAt = time.Now().Truncate(time.Second)
	data.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22"), mustGrantCapability(t, "tcp:443"), mustGrantCapability(t, "tcp:8443")}
	data.ProxyHosts = []ProxyHost{
		{ID: 3, Enabled: true, DomainNames: []string{"npm.example.test"}, ForwardHost: "100.64.0.10", ForwardPort: 443, ForwardScheme: "https"},
		{ID: 99, Enabled: false, DomainNames: []string{"hidden.example.test"}, ForwardHost: "100.64.0.10", ForwardPort: 443, ForwardScheme: "https"},
	}
	data.ServiceMetadata = sharedIconTestMetadata(t, "gitea", "portainer", "immich")
	data.GrantRoleSelectorsByLogin["alice@example.com"] = []string{"autogroup:member", "autogroup:admin"}
	store := personalTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	sharedIconRestore(t, store, "alice@example.com", state.Document)
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cfg := &Config{ControlPlane: binding.ControlPlane, StateNamespace: binding.Namespace, PollInterval: 30 * time.Second, TrustedProxyCIDR: trusted, PortalEditMode: serviceEditOff, PortalLogoDefaultVisible: true}
	return cfg, newTestCache(data), store
}

// Mirror production's common namespace for both independently owned documents.
// The incumbent durable fixture hardcodes fixture-1, so do not reuse its binding
// for this portal-demo-01 application digest fixture.
func sharedIconPrivateConfig(t *testing.T, cfg *Config, records []personalStorageCollection) *PersonalServicesConfig {
	t.Helper()
	path := storageTestPath(t)
	snapshot := personalStorageSnapshot{Version: 1, Namespace: cfg.StateNamespace, StoreID: strings.Repeat("a", 32), Collections: records}
	encoded, err := serializePersonalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	return &PersonalServicesConfig{Origin: "http://portal.example:8081", Host: "portal.example:8081", File: path, Namespace: cfg.StateNamespace}
}

func sharedIconRestore(t *testing.T, s *PersonalServicesStore, login string, document PersonalServicesDocument) {
	t.Helper()
	state, err := s.Read(login)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Mutate(login, PersonalServicesMutation{Action: "restore", Generation: state.Generation, Revision: state.Revision, Document: &document})
	if err != nil {
		t.Fatal(err)
	}
}

func sharedIconMux(t *testing.T, cfg *Config, cache *Cache, editor *ServiceMetadataEditor, store *PersonalServicesStore) http.Handler {
	t.Helper()
	mux, err := newServerMux(cfg, cache, nil, editor, store)
	if err != nil {
		t.Fatal(err)
	}
	return mux
}

func sharedIconResponse(t *testing.T, h http.Handler, login, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := personalHTTPRequest("GET", nil)
	r.URL.Path = path
	r.Header.Set("Tailscale-User-Login", login)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("missing no-store")
	}
	return w
}

func sharedIconCard(t *testing.T, page, marker string) string {
	t.Helper()
	pattern := `<(article|a)\b[^>]*` + regexp.QuoteMeta(marker) + `[^>]*>`
	match := regexp.MustCompile(pattern).FindStringSubmatchIndex(page)
	if match == nil {
		t.Fatalf("missing card %s", marker)
	}
	// An article may contain a service or console anchor; its first </a> is not
	// the end of the card. Compare the whole row, including commands/policy text.
	closing := "</" + page[match[2]:match[3]] + ">"
	end := strings.Index(page[match[1]:], closing)
	if end < 0 {
		t.Fatal("unterminated card", marker)
	}
	return page[match[0] : match[1]+end+len(closing)]
}
func sharedIconAssert(t *testing.T, card, icon string) {
	t.Helper()
	images := regexp.MustCompile(`<img\b[^>]*>`).FindAllString(card, -1)
	if icon == "" {
		if len(images) != 0 {
			t.Fatal("invented fallback image", images)
		}
		return
	}
	if len(images) != 1 || !strings.Contains(images[0], `src="`+serviceIconForID(icon).Path+`"`) || !strings.Contains(images[0], `alt=""`) {
		t.Fatalf("icon %q: %v", icon, images)
	}
}
func sharedIconAssertAll(t *testing.T, page, npm, app, machine string) {
	t.Helper()
	sharedIconAssert(t, sharedIconCard(t, page, `data-service-id="3"`), npm)
	sharedIconAssert(t, sharedIconCard(t, page, `data-personal-service="ps_00000000000000000000000000000001"`), app)
	sharedIconAssert(t, sharedIconCard(t, page, `data-machine="server.tailnet.ts.net"`), machine)
}

func TestSharedIconActualRenderAllSourcesReadonlyModes(t *testing.T) {
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser} {
		t.Run(string(mode), func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			cfg.PortalEditMode = mode
			cfg.ServiceMetadataFile = editorTestFile(t, `{"version":3,"services":[]}`)
			editor, unusedPersonal, err := newPresentationStores(cfg)
			if err != nil || editor != nil || unusedPersonal != nil {
				t.Fatal("readonly initialized writer", err)
			}
			h := sharedIconMux(t, cfg, cache, editor, store)
			before, _ := store.Read("alice@example.com")
			machineEvidence := evaluateMachines(&Identity{Login: "alice@example.com"}, cache.Get())
			for _, path := range []string{"/", "/portal"} {
				page := sharedIconResponse(t, h, "alice@example.com", path).Body.String()
				sharedIconAssertAll(t, page, "gitea", "immich", "portainer")
				for _, bad := range []string{sharedIconTestDigest, `data-service-id="99"`, `data-machine="invisible"`, "hidden.example.test", "Shared <Wiki>"} {
					if strings.Contains(page, bad) {
						t.Fatal("unexpected output", bad)
					}
				}
				for _, want := range []string{"Shared &lt;Wiki&gt; &amp; tools", `data-command="tailscale ssh deploy@server.tailnet.ts.net"`, `https://console.tailscale.com/admin/machines?`, "Inherit shared default (otherwise generic)"} {
					if !strings.Contains(page, want) {
						t.Fatal("missing", want)
					}
				}
				if mode == serviceEditBrowser && !strings.Contains(page, `data-default-icon="gitea"`) {
					t.Fatal("poll default missing")
				}
				if strings.Contains(page, "service-metadata-saved from:body") {
					t.Fatal("readonly enables shared-edit polling")
				}
			}
			after, _ := store.Read("alice@example.com")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(machineEvidence, evaluateMachines(&Identity{Login: "alice@example.com"}, cache.Get())) {
				t.Fatal("presentation mutated authored state/evidence")
			}
			files, err := os.ReadDir(filepath.Dir(cfg.ServiceMetadataFile))
			if err != nil || len(files) != 1 {
				t.Fatal("readonly acquired lock/storage", err)
			}
			r := personalHTTPRequest("GET", nil)
			r.URL.Path = serviceMetadataEditorRoute + "3"
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatal("readonly exposes shared API", w.Code)
			}
		})
	}
}

func TestSharedIconActualRenderBindingAndFallbacks(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Config, *CacheData)
		npm    string
	}{
		{"absent", func(_ *Config, d *CacheData) { d.ServiceMetadata = nil }, ""},
		{"unbound", func(c *Config, _ *CacheData) { c.StateNamespace = "" }, ""},
		{"wrong namespace", func(c *Config, _ *CacheData) { c.StateNamespace = "another-source" }, ""},
		{"wrong selected provider", func(c *Config, _ *CacheData) { c.ControlPlane = controlPlaneHeadscale }, ""},
		{"wrong document provider", func(_ *Config, d *CacheData) {
			m, err := parseServiceMetadata([]byte(`{"version":4,"namespace":"portal-demo-01","control_plane":"headscale","services":[{"proxy_host_id":3,"icon":"gitea"}],"machines":[],"applications":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			d.ServiceMetadata = m
		}, ""},
		{"no maps", func(_ *Config, d *CacheData) { d.ServiceMetadata = sharedIconTestMetadata(t, "", "", "") }, ""},
		{"legacy", func(_ *Config, d *CacheData) {
			m, err := parseServiceMetadata([]byte(`{"version":3,"services":[{"proxy_host_id":3,"icon":"grafana"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			d.ServiceMetadata = m
		}, "grafana"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			original := cache.Get().ServiceMetadata
			tt.change(cfg, cache.Get())
			published := cache.Get().ServiceMetadata
			page := sharedIconResponse(t, sharedIconMux(t, cfg, cache, nil, store), "alice@example.com", "/portal").Body.String()
			sharedIconAssertAll(t, page, tt.npm, "generic", "")
			if cache.Get().ServiceMetadata != published || original.document.Namespace != "portal-demo-01" {
				t.Fatal("mutated published metadata")
			}
		})
	}
}

func TestSharedIconActualRenderTypedIdentityFromAPI(t *testing.T) {
	u := newTestUpstreams(t)
	for _, tt := range []struct{ name, fields, icon string }{
		{"selected id", `"id":"device-1","nodeId":"other"`, "immich"},
		{"replacement", `"id":"replacement"`, "generic"},
		{"different kind", `"nodeId":"device-1"`, "generic"},
		{"ambiguous", `"id":"device-1","id":"device-1"`, "generic"},
		{"unsupported", `"id":" device-1 "`, "generic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			client, closeServer := tailscaleClientWithBodies(t, presentationTestUsers, `{"devices":[`+presentationTestRecord(tt.fields)+`]}`, presentationTestPolicy)
			defer closeServer()
			data, err := loadSnapshot(context.Background(), client, u.npm)
			if err != nil {
				t.Fatal(err)
			}
			data.ProxyHosts = cache.Get().ProxyHosts
			data.ServiceMetadata = cache.Get().ServiceMetadata
			cache.data.Store(data)
			page := sharedIconResponse(t, sharedIconMux(t, cfg, cache, nil, store), "alice@example.com", "/portal").Body.String()
			machine := ""
			if tt.icon == "immich" {
				machine = "portainer"
			}
			sharedIconAssertAll(t, page, "gitea", tt.icon, machine)
		})
	}
	for _, tt := range []struct {
		name string
		id   ProviderDeviceID
	}{
		{"missing", ProviderDeviceID{}},
		{"wrong provider", ProviderDeviceID{controlPlaneHeadscale, deviceIDKindID, "device-1"}},
		{"invalid kind", ProviderDeviceID{controlPlaneTailscale, "other", "device-1"}},
		{"invalid value", ProviderDeviceID{controlPlaneTailscale, deviceIDKindID, "device-1 "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			cache.Get().PresentationDeviceIDsByNodeID["node-1"] = tt.id
			page := sharedIconResponse(t, sharedIconMux(t, cfg, cache, nil, store), "alice@example.com", "/portal").Body.String()
			sharedIconAssertAll(t, page, "gitea", "generic", "")
		})
	}
}

func TestSharedIconActualRenderPersonalDestinationAndPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, url, authored, want string
		rename                    bool
	}{
		{"inherit", "https://server.tailnet.ts.net/photos", "", "immich", false},
		{"explicit generic", "https://server.tailnet.ts.net/photos", "generic", "generic", false},
		{"explicit finite", "https://server.tailnet.ts.net/photos", "grafana", "grafana", false},
		{"path slash", "https://server.tailnet.ts.net/photos/", "", "generic", false},
		{"path case", "https://server.tailnet.ts.net/Photos", "", "generic", false},
		{"other path", "https://server.tailnet.ts.net/private", "", "generic", false},
		{"port", "https://server.tailnet.ts.net:8443/photos", "", "generic", false},
		{"rename", "https://renamed.tailnet.ts.net/photos", "", "immich", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			state, _ := store.Read("alice@example.com")
			e := state.Document.Entries[0]
			_, err := store.Mutate("alice@example.com", PersonalServicesMutation{Action: "update", ID: e.ID, Generation: state.Generation, Revision: state.Revision, Name: `My <private> & tool`, URL: tt.url, Icon: tt.authored})
			if err != nil {
				t.Fatal(err)
			}
			if tt.rename {
				cache.Get().Nodes[0].Name = "renamed.tailnet.ts.net"
			}
			before, _ := store.Read("alice@example.com")
			page := sharedIconResponse(t, sharedIconMux(t, cfg, cache, nil, store), "alice@example.com", "/portal").Body.String()
			card := sharedIconCard(t, page, `data-personal-service="`+e.ID+`"`)
			sharedIconAssert(t, card, tt.want)
			if !strings.Contains(card, "My &lt;private&gt; &amp; tool") {
				t.Fatal("name not escaped")
			}
			target := "server.tailnet.ts.net"
			if tt.rename {
				target = "renamed.tailnet.ts.net"
			}
			sharedIconAssert(t, sharedIconCard(t, page, `data-machine="`+target+`"`), "portainer")
			after, _ := store.Read("alice@example.com")
			if !reflect.DeepEqual(before, after) || after.Document.Entries[0].Icon != tt.authored {
				t.Fatal("default materialized")
			}
		})
	}
}

func TestSharedIconActualRenderVisibilityStalenessAndHeadscale(t *testing.T) {
	for _, tt := range []struct {
		name              string
		change            func(*CacheData)
		npm, app, machine bool
	}{
		{"stale", func(d *CacheData) { d.UpdatedAt = time.Now().Add(-time.Hour) }, true, false, true},
		{"cold", func(d *CacheData) { d.UpdatedAt = time.Time{} }, true, false, true},
		{"future", func(d *CacheData) { d.UpdatedAt = time.Now().Add(time.Hour) }, true, false, true},
		{"no HTTP grant", func(d *CacheData) {
			d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22")}
		}, false, false, true},
		{"no SSH grant", func(d *CacheData) {
			d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:443")}
		}, true, true, false},
		{"unsupported SSH", func(d *CacheData) { d.Policy.SSH = SSHPolicy{} }, true, true, false},
		{"no capability", func(d *CacheData) { d.MachineSSHCapableByID = nil }, true, true, false},
		{"no human", func(d *CacheData) { d.GrantRoleSelectorsByLogin = nil }, true, false, false},
		{"no grants", func(d *CacheData) { d.Policy.Grants = nil }, false, false, false},
		{"ambiguous hostname", func(d *CacheData) { n := d.Nodes[0]; n.ID = "other"; d.Nodes = append(d.Nodes, n) }, true, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			tt.change(cache.Get())
			h := sharedIconMux(t, cfg, cache, nil, store)
			page := sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
			for marker, want := range map[string]bool{`data-service-id="3"`: tt.npm, `data-personal-service="`: tt.app, `data-machine="`: tt.machine} {
				if strings.Contains(page, marker) != want {
					t.Fatal(tt.name, marker, want)
				}
			}
			if !tt.app && strings.Contains(page, `href="https://server.tailnet.ts.net/photos"`) {
				t.Fatal("inactive bypass link")
			}
			foreign := sharedIconResponse(t, h, "stranger@example.com", "/portal").Body.String()
			if strings.Contains(foreign, `data-service-id="3"`) || strings.Contains(foreign, `data-personal-service="`) || strings.Contains(foreign, `data-machine="`) {
				t.Fatal("metadata created foreign cards")
			}
		})
	}
	t.Run("headscale NPM only", func(t *testing.T) {
		cfg, cache, _ := sharedIconTestFixture(t)
		cfg.ControlPlane = controlPlaneHeadscale
		data := cache.Get()
		data.ControlPlane.Provider = controlPlaneHeadscale
		data.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"*"}}}
		data.Policy.Grants = nil
		m, err := parseServiceMetadata([]byte(`{"version":4,"namespace":"portal-demo-01","control_plane":"headscale","services":[{"proxy_host_id":3,"icon":"gitea"}],"machines":[],"applications":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		data.ServiceMetadata = m
		page := sharedIconResponse(t, sharedIconMux(t, cfg, cache, nil, nil), "alice@example.com", "/portal").Body.String()
		sharedIconAssert(t, sharedIconCard(t, page, `data-service-id="3"`), "gitea")
		if strings.Contains(page, `data-personal-service="`) || strings.Contains(page, `data-machine="`) {
			t.Fatal("headscale broadened")
		}
	})
}

func TestSharedIconActualRenderMachineArtworkOnly(t *testing.T) {
	for _, admin := range []bool{false, true} {
		t.Run(fmt.Sprint(admin), func(t *testing.T) {
			cfg, cache, store := sharedIconTestFixture(t)
			if !admin {
				cache.Get().GrantRoleSelectorsByLogin["alice@example.com"] = []string{"autogroup:member"}
			}
			h := sharedIconMux(t, cfg, cache, nil, store)
			before := evaluateMachines(&Identity{Login: "alice@example.com"}, cache.Get())
			decorated := sharedIconCard(t, sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String(), `data-machine="server.tailnet.ts.net"`)
			cache.Get().ServiceMetadata = nil
			plain := sharedIconCard(t, sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String(), `data-machine="server.tailnet.ts.net"`)
			image := regexp.MustCompile(`<img class="machine-icon"[^>]*>`)
			if len(image.FindAllString(decorated, -1)) != 1 || image.ReplaceAllString(decorated, "") != plain {
				t.Fatal("artwork changed machine controls, account choices, commands or policy")
			}
			if strings.Contains(decorated, tailscaleConsoleMachinesURL) != admin {
				t.Fatal("artwork changed role-gated navigation")
			}
			if !reflect.DeepEqual(before, evaluateMachines(&Identity{Login: "alice@example.com"}, cache.Get())) {
				t.Fatal("metadata changed machine evidence")
			}
		})
	}
}

func TestSharedIconActualRenderCommittedRequestLocalView(t *testing.T) {
	cfg, cache, store := sharedIconTestFixture(t)
	editor, _, _ := editorFixture(t)
	cfg.PortalEditMode = serviceEditShared
	original := cache.Get()
	originalMetadata := original.ServiceMetadata
	editor.view.Store(&serviceMetadataEditorView{metadata: sharedIconTestMetadata(t, "grafana", "generic", "nextcloud")})
	h := sharedIconMux(t, cfg, cache, editor, store)
	page := sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	sharedIconAssertAll(t, page, "grafana", "nextcloud", "generic")
	// An uncertain shared writer keeps its last confirmed presentation only; this
	// does not confer a mutable target/read-back or impose write freshness on views.
	editor.disabled = true
	cache.Get().UpdatedAt = time.Now().Add(-time.Hour)
	page = sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	sharedIconAssert(t, sharedIconCard(t, page, `data-service-id="3"`), "grafana")
	sharedIconAssert(t, sharedIconCard(t, page, `data-machine="server.tailnet.ts.net"`), "generic")
	if cache.Get() != original || cache.Get().ServiceMetadata != originalMetadata {
		t.Fatal("request changed cache")
	}
	// A committed view from another valid namespace must not fall back to the
	// old cache's indexes. Reject its presentation consistently across all cards.
	encoded, err := serializeServiceMetadataDocument(editor.view.Load().metadata.document)
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := parseServiceMetadata(bytes.ReplaceAll(encoded, []byte("portal-demo-01"), []byte("other-source")))
	if err != nil {
		t.Fatal(err)
	}
	editor.view.Store(&serviceMetadataEditorView{metadata: wrong})
	cache.Get().UpdatedAt = time.Now().Truncate(time.Second)
	page = sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	sharedIconAssertAll(t, page, "generic", "generic", "") // Shared-editor incumbent NPM fallback is generic.
	if cache.Get().ServiceMetadata != originalMetadata {
		t.Fatal("mismatched view rewrote cache")
	}
}

func TestSharedIconActualRenderPrivatePersistenceIsolation(t *testing.T) {
	cfg, cache, volatile := sharedIconTestFixture(t)
	data := cache.Get()
	data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: true}, "bob@example.com": {ID: "bob-id", DirectMember: true}}
	data.GrantRoleSelectorsByLogin["bob@example.com"] = []string{"autogroup:member"}
	data.Policy.Grants[0].BrowserSrc = []string{"autogroup:member"}
	alice, _ := volatile.Read("alice@example.com")
	bobDoc := copyPersonalDocument(alice.Document)
	bobDoc.Entries[0].ID = personalTestEntry(1).ID
	bobDoc.Entries[0].Name = "Bob secret title"
	privateCfg := sharedIconPrivateConfig(t, cfg, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "7", alice.Document.Entries), durableRecord("bob@example.com", "bob-id", "11", bobDoc.Entries)})
	// Presentation receives the common runtime binding, not private authored data.
	store := durableTestStore(t, privateCfg)
	h := sharedIconMux(t, cfg, cache, nil, store)
	diskBefore, err := os.ReadFile(privateCfg.File)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]PersonalServicesState{}
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		before, err := store.ReadForSnapshot(login, data, cfg.PollInterval, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		states[login] = before
		page := sharedIconResponse(t, h, login, "/portal").Body.String()
		sharedIconAssert(t, sharedIconCard(t, page, `data-personal-service="`+before.Document.Entries[0].ID+`"`), "immich")
		api := sharedIconResponse(t, h, login, personalServicesRoute).Body.Bytes()
		var response personalServicesResponse
		if err := json.Unmarshal(api, &response); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(response.PersonalServicesState, before) || response.Document.Entries[0].Icon != "" {
			t.Fatal("API materialized icon or changed revision")
		}
		for _, bad := range []string{sharedIconTestDigest, "target_digest", "applications", "matching_users"} {
			if bytes.Contains(api, []byte(bad)) || strings.Contains(page, bad) {
				t.Fatal("shared lookup leaked", bad)
			}
		}
		if login == "alice@example.com" && (strings.Contains(page, "Bob secret title") || bytes.Contains(api, []byte("Bob secret title"))) {
			t.Fatal("other owner exposed")
		}
	}
	// Refresh changes only defaults, not either private revision/file or backup input.
	data.ServiceMetadata = sharedIconTestMetadata(t, "gitea", "portainer", "grafana")
	for login, before := range states {
		page := sharedIconResponse(t, h, login, "/portal").Body.String()
		sharedIconAssert(t, sharedIconCard(t, page, `data-personal-service="`+before.Document.Entries[0].ID+`"`), "grafana")
		after, err := store.ReadForSnapshot(login, data, cfg.PollInterval, time.Now)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatal("private state/revision changed", err)
		}
	}
	diskAfter, err := os.ReadFile(privateCfg.File)
	if err != nil || !bytes.Equal(diskBefore, diskAfter) {
		t.Fatal("private disk changed", err)
	}
	if bytes.Contains(diskAfter, []byte("immich")) || bytes.Contains(diskAfter, []byte("target_digest")) {
		t.Fatal("default in private backup")
	}
}

func TestSharedIconActualRenderImportKeepsEmptyAuthoredIcon(t *testing.T) {
	cfg, cache, volatile := sharedIconTestFixture(t)
	cache.Get().TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: true}}
	backup, _ := volatile.Read("alice@example.com")
	privateCfg := sharedIconPrivateConfig(t, cfg, []personalStorageCollection{})
	store := durableTestStore(t, privateCfg)
	h := sharedIconMux(t, cfg, cache, nil, store)
	first := personalHTTPRead(t, h)
	req := PersonalServicesMutation{Action: "restore", Generation: first.Generation, Revision: first.Revision, StoreID: first.StoreID, CSRF: first.CSRF, Document: &backup.Document}
	w, saved := personalHTTPPost(t, h, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	page := sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	sharedIconAssert(t, sharedIconCard(t, page, `data-personal-service="`+saved.Document.Entries[0].ID+`"`), "immich")
	after := personalHTTPRead(t, h)
	if after.Document.Entries[0].Icon != "" || after.Revision != saved.Revision || !reflect.DeepEqual(after.Document, backup.Document) {
		t.Fatal("restore materialized default")
	}
	disk, err := os.ReadFile(privateCfg.File)
	if err != nil || bytes.Contains(disk, []byte("immich")) || bytes.Contains(disk, []byte(sharedIconTestDigest)) {
		t.Fatal("import persisted shared data", err)
	}
}

func TestSharedIconRuntimeGateAndFiniteRenderer(t *testing.T) {
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	cfg, cache, _ := sharedIconTestFixture(t)
	encoded, err := serializeServiceMetadataDocument(cache.Get().ServiceMetadata.document)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ServiceMetadataFile = editorTestFile(t, string(encoded))
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser} {
		cfg.PortalEditMode = mode
		e, p, err := newPresentationStores(cfg)
		if err != nil || e != nil {
			t.Fatal("readonly v4 rejected or created writer", err)
		}
		_ = p.Close()
		if _, err := os.Stat(cfg.ServiceMetadataFile + ".lock"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("readonly opened writer")
		}
	}
	card := MachineCard{Target: "server.tailnet.ts.net", Access: []MachineAccess{{User: "deploy", Action: "accept"}}}
	for _, icon := range []string{"", `"><script>alert(1)</script>`, "https://evil.test/image.svg", "../generic"} {
		var out strings.Builder
		renderMachineCard(&out, card, 1, true, icon)
		sharedIconAssert(t, out.String(), "")
	}
	for _, icon := range serviceIconIDs {
		var out strings.Builder
		renderMachineCard(&out, card, 1, true, icon)
		sharedIconAssert(t, out.String(), icon)
	}
}

// Fresh real mux output consumed by an additional private JS assertion harness.
func TestSharedIconBrowserRenderFixture(t *testing.T) {
	cfg, cache, store := sharedIconTestFixture(t)
	cfg.PortalEditMode = serviceEditBrowser
	cache.Get().ServiceMetadata = sharedIconTestMetadata(t, "grafana", "portainer", "immich")
	h := sharedIconMux(t, cfg, cache, nil, store)
	first := sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	state := personalHTTPRead(t, h)
	cache.Get().ServiceMetadata = sharedIconTestMetadata(t, "generic", "portainer", "grafana")
	second := sharedIconResponse(t, h, "alice@example.com", "/portal").Body.String()
	refreshedState := personalHTTPRead(t, h)
	if !reflect.DeepEqual(state.PersonalServicesState, refreshedState.PersonalServicesState) || state.Document.Entries[0].Icon != "" {
		t.Fatal("fixture shared refresh changed authored data")
	}
	sharedIconAssertAll(t, first, "grafana", "immich", "portainer")
	sharedIconAssertAll(t, second, "generic", "grafana", "portainer")
	path := os.Getenv("VELOCIPORTAL_SHARED_ICON_FIXTURE")
	if path == "" {
		path = filepath.Join(t.TempDir(), "shared-icons.json")
	}
	encoded, err := json.Marshal(map[string]any{"initial": first, "refreshed": second, "state": state, "refreshed_state": refreshedState})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}
