package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var s4hToolNames = []string{"icons_list", "personal_read", "personal_create", "personal_update", "personal_delete", "shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset"}

type s4hFixture struct {
	f     *personalOpsFixture
	cfg   *Config
	tools *mcpApplicationTools
}

func newS4HFixture(t *testing.T, records []personalStorageCollection) *s4hFixture {
	t.Helper()
	f := newPersonalOpsFixture(t, records)
	p := f.s.config
	cfg := &Config{ControlPlane: controlPlaneTailscale, StateNamespace: p.Namespace, PersonalServices: &p, MCP: f.config, TrustedProxyCIDR: mcpTrusted(), PollInterval: 30 * time.Second, PortalEditMode: serviceEditOff}
	a := newMCPApplicationTools(cfg, nil, f.cache, f.s)
	a.now = f.now
	return &s4hFixture{f, cfg, a}
}
func s4hJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func s4hKeys(v map[string]any) []string {
	out := []string{}
	for k := range v {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
func s4hArgs(name string) map[string]any {
	cas := map[string]any{"store_id": strings.Repeat("a", 32), "generation": strings.Repeat("b", 32), "revision": "0"}
	switch name {
	case "icons_list", "personal_read":
		return map[string]any{}
	case "personal_create", "personal_update":
		cas["name"] = "Authored <title> & café"
		cas["url"] = "https://server.tailnet.ts.net/app"
		if name == "personal_update" {
			cas["id"] = "ps_" + strings.Repeat("c", 32)
		}
		return cas
	case "personal_delete":
		cas["id"] = "ps_" + strings.Repeat("c", 32)
		return cas
	case "shared_targets_list":
		return map[string]any{"kind": "npm"}
	default:
		v := map[string]any{"target": map[string]any{"kind": "npm", "handle": strings.Repeat("A", 43)}}
		if name != "shared_target_get" {
			v["revision"] = "opaque"
			v["target_revision"] = "opaque-target"
		}
		if name == "shared_icon_set" {
			v["icon"] = "generic"
		}
		return v
	}
}
func s4hMutationArgs(state mcpPersonalResult, name string) map[string]any {
	v := s4hArgs(name)
	v["store_id"] = state.StoreID
	v["generation"] = state.Generation
	v["revision"] = state.Revision
	if name != "personal_create" {
		v["id"] = state.Document.Entries[0].ID
	}
	return v
}
func s4hStructured(t *testing.T, result mcpToolResult) []byte {
	t.Helper()
	b := s4hJSON(t, result.StructuredContent)
	if len(result.Content) != 1 || result.Content[0].Type != "text" || !bytes.Equal(b, []byte(result.Content[0].Text)) {
		t.Fatal("dual representation diverged")
	}
	return b
}
func s4hCode(t *testing.T, result mcpToolResult, want string) {
	t.Helper()
	b := s4hStructured(t, result)
	if !result.IsError || string(b) != `{"error":"`+want+`"}` {
		t.Fatalf("coarse result %s isError=%v want=%s", b, result.IsError, want)
	}
}
func s4hPersonal(t *testing.T, a *mcpApplicationTools, r *http.Request, name string, args any) mcpPersonalResult {
	t.Helper()
	result, err := a.Call(r, name, s4hJSON(t, args))
	if err != nil || result.IsError {
		t.Fatalf("personal operation %s: %v error=%v", name, err, result.IsError)
	}
	b := s4hStructured(t, result)
	var state mcpPersonalResult
	if json.Unmarshal(b, &state) != nil {
		t.Fatal("state encoding")
	}
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if !reflect.DeepEqual(s4hKeys(fields), []string{"document", "durable", "generation", "initialized", "projection", "revision", "store_id"}) {
		t.Fatal("private result field expansion")
	}
	for _, forbidden := range []string{"alice@example.com", "alice-id", "bob@example.com", "bob-id", "fixture-1", "TailscaleUsers", "Policy", "Nodes", "csrf", "identity_scope", "private_file_revision"} {
		if bytes.Contains(b, []byte(forbidden)) {
			t.Fatal("private/topology leak", forbidden)
		}
	}
	return state
}
func s4hNames(t *testing.T, tools []any) []string {
	t.Helper()
	names := []string{}
	for _, v := range tools {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatal("descriptor shape")
		}
		names = append(names, m["name"].(string))
	}
	return names
}

func TestMCPApplicationSchemas(t *testing.T) {
	expected := map[string][]string{
		"icons_list": {}, "personal_read": {}, "personal_create": {"generation", "name", "revision", "store_id", "url"}, "personal_update": {"generation", "id", "name", "revision", "store_id", "url"}, "personal_delete": {"generation", "id", "revision", "store_id"},
		"shared_targets_list": {"kind"}, "shared_target_get": {"target"}, "shared_icon_set": {"icon", "revision", "target", "target_revision"}, "shared_icon_reset": {"revision", "target", "target_revision"},
	}
	for _, name := range s4hToolNames {
		t.Run(name, func(t *testing.T) {
			d := mcpApplicationTool(name)
			if d["name"] != name {
				t.Fatal("name")
			}
			input := d["inputSchema"].(map[string]any)
			props := input["properties"].(map[string]any)
			required := []string{}
			if raw, ok := input["required"]; ok {
				required = append(required, raw.([]string)...)
			}
			sort.Strings(required)
			if !reflect.DeepEqual(required, expected[name]) || input["additionalProperties"] != false || input["type"] != "object" {
				t.Fatal("exact required/schema", required)
			}
			keys := append([]string{}, expected[name]...)
			if name == "personal_create" || name == "personal_update" {
				keys = append(keys, "icon")
			}
			if name == "shared_targets_list" {
				keys = append(keys, "cursor", "limit")
			}
			sort.Strings(keys)
			if !reflect.DeepEqual(s4hKeys(props), keys) {
				t.Fatal("exact properties", s4hKeys(props), keys)
			}
			read := name == "icons_list" || name == "personal_read" || name == "shared_targets_list" || name == "shared_target_get"
			if !reflect.DeepEqual(d["annotations"], map[string]bool{"readOnlyHint": read, "destructiveHint": !read, "idempotentHint": read, "openWorldHint": false}) {
				t.Fatal("truthful annotations")
			}
			if target, ok := props["target"]; ok {
				v := target.(map[string]any)
				if v["additionalProperties"] != false || !reflect.DeepEqual(s4hKeys(v["properties"].(map[string]any)), []string{"handle", "kind"}) {
					t.Fatal("target schema")
				}
			}
			if name == "shared_icon_set" || name == "shared_icon_reset" {
				out := d["outputSchema"].(map[string]any)
				if !reflect.DeepEqual(s4hKeys(out["properties"].(map[string]any)), []string{"status"}) || out["properties"].(map[string]any)["status"].(map[string]any)["const"] != "saved" {
					t.Fatal("exact save result")
				}
			}
			if strings.HasPrefix(name, "personal_") {
				out := d["outputSchema"].(map[string]any)
				if !reflect.DeepEqual(s4hKeys(out["properties"].(map[string]any)), []string{"document", "durable", "generation", "initialized", "projection", "revision", "store_id"}) {
					t.Fatal("personal output expansion")
				}
			}
			if _, err := mcpApplicationArguments(name, s4hJSON(t, s4hArgs(name))); err != nil {
				t.Fatal("schema valid arguments rejected", err)
			}
		})
	}
	if !reflect.DeepEqual(mcpApplicationTool("icons_list"), mcpIconsTool()) || mcpApplicationTool("personal_import") != nil {
		t.Fatal("finite registry changed")
	}
	// Descriptors are detached, not a client-mutable global registry.
	d := mcpApplicationTool("personal_create")
	d["inputSchema"].(map[string]any)["properties"].(map[string]any)["icon"].(map[string]any)["enum"].([]string)[1] = "poison"
	if bytes.Contains(s4hJSON(t, mcpApplicationTool("personal_create")), []byte("poison")) {
		t.Fatal("schema aliases finite registry")
	}
}

func TestMCPApplicationStrictArguments(t *testing.T) {
	for _, name := range s4hToolNames {
		t.Run(name, func(t *testing.T) {
			valid := s4hArgs(name)
			base := string(s4hJSON(t, valid))
			cases := map[string]string{"null": "null", "array": "[]", "scalar": `"x"`, "number": "1", "empty": "", "space": " ", "trailing": base + `{}`, "large": strings.Repeat(" ", mcpMaxRequestBytes) + base,
				"deep": `{"unexpected":` + strings.Repeat("[", 17) + `0` + strings.Repeat("]", 17) + `}`, "invalid-utf8": `{"x":"` + string([]byte{0xff}) + `"}`,
				"surrogate-high": `{"x":"\ud800"}`, "surrogate-low": `{"x":"\udc00"}`, "surrogate-key": `{"\ud800":"x"}`, "duplicate-unknown": `{"x":1,"x":2}`,
			}
			for _, key := range []string{"Document", "document", "action", "login", "owner", "csrf", "authorization", "session", "restore", "import", "url_digest", "device_id", "target_digest", "tuple", "_meta"} {
				v := s4hArgs(name)
				v[key] = "private-sentinel"
				cases["forbidden-"+key] = string(s4hJSON(t, v))
			}
			for key := range valid {
				v := s4hArgs(name)
				v[key] = nil
				cases["null-"+key] = string(s4hJSON(t, v))
				v = s4hArgs(name)
				v[strings.ToUpper(key)] = v[key]
				delete(v, key)
				cases["case-"+key] = string(s4hJSON(t, v))
				cases["duplicate-"+key] = base[:len(base)-1] + `,"` + key + `":` + string(s4hJSON(t, valid[key])) + `}`
				cases["case-duplicate-"+key] = base[:len(base)-1] + `,"` + strings.ToUpper(key) + `":` + string(s4hJSON(t, valid[key])) + `}`
				v = s4hArgs(name)
				delete(v, key)
				cases["missing-"+key] = string(s4hJSON(t, v))
				for _, bad := range []any{true, 123, []any{}, map[string]any{}, "\x00", " padded ", "�"} {
					v = s4hArgs(name)
					v[key] = bad
					// Literal replacement scalars are valid names/opaque shared revisions,
					// not unpaired escapes. Incumbent revision strings are not ID schemas.
					if bad == "�" && (key == "name" || strings.HasPrefix(name, "shared_") && (key == "revision" || key == "target_revision")) {
						continue
					}
					cases[fmt.Sprintf("type-%s-%T-%v", key, bad, bad)] = string(s4hJSON(t, v))
				}
			}
			if name == "icons_list" {
				delete(cases, "empty")
			}
			for label, raw := range cases {
				t.Run(label, func(t *testing.T) {
					if _, err := mcpApplicationArguments(name, []byte(raw)); !errors.Is(err, errMCPInvalidParams) {
						t.Fatal("accepted invalid arguments")
					}
					var a *mcpApplicationTools
					result, err := a.Call(nil, name, []byte(raw))
					if !errors.Is(err, errMCPInvalidParams) || !reflect.DeepEqual(result, mcpToolResult{}) {
						t.Fatal("invalid arguments reached operation")
					}
				})
			}
		})
	}
	for _, name := range []string{"personal_list", "personal_get", "personal_restore", "personal_import", "shared_delete", "Personal_read", "", "icons_list "} {
		if _, err := mcpApplicationArguments(name, []byte(`{}`)); err == nil {
			t.Fatal("unknown tool", name)
		}
	}
}

func TestMCPApplicationStrictSpecificFields(t *testing.T) {
	for _, name := range []string{"shared_target_get", "shared_icon_set", "shared_icon_reset"} {
		for _, kind := range []string{"npm", "machine", "application"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				valid := s4hArgs(name)
				valid["target"].(map[string]any)["kind"] = kind
				if _, err := mcpApplicationArguments(name, s4hJSON(t, valid)); err != nil {
					t.Fatal(err)
				}
				for _, raw := range []string{`null`, `[]`, `{}`, `{"kind":"npm"}`, `{"kind":"npm","handle":null}`, `{"Kind":"npm","handle":"` + strings.Repeat("A", 43) + `"}`, `{"kind":"npm","kind":"npm","handle":"` + strings.Repeat("A", 43) + `"}`, `{"kind":"npm","handle":"\ud800"}`, `{"kind":"npm","handle":"raw-provider-id"}`, `{"kind":"npm","handle":"` + strings.Repeat("A", 42) + `B"}`} {
					v := s4hArgs(name)
					v["target"] = json.RawMessage(raw)
					if _, err := mcpApplicationArguments(name, s4hJSON(t, v)); err == nil {
						t.Fatal("bad nested target", raw)
					}
				}
				for _, key := range []string{"id", "url", "digest", "device_id", "owner", "login", "tuple", "namespace", "handle2"} {
					v := s4hArgs(name)
					v["target"].(map[string]any)[key] = "sentinel"
					if _, err := mcpApplicationArguments(name, s4hJSON(t, v)); err == nil {
						t.Fatal("target extra", key)
					}
				}
			})
		}
	}
	for _, raw := range []string{"0", "null", "-1", "65", "1.0", "1e0", `"1"`, "true", "[]", "{}", "01", "+1", "1e1000"} {
		t.Run("limit/"+raw, func(t *testing.T) {
			if _, err := mcpApplicationArguments("shared_targets_list", []byte(`{"kind":"npm","limit":`+raw+`}`)); err == nil {
				t.Fatal("noncanonical limit")
			}
		})
	}
	for n := 1; n <= 64; n++ {
		if _, err := mcpApplicationArguments("shared_targets_list", []byte(fmt.Sprintf(`{"kind":"npm","limit":%d}`, n))); err != nil {
			t.Fatal("valid limit", n)
		}
	}
	for _, name := range []string{"personal_create", "personal_update", "shared_icon_set"} {
		for _, icon := range append([]string{""}, serviceIconIDs...) {
			v := s4hArgs(name)
			v["icon"] = icon
			_, err := mcpApplicationArguments(name, s4hJSON(t, v))
			if (err != nil) != (name == "shared_icon_set" && icon == "") {
				t.Fatal("finite icon semantics", name, icon)
			}
		}
		for _, raw := range []any{nil, "https://example.com/icon.svg", "Generic", "unknown", 0, true} {
			v := s4hArgs(name)
			v["icon"] = raw
			if _, err := mcpApplicationArguments(name, s4hJSON(t, v)); err == nil {
				t.Fatal("invalid icon")
			}
		}
	}
	for _, name := range []string{"personal_delete", "shared_icon_reset"} {
		for _, key := range []string{"icon", "name", "url", "document"} {
			v := s4hArgs(name)
			v[key] = ""
			if _, err := mcpApplicationArguments(name, s4hJSON(t, v)); err == nil {
				t.Fatal("unused field")
			}
		}
	}
	for _, rev := range []string{"00", "+1", "-1", "1.0", "1e0", "18446744073709551616", " 1", "1 "} {
		v := s4hArgs("personal_create")
		v["revision"] = rev
		if _, err := mcpApplicationArguments("personal_create", s4hJSON(t, v)); err == nil {
			t.Fatal("revision canonical")
		}
	}
	// Valid paired scalars survive, unlike unpaired UTF-16 escapes.
	v := s4hArgs("personal_create")
	v["name"] = json.RawMessage(`"😀"`)
	if _, err := mcpApplicationArguments("personal_create", s4hJSON(t, v)); err != nil {
		t.Fatal("valid pair")
	}
	for _, size := range []int{mcpMaxRequestBytes, mcpMaxRequestBytes + 1} {
		raw := append([]byte(`{}`), bytes.Repeat([]byte(" "), size-2)...)
		_, err := mcpApplicationArguments("personal_read", raw)
		if (err != nil) != (size > mcpMaxRequestBytes) {
			t.Fatal("direct adapter byte boundary")
		}
	}
}

func TestMCPApplicationConfigurationMatrix(t *testing.T) {
	for _, provider := range []controlPlaneProvider{controlPlaneHeadscale, controlPlaneTailscale} {
		for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser, serviceEditShared} {
			for _, personal := range []string{"off", "ram", "durable"} {
				for _, enabled := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/%s/%s/mcp=%v", provider, mode, personal, enabled), func(t *testing.T) {
						f := newS4HFixture(t, []personalStorageCollection{})
						cfg := *f.cfg
						cfg.ControlPlane = provider
						cfg.PortalEditMode = mode
						s := f.f.s
						if personal == "off" {
							cfg.PersonalServices = nil
							s = nil
						} else if personal == "ram" {
							s = personalTestStore(t)
							p := s.config
							cfg.PersonalServices = &p
							cfg.StateNamespace = ""
						}
						if !enabled {
							cfg.MCP = nil
						}
						a := newMCPApplicationTools(&cfg, nil, f.f.cache, s)
						a.now = f.f.now
						r := f.f.request(t, false, "alice@example.com")
						before := personalOpsBytes(t, f.f)
						list, err := a.List(r)
						if !enabled {
							if err == nil || list != nil {
								t.Fatal("MCP off")
							}
						} else {
							if err != nil {
								t.Fatal(err)
							}
							want := []string{"icons_list"}
							if provider == controlPlaneTailscale && personal == "durable" {
								want = append(want, "personal_read", "personal_create", "personal_update", "personal_delete")
							}
							if !reflect.DeepEqual(s4hNames(t, list), want) {
								t.Fatal("capability matrix", s4hNames(t, list), want)
							}
						}
						if !bytes.Equal(before, personalOpsBytes(t, f.f)) {
							t.Fatal("constructor/discovery wrote")
						}
						if s != nil && len(s.requests) != 0 {
							t.Fatal("admission leaked")
						}
					})
				}
			}
		}
	}
}

func TestMCPApplicationCurrentCapabilities(t *testing.T) {
	for _, change := range []string{"fresh", "cold", "stale", "future", "provider", "policy", "owner-removed", "owner-shared", "owner-reassigned", "uncertain", "closed", "saturated", "namespace", "origin", "host", "file", "nil-clock", "zero-interval"} {
		t.Run(change, func(t *testing.T) {
			entry := personalTestEntry(0)
			f := newS4HFixture(t, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", []PersonalService{entry})})
			r := f.f.request(t, false, "alice@example.com")
			first, err := f.tools.List(r)
			if err != nil || len(first) != 5 {
				t.Fatal("initial capability", err)
			}
			d := *f.f.cache.Get()
			switch change {
			case "cold":
				f.f.cache.data.Store(nil)
			case "stale":
				d.UpdatedAt = f.f.at.Add(-91 * time.Second)
				f.f.cache.data.Store(&d)
			case "future":
				d.UpdatedAt = f.f.at.Add(time.Second)
				f.f.cache.data.Store(&d)
			case "provider":
				d.ControlPlane.Provider = controlPlaneHeadscale
				f.f.cache.data.Store(&d)
			case "policy":
				d.Policy = nil
				f.f.cache.data.Store(&d)
			case "owner-removed":
				d.TailscaleUsersByLogin = nil
				f.f.cache.data.Store(&d)
			case "owner-shared":
				d.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: false}}
				f.f.cache.data.Store(&d)
			case "owner-reassigned":
				d.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "new-id", DirectMember: true}}
				f.f.cache.data.Store(&d)
			case "uncertain":
				f.f.s.storage.uncertain = true
			case "closed":
				_ = f.f.s.Close()
			case "saturated":
				for i := 0; i < 8; i++ {
					f.f.s.requests <- struct{}{}
				}
				defer func() {
					for i := 0; i < 8; i++ {
						<-f.f.s.requests
					}
				}()
			case "namespace":
				f.f.s.config.Namespace = "other"
			case "origin":
				f.f.s.config.Origin = "https://other.example"
			case "host":
				f.f.s.config.Host = "other.example"
			case "file":
				f.f.s.config.File = ""
			case "nil-clock":
				f.tools.now = nil
			case "zero-interval":
				f.tools.interval = 0
			}
			list, err := f.tools.List(r)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if change == "fresh" {
				want = 5
			}
			if len(list) != want {
				t.Fatal("retained capability", len(list))
			}
			for _, name := range []string{"personal_read", "personal_create", "personal_update", "personal_delete"} {
				result, err := f.tools.Call(r, name, s4hJSON(t, s4hArgs(name)))
				if err != nil {
					t.Fatal(err)
				}
				if change != "fresh" && !result.IsError {
					t.Fatal("retained descriptor authorized call", name)
				}
				if change != "fresh" {
					b := s4hStructured(t, result)
					if bytes.Contains(b, []byte(entry.Name)) || bytes.Contains(b, []byte("document")) {
						t.Fatal("failed read exposed private state")
					}
				}
			}
		})
	}
}

func TestMCPApplicationActualRequestGuards(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, change := range []string{"valid", "method", "route", "query", "rawpath", "host", "origin", "origin-duplicate", "fetch", "cookie", "authorization", "session", "version", "version-duplicate", "content", "accept", "source", "raw-login", "duplicate-login", "context-login", "cancel"} {
			t.Run(fmt.Sprintf("browser=%v/%s", browser, change), func(t *testing.T) {
				f := newS4HFixture(t, []personalStorageCollection{})
				r := f.f.request(t, browser, "alice@example.com")
				body := &mcpGuardBody{}
				r.Body = body
				switch change {
				case "method":
					r.Method = "GET"
				case "route":
					r.URL.Path = "/mcp/"
				case "query":
					r.URL.ForceQuery = true
				case "rawpath":
					r.URL.RawPath = "/%6dcp"
				case "host":
					r.Host = "other.example"
				case "origin":
					r.Header.Set("Origin", "https://evil.example")
				case "origin-duplicate":
					r.Header["Origin"] = []string{f.cfg.MCP.Origin, f.cfg.MCP.Origin}
				case "fetch":
					r.Header.Set("Sec-Fetch-Site", "cross-site")
				case "cookie":
					r.Header["Cookie"] = []string{""}
				case "authorization":
					r.Header["Authorization"] = []string{""}
				case "session":
					r.Header.Set("Mcp-Session-Id", "sentinel")
				case "version":
					r.Header.Del("Mcp-Protocol-Version")
				case "version-duplicate":
					r.Header["Mcp-Protocol-Version"] = []string{mcpProtocolVersion, mcpProtocolVersion}
				case "content":
					r.Header.Set("Content-Type", "text/plain")
				case "accept":
					r.Header.Set("Accept", "text/html")
				case "source":
					r.RemoteAddr = "192.0.2.8:1234"
				case "raw-login":
					r.Header.Set("Tailscale-User-Login", "bob@example.com")
				case "duplicate-login":
					r.Header["Tailscale-User-Login"] = []string{"alice@example.com", "alice@example.com"}
				case "context-login":
					IdentityFromContext(r.Context()).Login = "bob@example.com"
				case "cancel":
					ctx, cancel := context.WithCancel(r.Context())
					cancel()
					r = r.WithContext(ctx)
				}
				list, err := f.tools.List(r)
				if change == "valid" {
					if err != nil || len(list) != 5 {
						t.Fatal("valid admission")
					}
				} else if err == nil || list != nil {
					t.Fatal("unguarded List")
				}
				for _, name := range s4hToolNames {
					result, err := f.tools.Call(r, name, s4hJSON(t, s4hArgs(name)))
					if err != nil {
						t.Fatal(err)
					}
					if change != "valid" {
						s4hCode(t, result, "forbidden")
					}
				}
				if body.reads != 0 || body.closes != 0 || len(f.f.s.requests) != 0 {
					t.Fatal("adapter touched consumed body or leaked slot")
				}
			})
		}
	}
}

func TestMCPApplicationDurableCRUD(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, inactive := range []bool{false, true} {
			t.Run(fmt.Sprintf("browser=%v/inactive=%v", browser, inactive), func(t *testing.T) {
				f := newS4HFixture(t, []personalStorageCollection{durableRecord("bob@example.com", "bob-id", "9", []PersonalService{{ID: "ps_" + strings.Repeat("d", 32), Name: "FOREIGN_SECRET", URL: "https://foreign.tailnet.ts.net/private"}})})
				r := f.f.request(t, browser, "alice@example.com")
				if inactive {
					d := *f.f.cache.Get()
					d.Nodes = nil
					f.f.cache.data.Store(&d)
				}
				state := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
				if state.Initialized || state.Revision != "0" || len(state.Projection) != 0 {
					t.Fatal("absence")
				}
				create := s4hMutationArgs(state, "personal_create")
				state = s4hPersonal(t, f.tools, r, "personal_create", create)
				if state.Revision != "1" || !state.Initialized || len(state.Projection) != 1 || (state.Projection[0].Status == "active") == inactive || state.Document.Entries[0].Icon != "" {
					t.Fatal("create/action/projection")
				}
				result, err := f.tools.Call(r, "personal_create", s4hJSON(t, create))
				if err != nil {
					t.Fatal(err)
				}
				s4hCode(t, result, "conflict") // Lost response: no replay.
				update := s4hMutationArgs(state, "personal_update")
				update["icon"] = "generic"
				update["name"] = "Own updated"
				state = s4hPersonal(t, f.tools, r, "personal_update", update)
				if state.Revision != "2" || state.Document.Entries[0].Name != "Own updated" || state.Document.Entries[0].Icon != "generic" {
					t.Fatal("update")
				}
				update = s4hMutationArgs(state, "personal_update")
				state = s4hPersonal(t, f.tools, r, "personal_update", update)
				if state.Document.Entries[0].Icon != "" {
					t.Fatal("omitted icon inherits")
				}
				remove := s4hMutationArgs(state, "personal_delete")
				state = s4hPersonal(t, f.tools, r, "personal_delete", remove)
				if !state.Initialized || state.Revision != "4" || len(state.Document.Entries) != 0 || len(state.Projection) != 0 {
					t.Fatal("initialized empty")
				}
				if got := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{}); !reflect.DeepEqual(got, state) {
					t.Fatal("confirmed readback")
				}
				var snapshot personalStorageSnapshot
				if json.Unmarshal(personalOpsBytes(t, f.f), &snapshot) != nil {
					t.Fatal("disk")
				}
				if len(snapshot.Collections) != 2 || snapshot.Collections[1].Revision != "9" {
					t.Fatal("foreign collection changed")
				}
			})
		}
	}
}

func TestMCPApplicationOptionalFieldsStrict(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		value     any
	}{{"personal_create", "icon", "generic"}, {"personal_update", "icon", "generic"}, {"shared_targets_list", "cursor", ""}, {"shared_targets_list", "limit", 64}} {
		t.Run(tc.name+"/"+tc.key, func(t *testing.T) {
			v := s4hArgs(tc.name)
			v[tc.key] = tc.value
			base := string(s4hJSON(t, v))
			for _, raw := range []string{base[:len(base)-1] + `,"` + tc.key + `":null}`, base[:len(base)-1] + `,"` + strings.ToUpper(tc.key) + `":null}`} {
				if _, err := mcpApplicationArguments(tc.name, []byte(raw)); err == nil {
					t.Fatal("duplicate optional field")
				}
			}
			for _, bad := range []any{nil, true, []any{}, map[string]any{}} {
				v[tc.key] = bad
				if _, err := mcpApplicationArguments(tc.name, s4hJSON(t, v)); err == nil {
					t.Fatal("null/type optional field")
				}
			}
		})
	}
}

func TestMCPApplicationConstructorMismatch(t *testing.T) {
	for _, change := range []string{"nil", "provider", "personal-off", "namespace", "file", "origin", "host", "mcp-origin", "mcp-off"} {
		t.Run(change, func(t *testing.T) {
			f := newS4HFixture(t, []personalStorageCollection{})
			cfg := *f.cfg
			p := *cfg.PersonalServices
			cfg.PersonalServices = &p
			m := *cfg.MCP
			cfg.MCP = &m
			switch change {
			case "provider":
				cfg.ControlPlane = ""
			case "personal-off":
				cfg.PersonalServices = nil
			case "namespace":
				cfg.StateNamespace = "other"
			case "file":
				p.File += ".other"
			case "origin":
				p.Origin = "https://other.example"
			case "host":
				p.Host = "other.example"
			case "mcp-origin":
				m.Origin = "https://other.example"
				m.Host = "other.example"
			case "mcp-off":
				cfg.MCP = nil
			}
			var a *mcpApplicationTools
			if change == "nil" {
				a = newMCPApplicationTools(nil, nil, f.f.cache, f.f.s)
			} else {
				a = newMCPApplicationTools(&cfg, nil, f.f.cache, f.f.s)
			}
			a.now = f.f.now
			r := f.f.request(t, false, "alice@example.com")
			list, err := a.List(r)
			if err == nil && len(list) != 1 {
				t.Fatal("mismatched store advertised")
			}
			result, err := a.Call(r, "personal_read", []byte(`{}`))
			if err != nil || !result.IsError {
				t.Fatal("mismatched store callable")
			}
		})
	}
	f := newS4HFixture(t, []personalStorageCollection{})
	f.cfg.MCP.Origin = "https://changed.example"
	f.cfg.TrustedProxyCIDR.IP[0] = 1
	if f.tools.config.Origin == f.cfg.MCP.Origin || reflect.DeepEqual(f.tools.trusted, f.cfg.TrustedProxyCIDR) {
		t.Fatal("mutable config aliases")
	}
}

func TestMCPApplicationSharedGateZeroEffects(t *testing.T) {
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	for _, kind := range []string{"npm", "machine", "application"} {
		for _, privateUncertain := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/private-uncertain=%v", kind, privateUncertain), func(t *testing.T) {
				f := newS4HFixture(t, []personalStorageCollection{})
				r := f.f.request(t, false, "alice@example.com")
				if privateUncertain {
					f.f.s.storage.uncertain = true
				}
				// A deliberately unusable seeded operation would panic on clock use; the
				// literal public gate must prevent it. This is NOT shared success evidence.
				f.tools.shared = &SharedIconOperations{personal: f.f.s, now: func() time.Time { t.Fatal("false gate touched shared clock"); return time.Time{} }}
				before := personalOpsBytes(t, f.f)
				for _, name := range []string{"shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset"} {
					v := s4hArgs(name)
					if name == "shared_targets_list" {
						v["kind"] = kind
					} else {
						v["target"].(map[string]any)["kind"] = kind
					}
					result, err := f.tools.Call(r, name, s4hJSON(t, v))
					if err != nil {
						t.Fatal(err)
					}
					s4hCode(t, result, "unavailable")
				}
				list, err := f.tools.List(r)
				if err != nil {
					t.Fatal(err)
				}
				for _, name := range s4hNames(t, list) {
					if strings.HasPrefix(name, "shared_") {
						t.Fatal("dormant descriptor")
					}
				}
				if !bytes.Equal(before, personalOpsBytes(t, f.f)) {
					t.Fatal("gate side effect")
				}
			})
		}
	}
}

// Explicitly below-gate prepared reads are authority fixtures only. Even a real
// seeded v4 writer with a valid selected target cannot expose a new public tool.
func TestMCPApplicationSeededSharedPreparedBoundary(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		for _, change := range []string{"fresh", "private-uncertain", "allowlist", "origin", "namespace", "writer-uncertain", "stale"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				o, r, ref, at := sharedTargetFixture(t, kind)
				p := newPersonalOpsFixture(t, []personalStorageCollection{})
				o.personal = p.s
				cfg := &Config{ControlPlane: controlPlaneTailscale, StateNamespace: o.binding.Namespace, PortalEditMode: serviceEditShared, MCP: o.mcp, TrustedProxyCIDR: o.trusted, PollInterval: o.interval}
				a := newMCPApplicationTools(cfg, o, o.cache, p.s)
				a.now = func() time.Time { return at }
				before, err := os.ReadFile(o.editor.path)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "private-uncertain":
					p.s.storage.uncertain = true
				case "allowlist":
					o.editor.config.Logins = map[string]bool{}
				case "origin":
					o.editor.config.Origin = "https://other.example"
				case "namespace":
					o.binding.Namespace = "different"
				case "writer-uncertain":
					o.editor.disabled = true
				case "stale":
					d := *o.cache.Get()
					d.UpdatedAt = at.Add(-91 * time.Second)
					o.cache.data.Store(&d)
				}
				_, preparedErr := o.prepareMCPTarget(r, ref)
				if (preparedErr == nil) != (change == "fresh" || change == "private-uncertain") {
					t.Fatal("below-gate independent shared guard", preparedErr)
				}
				list, err := a.List(r)
				eligible := change == "fresh" || change == "private-uncertain"
				wantTools := 1
				if eligible {
					wantTools = 5
				}
				if err != nil || len(list) != wantTools {
					t.Fatal("current shared discovery", err, s4hNames(t, list))
				}
				for _, name := range []string{"shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset"} {
					v := s4hArgs(name)
					if name == "shared_targets_list" {
						v["kind"] = string(kind)
					} else {
						v["target"] = ref
					}
					result, err := a.Call(r, name, s4hJSON(t, v))
					if err != nil {
						t.Fatal(err)
					}
					if eligible && (name == "shared_targets_list" || name == "shared_target_get") {
						if result.IsError {
							t.Fatal("eligible read failed", result)
						}
					} else {
						code := "unavailable"
						if eligible {
							code = "conflict"
						}
						if change == "allowlist" || change == "origin" {
							code = "forbidden"
						}
						if change == "writer-uncertain" {
							code = "uncertain"
						}
						s4hCode(t, result, code)
					}
				}
				after, err := os.ReadFile(o.editor.path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("new shared tool wrote")
				}
			})
		}
	}
}

func TestMCPApplicationPersonalAdmission(t *testing.T) {
	f := newS4HFixture(t, []personalStorageCollection{})
	r := f.f.request(t, true, "alice@example.com")
	for i := 0; i < 7; i++ {
		f.f.s.requests <- struct{}{}
	}
	list, err := f.tools.List(r)
	if err != nil || len(list) != 5 {
		t.Fatal("List reacquired personal slot")
	}
	state := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
	state = s4hPersonal(t, f.tools, r, "personal_create", s4hMutationArgs(state, "personal_create"))
	if state.Revision != "1" || len(f.f.s.requests) != 7 {
		t.Fatal("mutation double admission/leak")
	}
	f.f.s.requests <- struct{}{}
	list, err = f.tools.List(r)
	if err != nil || len(list) != 1 {
		t.Fatal("saturated descriptor")
	}
	result, err := f.tools.Call(r, "personal_read", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	s4hCode(t, result, "unavailable")
	// Same eight slots are consumed by the unchanged browser wrapper too.
	h := newPersonalServicesHandler(f.f.s, f.f.cache, mcpTrusted(), f.tools.interval)
	br := personalHTTPRequest(http.MethodGet, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, br)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("browser did not share admission", w.Code)
	}
	for i := 0; i < 8; i++ {
		<-f.f.s.requests
	}
	_ = s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
}

func TestMCPApplicationCrossUserAndLateLoss(t *testing.T) {
	for _, change := range []string{"foreign-id", "owner-final", "cancel-final", "owner-post-sync", "cancel-post-sync", "uncertain"} {
		t.Run(change, func(t *testing.T) {
			f := newS4HFixture(t, []personalStorageCollection{})
			r := f.f.request(t, false, "alice@example.com")
			state := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
			state = s4hPersonal(t, f.tools, r, "personal_create", s4hMutationArgs(state, "personal_create"))
			args := s4hMutationArgs(state, "personal_update")
			before := personalOpsBytes(t, f.f)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			if change == "foreign-id" {
				r = f.f.request(t, false, "bob@example.com")
				own := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
				args["generation"] = own.Generation
				args["store_id"] = own.StoreID
				args["revision"] = own.Revision
			}
			stage := "finalcheck"
			if strings.HasSuffix(change, "post-sync") {
				stage = "publish"
			}
			f.f.s.storage.failpoint = func(at string) error {
				if change == "uncertain" && at == "dirsync" {
					return errors.New("secret filesystem detail")
				}
				if at == stage {
					if strings.HasPrefix(change, "owner-") {
						d := *f.f.cache.Get()
						d.TailscaleUsersByLogin = nil
						f.f.cache.data.Store(&d)
					}
					if strings.HasPrefix(change, "cancel-") {
						cancel()
					}
				}
				return nil
			}
			result, err := f.tools.Call(r, "personal_update", s4hJSON(t, args))
			if err != nil || !result.IsError {
				t.Fatal("late/foreign authority", err)
			}
			s4hStructured(t, result)
			if bytes.Contains(s4hJSON(t, result), []byte("document")) || bytes.Contains(s4hJSON(t, result), []byte("secret")) {
				t.Fatal("private error output")
			}
			after := personalOpsBytes(t, f.f)
			if strings.HasSuffix(change, "post-sync") {
				if bytes.Equal(before, after) {
					t.Fatal("post-sync failure falsely treated as rollback")
				}
			} else if change != "uncertain" && !bytes.Equal(before, after) {
				t.Fatal("precommit failure wrote")
			}
		})
	}
}

// Test-only dispatch assembly: actual middleware plus incumbent framing/parser,
// one existing-style route slot and the dormant adapters. NOT production wiring or
// deadline integration evidence; production route tests remain unchanged.
func s4hDispatch(t *testing.T, a *mcpApplicationTools, h *mcpHandler, w http.ResponseWriter, r *http.Request, calls *int) {
	t.Helper()
	if checkMCPToolRequest(r, a.config, a.trusted) != nil {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, mcpMaxRequestBytes))
	if err != nil || mcpValidateJSON(b) != nil {
		mcpWrite(w, 400, nil, nil, -32600, "Invalid Request")
		return
	}
	req, ok := mcpParseRequest(b)
	if !ok {
		mcpWrite(w, 400, nil, nil, -32600, "Invalid Request")
		return
	}
	if req.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if req.Method != "tools/call" {
		mcpWrite(w, 200, req.ID, nil, -32601, "Method not found")
		return
	}
	name, valid := mcpString(req.Params["name"])
	if !valid || !mcpFields(req.Params, "name", "arguments", "_meta") {
		mcpWrite(w, 200, req.ID, nil, -32602, "Invalid params")
		return
	}
	*calls++
	result, err := a.Call(r, name, req.Params["arguments"])
	if err != nil {
		if !errors.Is(err, errMCPInvalidParams) {
			t.Fatal("unexpected dispatch error")
		}
		mcpWrite(w, 200, req.ID, nil, -32602, "Invalid params")
		return
	}
	body, err := mcpApplicationResponse(req.ID, result)
	if err != nil {
		body, err = mcpApplicationResponse(req.ID, mcpApplicationResult(nil, errPersonalUnavailable, false))
		if err != nil {
			t.Fatal(err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}
func TestMCPApplicationTestOnlyDispatchAndNotifications(t *testing.T) {
	f := newS4HFixture(t, []personalStorageCollection{})
	h := &mcpHandler{config: *f.cfg.MCP, slots: make(chan struct{}, 8)}
	calls := 0
	middleware := IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s4hDispatch(t, f.tools, h, w, r, &calls) }))
	for _, name := range append(append([]string{}, s4hToolNames...), "personal_import") {
		for _, notification := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/notification=%v", name, notification), func(t *testing.T) {
				params := map[string]any{"name": name, "arguments": s4hArgs(name)}
				envelope := map[string]any{"jsonrpc": "2.0", "method": "tools/call", "params": params}
				if !notification {
					envelope["id"] = "rpc-id"
				}
				before := personalOpsBytes(t, f.f)
				prior := calls
				r := mcpTestRequest(string(s4hJSON(t, envelope)), true)
				r.Host = f.cfg.MCP.Host
				w := httptest.NewRecorder()
				middleware.ServeHTTP(w, r)
				if notification {
					if w.Code != 202 || w.Body.Len() != 0 || calls != prior || !bytes.Equal(before, personalOpsBytes(t, f.f)) {
						t.Fatal("notification invoked application")
					}
				} else {
					if w.Code != 200 || calls != prior+1 || w.Body.Len() > mcpMaxResponseBytes {
						t.Fatal("buffered response")
					}
					if name == "personal_import" && !strings.Contains(w.Body.String(), `"code":-32602`) {
						t.Fatal("unknown tool convention")
					}
				}
				if len(h.slots) != 0 || len(f.f.s.requests) != 0 {
					t.Fatal("slot leak")
				}
			})
		}
	}
}

func TestMCPApplicationCoarseErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{errPersonalForbidden, "forbidden"}, {errMetadataForbidden, "forbidden"}, {errMetadataNotVisible, "not_found"}, {errPersonalUnavailable, "unavailable"}, {errPersonalStorage, "unavailable"}, {errMetadataStorage, "unavailable"}, {errPersonalConflict, "conflict"}, {errMetadataConflict, "conflict"}, {errPersonalStorageUncertain, "uncertain"}, {errMetadataUncertain, "uncertain"}, {errPersonalCapacity, "capacity"}, {errPersonalInvalid, "invalid_request"}, {errMetadataInvalidFields, "invalid_request"}, {errors.New("/secret/path token=PRIVATE"), "unavailable"}} {
		t.Run(tc.code+"/"+fmt.Sprint(tc.err), func(t *testing.T) {
			s4hCode(t, mcpApplicationResult(map[string]string{"private": "must-disappear"}, fmt.Errorf("raw input secret: %w", tc.err), true), tc.code)
		})
	}
	for _, mutation := range []bool{false, true} {
		want := "unavailable"
		if mutation {
			want = "uncertain"
		}
		s4hCode(t, mcpApplicationResult(make(chan int), nil, mutation), want)
		s4hCode(t, mcpApplicationResult(map[string]string{"x": strings.Repeat("<", mcpMaxResponseBytes)}, nil, mutation), want)
	}
}

func TestMCPApplicationCommittedThenEncodingFailure(t *testing.T) {
	f := newS4HFixture(t, []personalStorageCollection{})
	r := f.f.request(t, false, "alice@example.com")
	state := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
	args := s4hMutationArgs(state, "personal_create")
	committed := s4hPersonal(t, f.tools, r, "personal_create", args)
	// Encoding-failure fixture only: current frozen DTOs contain no unsupported
	// values. Prove the common encoder never claims rollback for a real preceding
	// commit, without adding a production failpoint or retry path.
	failed := mcpApplicationResult(struct {
		Committed   mcpPersonalResult
		Unsupported chan int
	}{committed, make(chan int)}, nil, true)
	s4hCode(t, failed, "uncertain")
	fresh := s4hPersonal(t, f.tools, r, "personal_read", map[string]any{})
	if fresh.Revision != "1" || len(fresh.Document.Entries) != 1 {
		t.Fatal("encoding failure rolled back committed operation")
	}
	result, err := f.tools.Call(r, "personal_create", s4hJSON(t, args))
	if err != nil {
		t.Fatal(err)
	}
	s4hCode(t, result, "conflict")
}

func TestMCPApplicationCompleteResponseBudget(t *testing.T) {
	ids := []json.RawMessage{[]byte(`"` + strings.Repeat(`<`, 256) + `"`), []byte(`"` + strings.Repeat(string([]byte{92})+"u003c", 256) + `"`), s4hJSON(t, strings.Repeat("\"", 256)), s4hJSON(t, strings.Repeat("\\", 256)), s4hJSON(t, strings.Repeat("é", 128)), s4hJSON(t, strings.Repeat("😀", 64)), []byte(`9007199254740991`), []byte(`-9007199254740991`)}
	for i, id := range ids {
		for _, payload := range []string{strings.Repeat("<>&\"\\\n\t é", 100), strings.Repeat("x", 120000), strings.Repeat("<", 40000)} {
			t.Run(fmt.Sprintf("id=%d/len=%d", i, len(payload)), func(t *testing.T) {
				result := mcpApplicationResult(map[string]string{"value": payload}, nil, false)
				body, err := mcpApplicationResponse(id, result)
				if err != nil || len(body) > mcpMaxResponseBytes {
					t.Fatal("full response bound", err)
				}
				// Independently marshal the ACTUAL frozen envelope, including retained raw
				// ID and the JSON-inside-JSON escaped text. No size-helper self-oracle.
				data := s4hStructured(t, result)
				wire := struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Result  struct {
						Structured json.RawMessage  `json:"structuredContent"`
						Content    []mcpTextContent `json:"content"`
						IsError    bool             `json:"isError"`
					} `json:"result"`
				}{JSONRPC: "2.0", ID: id}
				wire.Result.Structured = data
				wire.Result.Content = []mcpTextContent{{Type: "text", Text: string(data)}}
				wire.Result.IsError = result.IsError
				independent := s4hJSON(t, wire)
				if !bytes.Equal(body, independent) {
					t.Fatal("envelope differs")
				}
				bound, e := mcpToolResultSizeBound(json.RawMessage(data))
				if e != nil || len(body) > bound {
					t.Fatal("budget underestimate")
				}
			})
		}
	}
	for _, id := range []json.RawMessage{nil, []byte(`null`), []byte(`"\n"`), []byte(`"` + strings.Repeat("a", 257) + `"`), []byte(`9007199254740992`)} {
		if body, err := mcpApplicationResponse(id, mcpApplicationResult(mcpIconsResult(), nil, false)); err == nil || body != nil {
			t.Fatal("unaccepted ID")
		}
	}
	oversize := mcpToolResult{StructuredContent: map[string]string{"private": strings.Repeat("<", 100000)}, Content: []mcpTextContent{{Type: "text", Text: "false-small-text"}}}
	if body, err := mcpApplicationResponse(ids[0], oversize); err == nil || body != nil {
		t.Fatal("partial/oversized result exposed")
	}
	result := mcpApplicationResult(map[string]string{"x": "original"}, nil, false)
	result.Content[0].Text = "forged"
	body, err := mcpApplicationResponse(ids[0], result)
	if err != nil || bytes.Contains(body, []byte("forged")) {
		t.Fatal("mismatched text escaped final encoder")
	}
}

func TestMCPApplicationLargeValidPersonalState(t *testing.T) {
	entries := make([]PersonalService, 128)
	for i := range entries {
		entries[i] = PersonalService{ID: fmt.Sprintf("ps_%032x", i+1), Name: strings.Repeat("<", 120), URL: "https://server.tailnet.ts.net/" + strings.Repeat("a", 200)}
	}
	document := PersonalServicesDocument{Version: 1, Entries: entries}
	// HTML-safe JSON encoding counts against the incumbent 64-KiB authored cap;
	// choose the largest bounded valid repeated escaping fixture by shrinking path.
	for validatePersonalDocument(document) != nil {
		for i := range document.Entries {
			e := &document.Entries[i]
			if len(e.Name) > 10 {
				e.Name = e.Name[:len(e.Name)-1]
			} else {
				t.Fatal("could not build valid large fixture")
			}
		}
	}
	f := newS4HFixture(t, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", document.Entries)})
	r := f.f.request(t, false, "alice@example.com")
	result, err := f.tools.Call(r, "personal_read", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := f.f.read(r)
	if err != nil {
		t.Fatal(err)
	}
	_, statuses := ProjectPersonalServices(IdentityFromContext(r.Context()), f.f.cache.Get(), state, f.f.at, 30*time.Second)
	data := mcpPersonalResult{state, statuses}
	raw := s4hJSON(t, data)
	text := string(raw)
	id := json.RawMessage(`"` + strings.Repeat(`<`, 256) + `"`)
	actual := s4hJSON(t, mcpResponse{JSONRPC: "2.0", ID: id, Result: mcpToolResult{StructuredContent: data, Content: []mcpTextContent{{Type: "text", Text: text}}}})
	body, err := mcpApplicationResponse(id, result)
	if err != nil || len(body) > mcpMaxResponseBytes {
		t.Fatal("personal final cap", err)
	}
	if len(actual) > mcpMaxResponseBytes {
		s4hCode(t, result, "unavailable")
	} else if result.IsError {
		t.Fatal("valid fitting personal state rejected")
	}
	if !result.IsError {
		var got mcpPersonalResult
		_ = json.Unmarshal(s4hStructured(t, result), &got)
		if len(got.Document.Entries) != 128 || len(got.Projection) != 128 {
			t.Fatal("partial personal success")
		}
	}
	t.Logf("valid authored bytes=%d full dual response=%d final=%d error=%v", len(s4hJSON(t, document)), len(actual), len(body), result.IsError)
}

func TestMCPApplicationDormantProduction(t *testing.T) {
	files, err := parser.ParseDir(token.NewFileSet(), ".", func(info os.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	constructors := 0
	for _, pkg := range files {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "mcp_tools.go") {
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				if call, ok := node.(*ast.CallExpr); ok {
					if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "newMCPApplicationTools" {
						constructors++
						if !strings.HasSuffix(name, "main.go") {
							t.Fatal("unexpected production constructor", name)
						}
					}
				}
				return true
			})
		}
	}
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	if constructors != 1 {
		t.Fatal("production dependency count", constructors)
	}
	// Without optional stores/binding, discovery remains icons-only. A recognized
	// personal call is unavailable, not an unknown-tool error.
	h := newMCPHandler(*mcpTestConfig(), mcpTrusted(), mcpTestApplicationTools())
	for _, method := range []string{"tools/list", "tools/call"} {
		params := map[string]any{}
		if method == "tools/call" {
			params = map[string]any{"name": "personal_read", "arguments": map[string]any{}}
		}
		r := mcpTestRequest(string(s4hJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})), true)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal("production response", w.Code)
		}
		if method == "tools/list" {
			if strings.Contains(w.Body.String(), "personal_") || strings.Contains(w.Body.String(), "shared_") || !strings.Contains(w.Body.String(), "icons_list") {
				t.Fatal("production discovery expanded")
			}
		} else if !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), `"error":"unavailable"`) {
			t.Fatal("unconfigured production tool succeeded")
		}
	}
}
