package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// S5 fixtures open real configured temporary files and use the production mux,
// IdentityMiddleware and JSON-RPC dispatch. Provider evidence is synthetic; no
// fixture proves native clients, genuine Serve identity or a deployed restart.
const s5Origin = "http://portal.example:8081"
const s5Namespace = "s5-source"

type s5Fixture struct {
	cfg      *Config
	cache    *Cache
	editor   *ServiceMetadataEditor
	personal *PersonalServicesStore
	mux      *http.ServeMux
}

func s5Document(provider string, version int) string {
	services := `[{"proxy_host_id":3,"name":"Original","url":null},{"proxy_host_id":99,"name":"Unseen","url":null}]`
	if version >= 2 {
		services = `[{"proxy_host_id":3,"name":"Original","url":null,"category":"Tools","order":0},{"proxy_host_id":99,"name":"Unseen","url":null}]`
	}
	if version == 4 {
		return fmt.Sprintf(`{"version":4,"namespace":"%s","control_plane":"%s","services":%s,"machines":[],"applications":[]}`, s5Namespace, provider, services)
	}
	return fmt.Sprintf(`{"version":%d,"services":%s}`, version, services)
}
func s5Values(t *testing.T, provider, mode, personal string, mcp bool, version int, records []personalStorageCollection) map[string]string {
	t.Helper()
	v := namespaceConfigValues(provider, mode)
	v["POLL_INTERVAL"] = "30s"
	v["SERVICE_METADATA_FILE"] = editorTestFile(t, s5Document(provider, version))
	v["PORTAL_STATE_NAMESPACE"] = s5Namespace
	if personal != "off" {
		v["PORTAL_PERSONAL_SERVICES_ORIGIN"] = s5Origin
	}
	if personal == "durable" {
		if records == nil {
			records = []personalStorageCollection{}
		}
		p := sharedIconPrivateConfig(t, &Config{StateNamespace: s5Namespace}, records)
		v["PORTAL_PERSONAL_SERVICES_FILE"] = p.File
	}
	if mcp {
		v["PORTAL_MCP_ORIGIN"] = s5Origin
	}
	return v
}
func s5Data(t *testing.T, provider controlPlaneProvider) *CacheData {
	t.Helper()
	d := machineMatcherFixture(t)
	d.Nodes[0].Name = "server.tailnet.ts.net"
	d.UpdatedAt = time.Now()
	d.ControlPlane.Provider = provider
	d.ProxyHosts = []ProxyHost{{ID: 3, Enabled: true, DomainNames: []string{"npm.example.test"}, ForwardHost: "100.64.0.10", ForwardPort: 443, ForwardScheme: "https"}}
	d.Policy.Grants[0].BrowserSrc = []string{"alice@example.com", "bob@example.com"}
	d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22"), mustGrantCapability(t, "tcp:443"), mustGrantCapability(t, "tcp:8443")}
	d.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: true}, "bob@example.com": {ID: "bob-id", DirectMember: true}}
	d.GrantRoleSelectorsByLogin["bob@example.com"] = []string{"autogroup:member"}
	d.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"node-1": {controlPlaneTailscale, deviceIDKindID, "node-1"}}
	if provider == controlPlaneHeadscale {
		d.Policy.Grants = nil
		d.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"100.64.0.10:*"}}}
		d.Policy.SSH = SSHPolicy{}
		d.TailscaleUsersByLogin = nil
		d.PresentationDeviceIDsByNodeID = nil
	}
	return d
}
func s5Open(t *testing.T, values map[string]string) *s5Fixture {
	t.Helper()
	cfg, err := loadConfigFrom(mapConfigLookup(values))
	if err != nil {
		t.Fatal(err)
	}
	f := &s5Fixture{cfg: cfg, cache: newTestCache(s5Data(t, cfg.ControlPlane))}
	f.open(t)
	t.Cleanup(func() { _ = f.editor.Close(); _ = f.personal.Close() })
	return f
}
func (f *s5Fixture) open(t *testing.T) {
	t.Helper()
	e, p, err := newPresentationStores(f.cfg)
	if err != nil {
		t.Fatal("configured open", err)
	}
	f.editor, f.personal = e, p
	m, err := boundServiceMetadataLoaderForPath(f.cfg.ServiceMetadataFile, f.cfg.metadataBinding())()
	if err != nil {
		t.Fatal(err)
	}
	if d := f.cache.Get(); d != nil {
		copy := *d
		copy.ServiceMetadata = m
		f.cache.data.Store(&copy)
	}
	f.mux, err = newServerMux(f.cfg, f.cache, nil, e, p)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *s5Fixture) restart(t *testing.T, cold bool) {
	t.Helper()
	if err := f.editor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.personal.Close(); err != nil {
		t.Fatal(err)
	}
	if cold {
		f.cache = newTestCache(nil)
	} else {
		f.cache = newTestCache(s5Data(t, f.cfg.ControlPlane))
	}
	f.open(t)
}
func (f *s5Fixture) request(body string, login string) *http.Request {
	r := mcpTestRequest(body, true)
	r.Host = "portal.example:8081"
	r.URL.Host = r.Host
	r.Header.Set("Tailscale-User-Login", login)
	return r
}
func s5Envelope(t *testing.T, method string, params any) string {
	return string(s4hJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}))
}
func (f *s5Fixture) rpc(t *testing.T, method string, params any, login string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := f.request(s5Envelope(t, method, params), login)
	if method == "initialize" {
		r.Header.Del("MCP-Protocol-Version")
	}
	f.mux.ServeHTTP(w, r)
	return w
}
func s5ToolResponse(t *testing.T, w *httptest.ResponseRecorder, want string) json.RawMessage {
	t.Helper()
	var v struct {
		ID     json.RawMessage `json:"id"`
		Error  *mcpRPCError    `json:"error"`
		Result struct {
			Structured json.RawMessage  `json:"structuredContent"`
			Content    []mcpTextContent `json:"content"`
			IsError    bool             `json:"isError"`
		} `json:"result"`
	}
	if w.Code != 200 || w.Body.Len() > mcpMaxResponseBytes || json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Error != nil {
		t.Fatalf("RPC result: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") || w.Header().Get("Access-Control-Allow-Origin") != "" || w.Header().Get("Mcp-Session-Id") != "" {
		t.Fatal("response headers")
	}
	if len(v.Result.Content) != 1 || v.Result.Content[0].Type != "text" || !bytes.Equal(v.Result.Structured, []byte(v.Result.Content[0].Text)) {
		t.Fatal("partial or divergent dual encoding")
	}
	if want == "" {
		if v.Result.IsError {
			t.Fatalf("unexpected tool error %s", v.Result.Structured)
		}
	} else if !v.Result.IsError || string(v.Result.Structured) != `{"error":"`+want+`"}` {
		t.Fatalf("got %s, want %s", v.Result.Structured, want)
	}
	for _, canary := range []string{"alice-id", "bob-id", "TailscaleUsersByLogin", "MachineSSHCapableByID", "PresentationDeviceIDsByNodeID", "private_file_revision", "internalCache", "ForwardHost", "GrantRoleSelectorsByLogin", "sha256:"} {
		if bytes.Contains(v.Result.Structured, []byte(canary)) {
			t.Fatal("internal evidence leaked", canary)
		}
	}
	return v.Result.Structured
}
func (f *s5Fixture) call(t *testing.T, name string, args any, want string) json.RawMessage {
	t.Helper()
	return s5ToolResponse(t, f.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args}, "alice@example.com"), want)
}
func (f *s5Fixture) names(t *testing.T) []string {
	t.Helper()
	w := f.rpc(t, "tools/list", map[string]any{}, "alice@example.com")
	var v struct {
		Result struct {
			Tools []any `json:"tools"`
		} `json:"result"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal("discovery", w.Code, w.Body.String())
	}
	return s4hNames(t, v.Result.Tools)
}
func (f *s5Fixture) read(t *testing.T) mcpPersonalResult {
	t.Helper()
	var s mcpPersonalResult
	b := f.call(t, "personal_read", map[string]any{}, "")
	if json.Unmarshal(b, &s) != nil {
		t.Fatal("personal wire")
	}
	return s
}
func (f *s5Fixture) personalCall(t *testing.T, name string, args any) mcpPersonalResult {
	t.Helper()
	var s mcpPersonalResult
	if json.Unmarshal(f.call(t, name, args, ""), &s) != nil {
		t.Fatal("personal wire")
	}
	return s
}
func (f *s5Fixture) create(t *testing.T) PersonalServicesState {
	t.Helper()
	if f.personal.storage != nil {
		s := f.read(t)
		return f.personalCall(t, "personal_create", s4hMutationArgs(s, "personal_create")).PersonalServicesState
	}
	s := personalHTTPRead(t, f.mux)
	req := personalTestMutation(s.PersonalServicesState, "create")
	req.Name = "Own application"
	req.URL = "https://server.tailnet.ts.net/app"
	req.Icon = ""
	req.CSRF = s.CSRF
	w, out := personalHTTPPost(t, f.mux, req)
	if w.Code != 200 {
		t.Fatal("RAM browser create", w.Code, w.Body.String())
	}
	return out.PersonalServicesState
}
func (f *s5Fixture) page(t *testing.T, kind sharedTargetKind) sharedTargetPage {
	t.Helper()
	var p sharedTargetPage
	if json.Unmarshal(f.call(t, "shared_targets_list", map[string]any{"kind": kind}, ""), &p) != nil {
		t.Fatal("page")
	}
	return p
}
func (f *s5Fixture) form(t *testing.T, ref sharedTargetRef) sharedTargetForm {
	t.Helper()
	var v sharedTargetForm
	if json.Unmarshal(f.call(t, "shared_target_get", map[string]any{"target": ref}, ""), &v) != nil {
		t.Fatal("form")
	}
	return v
}
func s5IconArgs(form sharedTargetForm, icon string) map[string]any {
	a := map[string]any{"target": form.Target, "revision": form.Revision, "target_revision": form.TargetRevision}
	if icon != "" {
		a["icon"] = icon
	}
	return a
}
func s5Bytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestS5ConfiguredMuxMatrix(t *testing.T) {
	for _, provider := range []string{"headscale", "tailscale"} {
		for _, mode := range []string{"off", "browser", "shared"} {
			for _, personal := range []string{"off", "ram", "durable"} {
				for _, mcp := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/mcp=%v", provider, mode, personal, mcp), func(t *testing.T) {
						v := s5Values(t, provider, mode, personal, mcp, 4, nil)
						if provider == "headscale" && personal != "off" {
							if _, e := loadConfigFrom(mapConfigLookup(v)); e == nil {
								t.Fatal("Headscale personal accepted")
							}
							for _, key := range []string{"SERVICE_METADATA_FILE", "PORTAL_PERSONAL_SERVICES_FILE"} {
								if path := v[key]; path != "" {
									if _, e := os.Stat(path + ".lock"); !errors.Is(e, os.ErrNotExist) {
										t.Fatal("invalid config initialized writer")
									}
								}
							}
							return
						}
						before := s5Bytes(t, v["SERVICE_METADATA_FILE"])
						f := s5Open(t, v)
						if (f.editor != nil) != (mode == "shared") || (f.personal != nil) != (personal != "off") {
							t.Fatal("optional dependencies")
						}
						if !bytes.Equal(before, s5Bytes(t, v["SERVICE_METADATA_FILE"])) {
							t.Fatal("startup migration")
						}
						for _, path := range []string{"/", "/portal", "/unknown", "/api/shared-targets", "/mcp/"} {
							r := personalHTTPRequestAt("GET", path)
							w := httptest.NewRecorder()
							f.mux.ServeHTTP(w, r)
							want := 404
							if path == "/" || path == "/portal" {
								want = 200
							}
							if w.Code != want {
								t.Fatal("exact route", path, w.Code)
							}
						}
						w := f.rpc(t, "initialize", map[string]any{"protocolVersion": "old", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "synthetic", "version": "1"}}, "alice@example.com")
						if !mcp {
							if w.Code != 404 {
								t.Fatal("disabled MCP present", w.Code)
							}
							return
						}
						if w.Code != 200 || !strings.Contains(w.Body.String(), mcpProtocolVersion) {
							t.Fatal("initialize", w.Code)
						}
						want := []string{"icons_list"}
						if personal == "durable" {
							want = append(want, s4hToolNames[1:5]...)
						}
						if mode == "shared" {
							want = append(want, s4hToolNames[5:]...)
						}
						if got := f.names(t); !reflect.DeepEqual(got, want) {
							t.Fatal("capabilities", got, want)
						}
						f.call(t, "icons_list", map[string]any{}, "")
						if personal != "durable" {
							f.call(t, "personal_read", map[string]any{}, "unavailable")
						}
						if mode != "shared" {
							f.call(t, "shared_targets_list", map[string]any{"kind": "npm"}, "unavailable")
						}
						if mode == "shared" && provider == "headscale" {
							if len(f.page(t, sharedTargetMachine).Targets) != 0 {
								t.Fatal("Headscale exposed machine targets")
							}
							f.call(t, "shared_targets_list", map[string]any{"kind": "application"}, "not_found")
							if len(f.page(t, sharedTargetNPM).Targets) != 1 {
								t.Fatal("Headscale not NPM-only")
							}
						}
						if mode != "shared" {
							if _, e := os.Stat(v["SERVICE_METADATA_FILE"] + ".lock"); !errors.Is(e, os.ErrNotExist) {
								t.Fatal("MCP initialized writer")
							}
						}
					})
				}
			}
		}
	}
}

func TestS5MCPAloneInitializesNoStore(t *testing.T) {
	v := namespaceConfigValues("tailscale", "off")
	v["PORTAL_MCP_ORIGIN"] = s5Origin
	cfg, e := loadConfigFrom(mapConfigLookup(v))
	if e != nil {
		t.Fatal(e)
	}
	editor, personal, e := newPresentationStores(cfg)
	if e != nil || editor != nil || personal != nil || cfg.PortalEditMode != serviceEditOff || cfg.StateNamespace != "" {
		t.Fatal("MCP alone gained storage", e)
	}
	mux, e := newServerMux(cfg, newTestCache(s5Data(t, controlPlaneTailscale)), nil, editor, personal)
	if e != nil {
		t.Fatal(e)
	}
	f := &s5Fixture{cfg: cfg, mux: mux}
	if !reflect.DeepEqual(f.names(t), []string{"icons_list"}) {
		t.Fatal("MCP alone gained capabilities")
	}
	f.call(t, "personal_read", map[string]any{}, "unavailable")
}

func TestS5ConfiguredSharedAllKindsPromotionRestartRender(t *testing.T) {
	for _, personal := range []string{"ram", "durable"} {
		for _, version := range []int{1, 2, 3, 4} {
			t.Run(fmt.Sprintf("%s/v%d", personal, version), func(t *testing.T) {
				f := s5Open(t, s5Values(t, "tailscale", "shared", personal, true, version, nil))
				state := f.create(t)
				original := cloneServiceMetadataDocument(f.editor.view.Load().metadata.document)
				privateBefore := personalHTTPRead(t, f.mux).PersonalServicesState
				for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
					page := f.page(t, kind)
					if len(page.Targets) != 1 {
						t.Fatal("public target count", kind, len(page.Targets))
					}
					form := f.form(t, page.Targets[0].Target)
					if got := string(f.call(t, "shared_icon_set", s5IconArgs(form, "grafana"), "")); got != `{"status":"saved"}` {
						t.Fatal("save shape", got)
					}
					got := f.form(t, form.Target)
					if got.SharedIcon != "grafana" || got.EffectiveIcon != "grafana" {
						t.Fatal("saved read-back", kind, got)
					}
					f.call(t, "shared_icon_set", s5IconArgs(form, "generic"), "conflict")
				}
				now := f.editor.view.Load()
				if now.metadata.document.Version != 4 || now.metadata.document.Namespace != s5Namespace {
					t.Fatal("explicit promotion missing")
				}
				if !reflect.DeepEqual(original.Services[1], now.metadata.document.Services[1]) || now.metadata.document.Services[0].Name == nil || !now.metadata.document.Services[0].URLNull {
					t.Fatal("icon operation lost unrelated/null fields")
				}
				if got := personalHTTPRead(t, f.mux).PersonalServicesState; !reflect.DeepEqual(got, privateBefore) || got.Document.Entries[0].Icon != "" {
					t.Fatal("shared default persisted privately")
				}
				page := sharedIconResponse(t, f.mux, "alice@example.com", "/portal").Body.String()
				for _, marker := range []string{`data-service-id="3"`, `data-personal-service="` + state.Document.Entries[0].ID + `"`, `data-machine="server.tailnet.ts.net"`} {
					sharedIconAssert(t, sharedIconCard(t, page, marker), "grafana")
				}
				disk := s5Bytes(t, f.cfg.ServiceMetadataFile)
				if m, e := loadBoundServiceMetadataFile(f.cfg.ServiceMetadataFile, f.cfg.metadataBinding()); e != nil || !reflect.DeepEqual(m.document, now.metadata.document) {
					t.Fatal("bound reload", e)
				}
				f.restart(t, false)
				if !bytes.Equal(disk, s5Bytes(t, f.cfg.ServiceMetadataFile)) {
					t.Fatal("restart rewrote metadata")
				}
				if personal == "ram" {
					state = f.create(t)
				} else {
					loaded := f.read(t)
					if !reflect.DeepEqual(loaded.Document, privateBefore.Document) || loaded.Generation == privateBefore.Generation {
						t.Fatal("offline durable restart")
					}
				}
				for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
					p := f.page(t, kind)
					if len(p.Targets) != 1 {
						t.Fatal("restart targets", kind)
					}
					form := f.form(t, p.Targets[0].Target)
					if form.SharedIcon != "grafana" {
						t.Fatal("restart shared default", kind)
					}
					f.call(t, "shared_icon_reset", s5IconArgs(form, ""), "")
					if f.form(t, form.Target).SharedIcon != "" {
						t.Fatal("reset lost")
					}
				}
				if f.editor.view.Load().metadata.document.Version != 4 {
					t.Fatal("reset downgraded")
				}
			})
		}
	}
}

func TestS5ExplicitLegacyResetAndBrowserVersionDecision(t *testing.T) {
	for _, provider := range []string{"headscale", "tailscale"} {
		for _, version := range []int{1, 2, 3} {
			for _, action := range []string{"icon-reset", "browser-save", "browser-reset"} {
				t.Run(fmt.Sprintf("%s/v%d/%s", provider, version, action), func(t *testing.T) {
					f := s5Open(t, s5Values(t, provider, "shared", "off", true, version, nil))
					want := 3
					if action == "icon-reset" {
						p := f.page(t, sharedTargetNPM)
						if len(p.Targets) != 1 {
							t.Fatal("NPM list")
						}
						form := f.form(t, p.Targets[0].Target)
						f.call(t, "shared_icon_reset", s5IconArgs(form, ""), "")
						want = 4
					} else {
						form := editForm(t, f.mux, "3", "alice@example.com")
						a := strings.TrimPrefix(action, "browser-")
						w := editHTTPRequest(f.mux, "POST", "3", "alice@example.com", editBody(form, a), nil)
						if w.Code != 200 {
							t.Fatal(w.Code, w.Body.String())
						}
					}
					if f.editor.view.Load().metadata.document.Version != want {
						t.Fatal("wrong explicit version decision")
					}
					f.restart(t, false)
					if f.editor.view.Load().metadata.document.Version != want {
						t.Fatal("restart version")
					}
				})
			}
		}
	}
}

func TestS5ConfiguredV4BrowserLosslessAndCrossTransportCAS(t *testing.T) {
	for _, action := range []string{"save", "reset", "blank"} {
		t.Run(action, func(t *testing.T) {
			v := s5Values(t, "tailscale", "shared", "off", true, 4, nil)
			document := strings.ReplaceAll(metadataV4Fixture, "portal-demo-01", s5Namespace)
			if e := os.WriteFile(v["SERVICE_METADATA_FILE"], []byte(document), 0600); e != nil {
				t.Fatal(e)
			}
			f := s5Open(t, v)
			old := f.editor.view.Load()
			before := cloneServiceMetadataDocument(old.metadata.document)
			page := f.page(t, sharedTargetNPM)
			form := f.form(t, page.Targets[0].Target)
			browser := editForm(t, f.mux, "3", "alice@example.com")
			body := editBody(browser, action)
			if action == "blank" {
				body = string(s4hJSON(t, map[string]string{"action": "save", "revision": browser.Revision, "target_revision": browser.TargetRevision, "csrf": browser.CSRF, "name": "", "url": "", "icon": ""}))
			}
			w := editHTTPRequest(f.mux, "POST", "3", "alice@example.com", body, nil)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			expected, e := mutateServiceMetadata(before, 3, "Shared Wiki", "https://shared.example/path", "generic", false)
			if action != "save" {
				expected, e = mutateServiceMetadata(before, 3, "", "", "", action == "reset")
			}
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := serializeServiceMetadataDocument(expected)
			if !bytes.Equal(raw, s5Bytes(t, f.cfg.ServiceMetadataFile)) {
				t.Fatal("full browser lost complete v4")
			}
			f.call(t, "shared_icon_set", s5IconArgs(form, "grafana"), "conflict")
			browser = editForm(t, f.mux, "3", "alice@example.com")
			form = f.form(t, form.Target)
			f.call(t, "shared_icon_set", s5IconArgs(form, "gitea"), "")
			disk := s5Bytes(t, f.cfg.ServiceMetadataFile)
			w = editHTTPRequest(f.mux, "POST", "3", "alice@example.com", editBody(browser, "save"), nil)
			if w.Code != 409 || !bytes.Equal(disk, s5Bytes(t, f.cfg.ServiceMetadataFile)) {
				t.Fatal("MCP/browser CAS lost update")
			}
			if !reflect.DeepEqual(old.metadata.document, before) {
				t.Fatal("prior immutable metadata changed")
			}
			f.restart(t, false)
			if !bytes.Equal(disk, f.editor.view.Load().bytes) {
				t.Fatal("full configured restart")
			}
		})
	}
}

func TestS5BoundReadonlyStartupReloadAndDefaults(t *testing.T) {
	for _, provider := range []string{"headscale", "tailscale"} {
		for _, mode := range []string{"off", "browser"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				v := s5Values(t, provider, mode, "off", true, 4, nil)
				raw := strings.Replace(s5Document(provider, 4), `"name":"Original"`, `"name":"Original","icon":"gitea"`, 1)
				if e := os.WriteFile(v["SERVICE_METADATA_FILE"], []byte(raw), 0600); e != nil {
					t.Fatal(e)
				}
				f := s5Open(t, v)
				page := sharedIconResponse(t, f.mux, "alice@example.com", "/portal").Body.String()
				sharedIconAssert(t, sharedIconCard(t, page, `data-service-id="3"`), "gitea")
				if f.editor != nil {
					t.Fatal("readonly writer")
				}
				if _, e := os.Stat(f.cfg.ServiceMetadataFile + ".lock"); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("readonly lock")
				}
				old := f.cache.Get()
				f.cache.serviceMetadataLoader = boundServiceMetadataLoaderForPath(f.cfg.ServiceMetadataFile, f.cfg.metadataBinding())
				wrong := strings.ReplaceAll(raw, s5Namespace, "different-source")
				if e := os.WriteFile(f.cfg.ServiceMetadataFile, []byte(wrong), 0600); e != nil {
					t.Fatal(e)
				}
				if e := f.cache.refresh(context.Background()); e == nil || f.cache.Get() != old {
					t.Fatal("mismatched reload published partial snapshot")
				}
				if e, p, err := newPresentationStores(f.cfg); err == nil || e != nil || p != nil {
					t.Fatal("mismatch startup accepted")
				}
				if !bytes.Equal([]byte(wrong), s5Bytes(t, f.cfg.ServiceMetadataFile)) {
					t.Fatal("binding mismatch auto-repaired")
				}
			})
		}
	}
}

func TestS5WriterTypedTransitionAndBoundCandidate(t *testing.T) {
	// Writer-only evidence: these callbacks do NOT stand in for operation authority.
	for _, version := range []int{1, 2, 3, 4} {
		for _, test := range []string{"npm", "icon", "zero", "unknown", "nil-final", "npm-promote", "icon-legacy", "downgrade", "rebind", "missing-binding", "wrong-provider", "bad-unseen"} {
			t.Run(fmt.Sprintf("v%d/%s", version, test), func(t *testing.T) {
				f := s5Open(t, s5Values(t, "tailscale", "shared", "off", true, version, nil))
				e := f.editor
				old := e.view.Load()
				kind := serviceMetadataWriteNPM
				d, err := mutateServiceMetadata(old.metadata.document, 3, "Next", "", "generic", false)
				if err != nil {
					t.Fatal(err)
				}
				good := test == "npm" || test == "icon"
				finalCalled := false
				final := func() error { finalCalled = true; return nil }
				switch test {
				case "icon":
					kind = serviceMetadataWriteIcon
					d, err = sharedIconDocumentV4(d, e.config.Binding)
				case "zero":
					kind = 0
				case "unknown":
					kind = 255
				case "nil-final":
					final = nil
				case "npm-promote":
					d, err = sharedIconDocumentV4(d, e.config.Binding)
					good = version == 4
				case "icon-legacy":
					kind = serviceMetadataWriteIcon
					d = serviceMetadataDocument{Version: 3, Services: []serviceMetadataEntry{{ProxyHostID: 3, Name: &[]string{"Next"}[0]}}}
				case "downgrade":
					d = serviceMetadataDocument{Version: 2, Services: []serviceMetadataEntry{{ProxyHostID: 3, Name: &[]string{"Next"}[0]}}}
				case "rebind":
					kind = serviceMetadataWriteIcon
					d, err = sharedIconDocumentV4(d, e.config.Binding)
					d.Namespace = "different"
				case "missing-binding":
					kind = serviceMetadataWriteIcon
					d, err = sharedIconDocumentV4(d, e.config.Binding)
					e.config.Binding.Namespace = ""
				case "wrong-provider":
					kind = serviceMetadataWriteIcon
					d, err = sharedIconDocumentV4(d, e.config.Binding)
					e.config.Binding.ControlPlane = controlPlaneHeadscale
				case "bad-unseen":
					d.Services[1].ProxyHostID = 0
				}
				if err != nil {
					t.Fatal(err)
				}
				effects := 0
				e.failpoint = func(string) error { effects++; return nil }
				e.mu.Lock()
				err = e.commit(old.revision, d, kind, final)
				e.mu.Unlock()
				if (err == nil) != good || finalCalled != good {
					t.Fatal("transition", test, err, finalCalled)
				}
				if !good {
					if effects != 0 || !bytes.Equal(old.bytes, s5Bytes(t, e.path)) || e.view.Load() != old || e.disabled {
						t.Fatal("rejected transition had effects")
					}
				} else {
					if _, err := parseBoundRuntimeServiceMetadata(s5Bytes(t, e.path), e.config.Binding); err != nil {
						t.Fatal("writer persisted unreadable candidate", err)
					}
				}
			})
		}
	}
}

func TestS5PersonalPublicCRUDOfflineRestartAndCAS(t *testing.T) {
	f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 3, nil))
	first := f.read(t)
	if first.Initialized || first.Revision != "0" {
		t.Fatal("absent state")
	}
	create := s4hMutationArgs(first, "personal_create")
	saved := f.personalCall(t, "personal_create", create)
	if len(saved.Document.Entries) != 1 || saved.Projection[0].Status != "active" || saved.Document.Entries[0].Icon != "" {
		t.Fatal("public create")
	}
	f.call(t, "personal_create", create, "conflict")
	f.restart(t, true)
	f.call(t, "personal_read", map[string]any{}, "unavailable")
	f.cache.data.Store(s5Data(t, controlPlaneTailscale))
	loaded := f.read(t)
	if loaded.Generation == saved.Generation || loaded.StoreID != saved.StoreID || loaded.Revision != saved.Revision || !reflect.DeepEqual(loaded.Document, saved.Document) {
		t.Fatal("browser-offline reload")
	}
	for _, field := range []string{"store_id", "generation", "revision"} {
		args := s4hMutationArgs(loaded, "personal_update")
		switch field {
		case "store_id":
			args[field] = strings.Repeat("b", 32)
		case "generation":
			args[field] = saved.Generation
		case "revision":
			args[field] = "0"
		}
		f.call(t, "personal_update", args, "conflict")
	}
	d := s5Data(t, controlPlaneTailscale)
	d.Nodes = nil
	f.cache.data.Store(d)
	inactive := f.read(t)
	if inactive.Projection[0].Status != "unavailable" {
		t.Fatal("inactive management")
	}
	update := s4hMutationArgs(inactive, "personal_update")
	update["name"] = "Inactive repaired"
	updated := f.personalCall(t, "personal_update", update)
	deleted := f.personalCall(t, "personal_delete", s4hMutationArgs(updated, "personal_delete"))
	if !deleted.Initialized || len(deleted.Document.Entries) != 0 {
		t.Fatal("initialized empty")
	}
	f.restart(t, false)
	empty := f.read(t)
	if !empty.Initialized || len(empty.Document.Entries) != 0 || empty.Revision != deleted.Revision {
		t.Fatal("deleted records resurrected")
	}
	for _, name := range []string{"personal_restore", "personal_import"} {
		w := f.rpc(t, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}}, "alice@example.com")
		if !strings.Contains(w.Body.String(), `"code":-32602`) {
			t.Fatal("MCP import exists")
		}
	}
}

func TestS5RetainedDescriptorsValidCASRevocation(t *testing.T) {
	for _, target := range []string{"personal", "npm", "machine", "application"} {
		for _, change := range []string{"owner-reassigned", "owner-removed", "shared-user", "stale", "provider", "allowlist", "cross-identity", "grant", "ssh", "capability"} {
			t.Run(target+"/"+change, func(t *testing.T) {
				f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 4, nil))
				f.create(t)
				if !reflect.DeepEqual(f.names(t), s4hToolNames) {
					t.Fatal("initial nine descriptors")
				}
				name := "personal_update"
				args := s4hMutationArgs(f.read(t), name)
				if target != "personal" {
					p := f.page(t, sharedTargetKind(target))
					if len(p.Targets) != 1 {
						t.Fatal("target")
					}
					name = "shared_icon_set"
					args = s5IconArgs(f.form(t, p.Targets[0].Target), "grafana")
				}
				private := s5Bytes(t, f.personal.config.File)
				shared := s5Bytes(t, f.editor.path)
				d := s5Data(t, controlPlaneTailscale)
				login := "alice@example.com"
				switch change {
				case "owner-reassigned":
					d.TailscaleUsersByLogin[login] = TailscaleUserIdentity{ID: "replacement", DirectMember: true}
				case "owner-removed":
					delete(d.TailscaleUsersByLogin, login)
				case "shared-user":
					d.TailscaleUsersByLogin[login] = TailscaleUserIdentity{ID: "alice-id", DirectMember: false}
					d.GrantRoleSelectorsByLogin = nil
				case "stale":
					d.UpdatedAt = time.Now().Add(-time.Hour)
				case "provider":
					d.ControlPlane.Provider = controlPlaneHeadscale
				case "allowlist":
					f.editor.config.Logins = map[string]bool{}
				case "cross-identity":
					login = "bob@example.com"
				case "grant":
					d.Policy.Grants = nil
				case "ssh":
					d.Policy.SSH = SSHPolicy{}
				case "capability":
					d.MachineSSHCapableByID = nil
				}
				f.cache.data.Store(d)
				denied := change == "stale" || change == "provider" || change == "cross-identity" || change == "shared-user" && target != "npm" || strings.HasPrefix(change, "owner-") && (target == "personal" || target == "application") || change == "allowlist" && target != "personal" || change == "grant" && target != "personal" || (change == "ssh" || change == "capability") && target == "machine"
				w := f.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args}, login)
				if denied {
					// A different direct member has a separate collection/CAS: their
					// valid identity may fail with conflict, never read or mutate Alice's state.
					crossPersonal := target == "personal" && change == "cross-identity"
					if crossPersonal {
						s5ToolResponse(t, w, "conflict")
					}
					if !strings.Contains(w.Body.String(), `"isError":true`) || (!crossPersonal && strings.Contains(w.Body.String(), `"error":"conflict"`)) {
						t.Fatal("revocation not isolated from CAS", w.Body.String())
					}
					if !bytes.Equal(private, s5Bytes(t, f.personal.config.File)) || !bytes.Equal(shared, s5Bytes(t, f.editor.path)) {
						t.Fatal("revoked operation wrote")
					}
				} else {
					s5ToolResponse(t, w, "")
				}
			})
		}
	}
}

func TestS5ActualDispatchTransportAndNotifications(t *testing.T) {
	changes := map[string]func(*http.Request){
		"raw-port": func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1234" }, "missing-human": func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }, "padded": func(r *http.Request) { r.Header.Set("Tailscale-User-Login", " alice@example.com") }, "duplicate": func(r *http.Request) { r.Header["tailscale-user-login"] = []string{"alice@example.com"} }, "host": func(r *http.Request) { r.Host = "other.example" }, "origin": func(r *http.Request) { r.Header.Set("Origin", "null") }, "fetch": func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }, "authorization": func(r *http.Request) { r.Header["Authorization"] = []string{""} }, "cookie": func(r *http.Request) { r.Header["Cookie"] = []string{""} }, "session": func(r *http.Request) { r.Header["Mcp-Session-Id"] = []string{""} }, "version": func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "old") }, "framing": func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }, "cancel": func(r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			*r = *r.WithContext(ctx)
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 3, nil))
			args := s4hMutationArgs(f.read(t), "personal_create")
			before := s5Bytes(t, f.personal.config.File)
			r := f.request(s5Envelope(t, "tools/call", map[string]any{"name": "personal_create", "arguments": args}), "alice@example.com")
			change(r)
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			if w.Code == 200 && !strings.Contains(w.Body.String(), `"isError":true`) {
				t.Fatal("transport admitted", w.Body.String())
			}
			if !bytes.Equal(before, s5Bytes(t, f.personal.config.File)) || len(f.personal.requests) != 0 {
				t.Fatal("transport side effects or slot leak")
			}
		})
	}
	f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 3, nil))
	state := f.read(t)
	p := f.page(t, sharedTargetNPM)
	form := f.form(t, p.Targets[0].Target)
	private := s5Bytes(t, f.personal.config.File)
	shared := s5Bytes(t, f.editor.path)
	// Holding both domains proves notifications do not even try private reads/locks.
	for _, name := range s4hToolNames {
		t.Run("notification/"+name, func(t *testing.T) {
			args := s4hArgs(name)
			if name == "personal_create" {
				args = s4hMutationArgs(state, name)
			}
			if name == "shared_icon_set" {
				args = s5IconArgs(form, "grafana")
			}
			if name == "shared_icon_reset" {
				args = s5IconArgs(form, "")
			}
			body := s4hJSON(t, map[string]any{"jsonrpc": "2.0", "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
			f.personal.mu.Lock()
			f.editor.mu.Lock()
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, f.request(string(body), "alice@example.com"))
				done <- w
			}()
			var w *httptest.ResponseRecorder
			select {
			case w = <-done:
			case <-time.After(time.Second):
				f.editor.mu.Unlock()
				f.personal.mu.Unlock()
				t.Fatal("notification entered operation")
			}
			f.editor.mu.Unlock()
			f.personal.mu.Unlock()
			if w.Code != 202 || w.Body.Len() != 0 {
				t.Fatal("notification response")
			}
		})
	}
	if !bytes.Equal(private, s5Bytes(t, f.personal.config.File)) || !bytes.Equal(shared, s5Bytes(t, f.editor.path)) {
		t.Fatal("notification mutated")
	}
	for _, browser := range []bool{false, true} {
		r := f.request(s5Envelope(t, "tools/call", map[string]any{"name": "icons_list", "arguments": map[string]any{}}), "alice@example.com")
		if browser {
			r.Header.Set("Origin", s5Origin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
		}
		w := &mcpDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		f.mux.ServeHTTP(w, r)
		s5ToolResponse(t, w.ResponseRecorder, "")
		if strings.Join(w.events, ",") != "read-set,write-set,flush,write-clear,read-clear" {
			t.Fatal("deadline lifecycle", w.events)
		}
	}
}

func TestS5PublicSharedFinalChecksReplacementSnapshot(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		for _, change := range []string{"identical", "stale", "owner-replaced", "grant", "ssh", "capability", "device-replaced", "request-cancel", "allowlist"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 3, nil))
				f.create(t)
				p := f.page(t, kind)
				form := f.form(t, p.Targets[0].Target)
				old := f.editor.view.Load()
				a := f.cache.Get()
				aBefore := s5Data(t, controlPlaneTailscale)
				aBefore.UpdatedAt = a.UpdatedAt
				var err error
				aBefore.ServiceMetadata, err = parseBoundRuntimeServiceMetadata(old.bytes, f.cfg.metadataBinding())
				if err != nil || !reflect.DeepEqual(a, aBefore) {
					t.Fatal("independent snapshot copy", err)
				}
				r := f.request(s5Envelope(t, "tools/call", map[string]any{"name": "shared_icon_set", "arguments": s5IconArgs(form, "grafana")}), "alice@example.com")
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				hit := false
				f.editor.failpoint = func(stage string) error {
					if stage == "recheck" {
						hit = true
						b := s5Data(t, controlPlaneTailscale)
						switch change {
						case "stale":
							b.UpdatedAt = time.Now().Add(-time.Hour)
						case "owner-replaced":
							b.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-owner", DirectMember: true}
						case "grant":
							b.Policy.Grants = nil
						case "ssh":
							b.Policy.SSH = SSHPolicy{}
						case "capability":
							b.MachineSSHCapableByID = nil
						case "device-replaced":
							b.Nodes[0].ID = "node-2"
							b.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"node-2": {controlPlaneTailscale, deviceIDKindID, "node-2"}}
							b.MachineSSHCapableByID = map[string]bool{"node-2": true}
						case "request-cancel":
							cancel()
						case "allowlist":
							f.editor.config.Logins = map[string]bool{}
						}
						f.cache.data.Store(b)
					}
					return nil
				}
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				deny := change == "stale" || change == "grant" || change == "request-cancel" || change == "allowlist" || change == "owner-replaced" && kind == sharedTargetApplication || (change == "ssh" || change == "capability") && kind == sharedTargetMachine || change == "device-replaced" && kind != sharedTargetNPM
				if !hit || f.cache.Get() == a || !reflect.DeepEqual(aBefore, a) {
					t.Fatal("final hook absent or snapshot A mutated")
				}
				if deny {
					if !strings.Contains(w.Body.String(), `"isError":true`) || !bytes.Equal(old.bytes, s5Bytes(t, f.editor.path)) || f.editor.disabled {
						t.Fatal("late denial wrote or claimed uncertainty", w.Body.String())
					}
				} else {
					s5ToolResponse(t, w, "")
				}
			})
		}
	}
}

func TestS5CrossTransportUncertaintyAndIndependentDomains(t *testing.T) {
	for _, source := range []string{"browser", "mcp", "private"} {
		for _, stage := range []string{"attempt-error", "attempt-applied", "dirsync", "publish"} {
			t.Run(source+"/"+stage, func(t *testing.T) {
				f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 4, nil))
				f.create(t)
				personalBefore := f.read(t)
				p := f.page(t, sharedTargetNPM)
				form := f.form(t, p.Targets[0].Target)
				browser := editForm(t, f.mux, "3", "alice@example.com")
				sentinel := errors.New("private-fixture-path-canary")
				fail := func(s string) error {
					if s == stage {
						return sentinel
					}
					return nil
				}
				rename := func(a int, b string, c int, d string) error {
					if stage == "attempt-applied" {
						if e := unix.Renameat(a, b, c, d); e != nil {
							return e
						}
					}
					return sentinel
				}
				if source == "private" {
					f.personal.storage.failpoint = fail
					if strings.HasPrefix(stage, "attempt-") {
						f.personal.storage.rename = rename
					}
					f.call(t, "personal_update", s4hMutationArgs(personalBefore, "personal_update"), "uncertain")
					f.call(t, "personal_read", map[string]any{}, "uncertain")
					w := httptest.NewRecorder()
					f.mux.ServeHTTP(w, personalHTTPRequest("GET", nil))
					if w.Code != 503 || strings.Contains(w.Body.String(), "document") {
						t.Fatal("private uncertain browser read")
					}
					for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
						p := f.page(t, kind)
						form := f.form(t, p.Targets[0].Target)
						f.call(t, "shared_icon_set", s5IconArgs(form, "grafana"), "")
					}
					f.call(t, "shared_targets_list", map[string]any{"kind": "application"}, "uncertain")
					if !reflect.DeepEqual(f.names(t), append([]string{"icons_list"}, s4hToolNames[5:]...)) {
						t.Fatal("private uncertainty hid independent capabilities")
					}
				} else {
					f.editor.failpoint = fail
					if strings.HasPrefix(stage, "attempt-") {
						f.editor.rename = rename
					}
					if source == "browser" {
						w := editHTTPRequest(f.mux, "POST", "3", "alice@example.com", editBody(browser, "save"), nil)
						if w.Code != 503 || !strings.Contains(w.Body.String(), "uncertain") {
							t.Fatal("browser uncertainty", w.Code, w.Body.String())
						}
					} else {
						f.call(t, "shared_icon_set", s5IconArgs(form, "grafana"), "uncertain")
					}
					f.call(t, "shared_icon_set", s5IconArgs(form, "generic"), "uncertain")
					f.call(t, "shared_target_get", map[string]any{"target": form.Target}, "uncertain")
					for _, method := range []string{"GET", "POST"} {
						w := editHTTPRequest(f.mux, method, "3", "alice@example.com", editBody(browser, "save"), nil)
						if w.Code != 503 || strings.Contains(w.Body.String(), "revision") {
							t.Fatal("uncertain shared readback", w.Code)
						}
					}
					if !reflect.DeepEqual(f.names(t), s4hToolNames[:5]) {
						t.Fatal("shared uncertainty affected private discovery")
					}
					f.personalCall(t, "personal_update", s4hMutationArgs(f.read(t), "personal_update"))
				}
			})
		}
	}
}

// Reading each body confirms acquisition of the actual production route slot.
// The first synchronous commit is held after file write; no synthetic dispatcher.
func TestS5ProductionRouteSlotsHeldThroughCommit(t *testing.T) {
	f := s5Open(t, s5Values(t, "tailscale", "shared", "off", true, 3, nil))
	p := f.page(t, sharedTargetNPM)
	form := f.form(t, p.Targets[0].Target)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	f.editor.failpoint = func(stage string) error {
		if stage == "filesync" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	}
	body := s5Envelope(t, "tools/call", map[string]any{"name": "shared_icon_set", "arguments": s5IconArgs(form, "grafana")})
	done := make(chan *httptest.ResponseRecorder, 8)
	read := make(chan struct{}, 8)
	launch := func() {
		r := f.request(body, "alice@example.com")
		r.Body = &mcpObservedSocketBody{ReadCloser: r.Body, entered: read}
		go func() { w := httptest.NewRecorder(); f.mux.ServeHTTP(w, r); done <- w }()
	}
	launch()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("commit did not reach hook")
	}
	<-read
	for i := 0; i < 7; i++ {
		launch()
		select {
		case <-read:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("route did not acquire slot")
		}
	}
	w := f.rpc(t, "ping", map[string]any{}, "alice@example.com")
	if w.Code != 503 {
		close(release)
		t.Fatal("ninth route admitted", w.Code)
	}
	close(release)
	success := 0
	conflict := 0
	for i := 0; i < 8; i++ {
		select {
		case w := <-done:
			if strings.Contains(w.Body.String(), `"isError":false`) {
				success++
			} else if strings.Contains(w.Body.String(), `"error":"conflict"`) {
				conflict++
			} else {
				t.Fatal("queued result", w.Code, w.Body.String())
			}
		case <-time.After(3 * time.Second):
			t.Fatal("route leaked")
		}
	}
	if success != 1 || conflict != 7 {
		t.Fatal("commit/CAS", success, conflict)
	}
	if w := f.rpc(t, "ping", map[string]any{}, "alice@example.com"); w.Code != 200 {
		t.Fatal("slots not released")
	}
}

func TestS5PersonalSlotOnceAndCancellation(t *testing.T) {
	for _, which := range []string{"success", "pre-rename-cancel", "post-sync-cancel"} {
		t.Run(which, func(t *testing.T) {
			f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 3, nil))
			args := s4hMutationArgs(f.read(t), "personal_create")
			before := s5Bytes(t, f.personal.config.File)
			for i := 0; i < 7; i++ {
				f.personal.requests <- struct{}{}
			}
			defer func() {
				for len(f.personal.requests) > 0 {
					<-f.personal.requests
				}
			}()
			r := f.request(s5Envelope(t, "tools/call", map[string]any{"name": "personal_create", "arguments": args}), "alice@example.com")
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			hit := false
			f.personal.storage.failpoint = func(stage string) error {
				if stage == "filesync" {
					hit = true
					if len(f.personal.requests) != 8 {
						t.Error("personal slot acquired more/less than once")
					}
					w := httptest.NewRecorder()
					f.mux.ServeHTTP(w, personalHTTPRequest("GET", nil))
					if w.Code != 503 {
						t.Error("browser not sharing admission")
					}
					if which == "pre-rename-cancel" {
						cancel()
					}
				}
				if stage == "dirsync" && which == "post-sync-cancel" {
					cancel()
				}
				return nil
			}
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			if !hit || len(f.personal.requests) != 7 {
				t.Fatal("admission released incorrectly")
			}
			for i := 0; i < 7; i++ {
				<-f.personal.requests
			}
			if which == "success" {
				s5ToolResponse(t, w, "")
			} else {
				if !strings.Contains(w.Body.String(), `"isError":true`) || strings.Contains(w.Body.String(), `"document"`) {
					t.Fatal("cancel exposed body")
				}
			}
			same := bytes.Equal(before, s5Bytes(t, f.personal.config.File))
			if same != (which == "pre-rename-cancel") {
				t.Fatal("cancellation claimed rollback or wrote early")
			}
			read := f.read(t)
			if read.Initialized != (which != "pre-rename-cancel") {
				t.Fatal("cancel readback")
			}
		})
	}
}

func TestS5ActualLargeResponseAndFinalBuffer(t *testing.T) {
	entries := make([]PersonalService, 128)
	for i := range entries {
		entries[i] = PersonalService{ID: fmt.Sprintf("ps_%032x", i+1), Name: strings.Repeat("<", 120), URL: "https://server.tailnet.ts.net/" + strings.Repeat("a", 200)}
	}
	doc := PersonalServicesDocument{Version: 1, Entries: entries}
	for validatePersonalDocument(doc) != nil {
		for i := range doc.Entries {
			if len(doc.Entries[i].Name) <= 10 {
				t.Fatal("fixture bound")
			}
			doc.Entries[i].Name = doc.Entries[i].Name[1:]
		}
	}
	f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 4, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", doc.Entries)}))
	id := `"` + strings.Repeat(`<`, 256) + `"`
	body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"personal_read","arguments":{}}}`
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, f.request(body, "alice@example.com"))
	raw := s5ToolResponse(t, w, "")
	var got mcpPersonalResult
	if json.Unmarshal(raw, &got) != nil || len(got.Document.Entries) != 128 || len(got.Projection) != 128 {
		t.Fatal("truncated valid personal state")
	}
	if len(s4hJSON(t, doc)) < 64000 {
		t.Fatal("fixture not near 64KiB bound")
	}
	t.Logf("configured public authored=%d response=%d entries=%d", len(s4hJSON(t, doc)), w.Body.Len(), len(got.Document.Entries))
	// The fixed DTOs cannot carry an unsupported Go value. Exercise the exact final
	// production writer separately; this is encoder evidence, not an injected Call.
	for _, name := range []string{"personal_read", "personal_create", "shared_icon_set"} {
		for _, value := range []any{make(chan int), map[string]string{"x": strings.Repeat("<", 100000)}} {
			out := httptest.NewRecorder()
			mcpWriteTool(out, []byte(`1`), mcpToolResult{StructuredContent: value}, name)
			want := "uncertain"
			if name == "personal_read" {
				want = "unavailable"
			}
			var response struct {
				Result mcpToolResult `json:"result"`
			}
			if out.Code != 200 || out.Body.Len() > mcpMaxResponseBytes || json.Unmarshal(out.Body.Bytes(), &response) != nil {
				t.Fatal("final buffer", out.Code)
			}
			s4hCode(t, response.Result, want)
		}
	}
}

func TestS5StrictActualToolDispatch(t *testing.T) {
	f := s5Open(t, s5Values(t, "tailscale", "shared", "durable", true, 4, nil))
	f.create(t)
	if !reflect.DeepEqual(f.names(t), s4hToolNames) {
		t.Fatal("fixed nine tools")
	}
	for _, name := range s4hToolNames {
		for _, raw := range []string{`null`, `[]`, `{"Unexpected":true}`, `{"x":1,"X":2}`, `{"x":"\ud800"}`} {
			t.Run(name+"/"+raw, func(t *testing.T) {
				private := s5Bytes(t, f.personal.config.File)
				shared := s5Bytes(t, f.editor.path)
				body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + raw + `}}`
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, f.request(body, "alice@example.com"))
				code := `"code":-32602`
				if strings.Contains(raw, `"X"`) {
					code = `"code":-32600`
				}
				if !strings.Contains(w.Body.String(), code) {
					t.Fatal("invalid arguments accepted", w.Code, w.Body.String())
				}
				if !bytes.Equal(private, s5Bytes(t, f.personal.config.File)) || !bytes.Equal(shared, s5Bytes(t, f.editor.path)) {
					t.Fatal("invalid args wrote")
				}
			})
		}
	}
	for _, name := range []string{"unknown", "personal_import", "shared_icon_delete"} {
		w := f.rpc(t, "tools/call", map[string]any{"name": name, "arguments": map[string]any{}}, "alice@example.com")
		if !strings.Contains(w.Body.String(), `"code":-32602`) {
			t.Fatal("unknown tool accepted")
		}
	}
}
