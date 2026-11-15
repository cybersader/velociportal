package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Real bound configured v4 open. Synthetic current provider data and request
// fixtures do not establish native-client, Serve or deployment acceptance.
func sharedTargetFixture(t *testing.T, kind sharedTargetKind) (*SharedIconOperations, *http.Request, sharedTargetRef, time.Time) {
	t.Helper()
	config := editorTestConfig()
	config.Binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "selected-source"}
	raw := []byte(`{"version":4,"namespace":"selected-source","control_plane":"tailscale","services":[{"proxy_host_id":3,"name":"Shared Wiki","url":"https://wiki.example/private-presentation-path","icon":"grafana"}],"machines":[{"device_id_type":"id","device_id":"node-1","icon":"generic"}],"applications":[]}`)
	e, err := newServiceMetadataEditor(editorTestFile(t, string(raw)), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cache := newTestCache(nil)
	data := machineMatcherFixture(t)
	npm := standardTestData()
	data.ProxyHosts, data.Policy.ACLs, data.Policy.Groups, data.Policy.Hosts = npm.ProxyHosts, npm.Policy.ACLs, npm.Policy.Groups, npm.Policy.Hosts
	data.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"node-1": {Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: "node-1"}}
	now := time.Now().Truncate(time.Second)
	data.UpdatedAt = now
	cache.data.Store(data)
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cfg := &Config{PortalEditMode: serviceEditShared, ControlPlane: controlPlaneTailscale, StateNamespace: e.config.Binding.Namespace,
		TrustedProxyCIDR: trusted, PollInterval: 30 * time.Second, MCP: &MCPConfig{Origin: e.config.Origin, Host: e.config.Host}}
	o := newSharedIconOperations(cfg, e, cache, nil)
	o.now = func() time.Time { return now }
	ref := o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "3")
	if kind == sharedTargetMachine {
		ref = o.sharedTargetHandle("alice@example.com", kind, "id", "node-1")
	}
	return o, sharedTargetRequest(t, o, "alice@example.com"), ref, now
}

func sharedTargetRequest(t *testing.T, o *SharedIconOperations, login string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, o.origin+"/mcp", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Tailscale-User-Login", login)
	r.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	var actual *http.Request
	IdentityMiddleware(o.trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { actual = r })).ServeHTTP(httptest.NewRecorder(), r)
	if actual == nil {
		t.Fatal("middleware rejected fixture")
	}
	return actual
}

func sharedTargetResult(t *testing.T, o *SharedIconOperations, r *http.Request, ref sharedTargetRef, want error) sharedTargetForm {
	t.Helper()
	form, err := o.prepareMCPTarget(r, ref)
	if !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
	if err != nil && form != (sharedTargetForm{}) {
		t.Fatal("failure exposed partial form")
	}
	return form
}

func TestSharedTargetsPublicGateBeforeEveryEffect(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, "application", "unknown"} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, ref, _ := sharedTargetFixture(t, kind)
			ref.Kind = kind
			old := o.editor.view.Load()
			o.editor.config = nil
			o.now = func() time.Time { t.Error("misconfigured getter sampled clock"); return time.Time{} }
			body := &mcpGuardBody{}
			r.Body = body
			o.personal = &PersonalServicesStore{}
			o.personal.mu.Lock()
			o.editor.mu.Lock()
			form, err := o.GetMCPTarget(r, ref)
			o.editor.mu.Unlock()
			o.personal.mu.Unlock()
			if !errors.Is(err, errMetadataStale) || form != (sharedTargetForm{}) || body.reads != 0 || body.closes != 0 {
				t.Fatal("public gate had effects")
			}
			sharedNPMUnchanged(t, o.editor, old, old.bytes)
		})
	}
	var absent *SharedIconOperations
	if f, e := absent.GetMCPTarget(nil, sharedTargetRef{}); f != (sharedTargetForm{}) || !errors.Is(e, errMetadataStale) {
		t.Fatal("gate not first")
	}
}

func TestSharedTargetsPreparedFormsAndPrivateLockIsolation(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, ref, _ := sharedTargetFixture(t, kind)
			o.personal = &PersonalServicesStore{closed: true, storage: &personalSnapshotStorage{uncertain: true}}
			o.personal.mu.Lock()
			o.personal.storage.mu.Lock()
			defer o.personal.storage.mu.Unlock()
			done := make(chan struct{})
			go func() { defer close(done); sharedTargetResult(t, o, r, ref, nil) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				o.personal.mu.Unlock()
				t.Fatal("private mutex blocked selected read")
			}
			o.personal.mu.Unlock()
			body := &mcpGuardBody{}
			r.Body = body
			form := sharedTargetResult(t, o, r, ref, nil)
			if form.Target != ref || form.Revision != o.editor.view.Load().revision || len(form.TargetRevision) != 43 {
				t.Fatal("opaque references missing")
			}
			if kind == sharedTargetNPM && (form.Name != "Shared Wiki" || form.SharedIcon != "grafana" || form.EffectiveIcon != "grafana") {
				t.Fatal("wrong NPM presentation")
			}
			if kind == sharedTargetMachine && (form.Name != "server" || form.SharedIcon != "generic" || form.EffectiveIcon != "generic" || machineConsoleEligible("alice@example.com", o.cache.Get())) {
				t.Fatal("machine selection depended on console role")
			}
			if body.reads != 0 || body.closes != 0 {
				t.Fatal("operation consumed request body")
			}
			encoded, _ := json.Marshal(form)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &fields)
			if len(fields) != 6 {
				t.Fatalf("unexpected response fields: %s", encoded)
			}
			for _, field := range []string{"target", "name", "shared_icon", "effective_icon", "revision", "target_revision"} {
				if _, ok := fields[field]; !ok {
					t.Fatal("missing public field", field)
				}
			}
			for _, secret := range []string{"node-1", "100.64.0.10", "alice@example.com", "private-presentation-path", "forward_host", "target_digest", "sshEnabled", "rule_index", "total", "csrf", "server.tailnet.ts.net"} {
				if bytes.Contains(encoded, []byte(secret)) {
					t.Fatalf("private evidence in response: %s", secret)
				}
			}
			var target map[string]any
			_ = json.Unmarshal(fields["target"], &target)
			if len(target) != 2 {
				t.Fatal("target leaks identity")
			}
			// Browser admission is concrete, not native with CSRF disabled.
			r.Header.Set("Origin", o.origin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
			if sharedTargetResult(t, o, r, ref, nil) != form {
				t.Fatal("native/browser form drift")
			}
		})
	}
}

func TestSharedTargetsAdmissionAtEntryAndLateCheckpoints(t *testing.T) {
	changes := []string{"no-context", "raw-login", "both-logins", "source", "origin", "fetch", "host", "method", "path", "query", "version", "content", "authorization", "cookie", "session", "cancel", "allowlist", "mode", "shared-origin", "shared-host", "mcp-origin", "mcp-host", "binding", "namespace", "provider"}
	for _, stage := range []string{"entry", "after-lock", "after-disk"} {
		for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
			for _, change := range changes {
				t.Run(stage+"/"+string(kind)+"/"+change, func(t *testing.T) {
					o, r, ref, now := sharedTargetFixture(t, kind)
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					r = r.WithContext(ctx)
					want := errMetadataForbidden
					mutate := func() {
						switch change {
						case "no-context":
							*r = *r.WithContext(context.Background())
						case "raw-login":
							r.Header.Set("Tailscale-User-Login", "bob@example.com")
						case "both-logins":
							IdentityFromContext(r.Context()).Login = "bob@example.com"
							r.Header.Set("Tailscale-User-Login", "bob@example.com")
							if stage == "entry" {
								want = errMetadataNotVisible
							}
						case "source":
							r.RemoteAddr = "192.0.2.1:22"
						case "origin":
							r.Header.Set("Origin", "http://foreign.example")
						case "fetch":
							r.Header.Set("Sec-Fetch-Site", "same-origin")
						case "host":
							r.Host = "foreign.example"
						case "method":
							r.Method = http.MethodGet
						case "path":
							r.URL.RawPath = "/m%63p"
						case "query":
							r.URL.ForceQuery = true
						case "version":
							r.Header.Del("MCP-Protocol-Version")
						case "content":
							r.Header.Set("Content-Type", "text/plain")
						case "authorization":
							r.Header["Authorization"] = []string{""}
						case "cookie":
							r.Header["Cookie"] = []string{""}
						case "session":
							r.Header["Mcp-Session-Id"] = []string{""}
						case "cancel":
							cancel()
						case "allowlist":
							delete(o.editor.config.Logins, "alice@example.com")
						case "mode":
							o.mode = serviceEditOff
						case "shared-origin":
							o.editor.config.Origin = "http://foreign.example"
						case "shared-host":
							o.editor.config.Host = "foreign.example"
						case "mcp-origin":
							o.mcp.Origin = "http://foreign.example"
						case "mcp-host":
							o.mcp.Host = "foreign.example"
						case "binding":
							o.editor.config.Binding.Namespace = "other"
							want = errMetadataStale
						case "namespace":
							o.binding.Namespace = ""
							o.editor.config.Binding = o.binding
							want = errMetadataStale
						case "provider":
							o.binding.ControlPlane = "unknown"
							o.editor.config.Binding = o.binding
							want = errMetadataStale
						}
					}
					calls := 0
					if stage == "entry" {
						mutate()
					} else {
						trigger := 1
						if stage == "after-disk" {
							trigger = 2
						}
						o.now = func() time.Time {
							calls++
							if calls == trigger {
								mutate()
							}
							return now
						}
					}
					old := o.editor.view.Load()
					// want may be set at a late seam, so inspect after the operation.
					form, err := o.prepareMCPTarget(r, ref)
					if !errors.Is(err, want) || form != (sharedTargetForm{}) {
						t.Fatalf("got %v want %v; form=%v", err, want, form)
					}
					if stage != "entry" && calls == 0 {
						t.Fatal("checkpoint not reached")
					}
					sharedNPMUnchanged(t, o.editor, old, old.bytes)
				})
			}
		}
	}
}

func TestSharedTargetsSnapshotWriterAndConfigPrerequisites(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		for _, change := range []string{"nil-service", "nil-editor", "nil-config", "nil-cache", "nil-clock", "nil-trusted", "nil-mcp", "nil-request", "nil-url", "nil-snapshot", "zero-time", "future-time", "stale", "nil-policy", "zero-interval", "negative-interval", "overflow-interval", "snapshot-provider", "disabled", "nil-view", "nil-metadata", "no-directory", "no-lock", "empty-bytes", "empty-revision", "invalid-model", "wrong-view-binding"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				o, r, ref, now := sharedTargetFixture(t, kind)
				data := *o.cache.Get()
				view := *o.editor.view.Load()
				want := errMetadataStale
				switch change {
				case "nil-service":
					o = nil
				case "nil-editor":
					o.editor = nil
				case "nil-config":
					o.editor.config = nil
				case "nil-cache":
					o.cache = nil
				case "nil-clock":
					o.now = nil
				case "nil-trusted":
					o.trusted = nil
					want = errMetadataForbidden
				case "nil-mcp":
					o.mcp = nil
					want = errMetadataForbidden
				case "nil-request":
					r = nil
					want = errMetadataForbidden
				case "nil-url":
					r.URL = nil
					want = errMetadataForbidden
				case "nil-snapshot":
					o.cache.data.Store(nil)
				case "zero-time":
					data.UpdatedAt = time.Time{}
					o.cache.data.Store(&data)
				case "future-time":
					data.UpdatedAt = now.Add(time.Nanosecond)
					o.cache.data.Store(&data)
				case "stale":
					data.UpdatedAt = now.Add(-90*time.Second - time.Nanosecond)
					o.cache.data.Store(&data)
				case "nil-policy":
					data.Policy = nil
					o.cache.data.Store(&data)
				case "zero-interval":
					o.interval = 0
				case "negative-interval":
					o.interval = -1
				case "overflow-interval":
					o.interval = time.Duration(1<<63-1)/3 + 1
				case "snapshot-provider":
					data.ControlPlane.Provider = controlPlaneHeadscale
					o.cache.data.Store(&data)
				case "disabled":
					o.editor.disabled = true
					want = errMetadataUncertain
				case "nil-view":
					o.editor.view.Store(nil)
				case "nil-metadata":
					view.metadata = nil
					o.editor.view.Store(&view)
				case "no-directory":
					held := o.editor.directory
					o.editor.directory = nil
					defer func() { o.editor.directory = held }()
				case "no-lock":
					held := o.editor.lock
					o.editor.lock = nil
					defer func() { o.editor.lock = held }()
				case "empty-bytes":
					view.bytes = nil
					o.editor.view.Store(&view)
				case "empty-revision":
					view.revision = ""
					o.editor.view.Store(&view)
				case "invalid-model", "wrong-view-binding":
					m := *view.metadata
					m.document = cloneServiceMetadataDocument(m.document)
					if change == "invalid-model" {
						m.document.Version = 0
					} else {
						m.document.Namespace = "wrong"
					}
					view.metadata = &m
					o.editor.view.Store(&view)
				}
				sharedTargetResult(t, o, r, ref, want)
			})
		}
	}
}

func TestSharedTargetsHandleDomainsAndValidation(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		o, r, ref, _ := sharedTargetFixture(t, kind)
		fields := []string{"alice@example.com", "selected-source", "tailscale", string(kind), "3"}
		if kind == sharedTargetMachine {
			fields = append(fields[:4], "id", "node-1")
		}
		raw, _ := json.Marshal(fields)
		mac := hmac.New(sha256.New, o.editor.key[:])
		mac.Write([]byte("shared-target-handle-v1\x00"))
		mac.Write(raw)
		if ref.Handle != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
			t.Fatal("wrong handle framing/domain")
		}
		for _, change := range []string{"caller", "kind", "process", "namespace", "provider", "reference", "id-kind"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				other := *o
				e := &ServiceMetadataEditor{key: o.editor.key} // new digest-only fixture, never copy a mutex
				other.editor = e
				login := "alice@example.com"
				k := kind
				parts := append([]string(nil), fields[4:]...)
				switch change {
				case "caller":
					login = "bob@example.com"
				case "kind":
					if k == sharedTargetNPM {
						k = sharedTargetMachine
					} else {
						k = sharedTargetNPM
					}
				case "process":
					e.key[0] ^= 1
				case "namespace":
					other.binding.Namespace = "another-source"
				case "provider":
					other.binding.ControlPlane = controlPlaneHeadscale
				case "reference":
					parts[len(parts)-1] += "9"
				case "id-kind":
					parts = append([]string{"nodeId"}, parts...)
				}
				foreign := other.sharedTargetHandle(login, k, parts...)
				foreign.Kind = kind
				if foreign == ref {
					t.Fatal("handle not bound")
				}
				sharedTargetResult(t, o, r, foreign, errMetadataNotVisible)
			})
		}
		for _, bad := range []string{"", "a", strings.Repeat("a", 42), strings.Repeat("a", 44), ref.Handle + "=", strings.Repeat("+", 43), strings.Repeat("_", 43), strings.Repeat("a", 42) + "!", "\n" + ref.Handle[1:]} {
			t.Run(string(kind)+"/malformed/"+strconv.Itoa(len(bad))+bad[:min(len(bad), 1)], func(t *testing.T) { v := ref; v.Handle = bad; sharedTargetResult(t, o, r, v, errMetadataInvalidFields) })
		}
		for _, badKind := range []sharedTargetKind{"", "NPM", "application", "unknown"} {
			v := ref
			v.Kind = badKind
			sharedTargetResult(t, o, r, v, errMetadataNotVisible)
		}
	}
}

func TestSharedTargetsNPMVisibilityAndHeadscaleLegacy(t *testing.T) {
	for _, change := range []string{"absent", "invisible", "disabled", "domainless", "blank-domain", "duplicate", "zero-id", "negative-id"} {
		t.Run(change, func(t *testing.T) {
			o, r, ref, _ := sharedTargetFixture(t, sharedTargetNPM)
			data := o.cache.Get()
			index := -1
			for i, h := range data.ProxyHosts {
				if h.ID == 3 {
					index = i
				}
			}
			if index < 0 {
				t.Fatal("missing fixture")
			}
			switch change {
			case "absent":
				data.ProxyHosts = nil
			case "invisible":
				data.Policy = &Policy{}
			case "disabled":
				data.ProxyHosts[index].Enabled = false
			case "domainless":
				data.ProxyHosts[index].DomainNames = nil
			case "blank-domain":
				data.ProxyHosts[index].DomainNames = []string{" "}
			case "duplicate":
				data.ProxyHosts = append(data.ProxyHosts, data.ProxyHosts[index])
			case "zero-id":
				data.ProxyHosts[index].ID = 0
				ref = o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "0")
			case "negative-id":
				data.ProxyHosts[index].ID = -3
				ref = o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "-3")
			}
			sharedTargetResult(t, o, r, ref, errMetadataNotVisible)
		})
	}
	for _, provider := range []controlPlaneProvider{controlPlaneHeadscale, controlPlaneTailscale} {
		t.Run(string(provider), func(t *testing.T) {
			e, cache, _ := editorFixture(t)
			e.config.Binding = serviceMetadataBinding{ControlPlane: provider, Namespace: "legacy-source"}
			data := cache.Get()
			data.ControlPlane.Provider = provider
			data.GrantRoleSelectorsByLogin = nil
			data.UpdatedAt = time.Now()
			o := editorTestOperations(e, cache)
			o.mcp = &MCPConfig{Origin: o.origin, Host: o.host}
			r := sharedTargetRequest(t, o, "alice@example.com")
			ref := o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "3")
			sharedTargetResult(t, o, r, ref, nil)
			if provider == controlPlaneHeadscale {
				sharedTargetResult(t, o, r, o.sharedTargetHandle("alice@example.com", sharedTargetMachine, "id", "node-1"), errMetadataNotVisible)
			}
			// Ordinary configured legacy read/save retains the browser wire and v3.
			form, err := o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3)
			if err != nil {
				t.Fatal(err)
			}
			var host ProxyHost
			for _, h := range data.ProxyHosts {
				if h.ID == 3 {
					host = h
				}
			}
			if form.TargetRevision != e.targetRevision(host) {
				t.Fatal("legacy browser revision changed")
			}
			req := serviceMetadataEditRequest{Action: "save", Revision: form.Revision, TargetRevision: form.TargetRevision, CSRF: form.CSRF, Icon: "generic"}
			if err := o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, req); err != nil {
				t.Fatal(err)
			}
			if e.view.Load().metadata.document.Version != 3 {
				t.Fatal("ordinary save promoted v4")
			}
		})
	}
}

func TestSharedTargetsMachineFullEvidenceAndTypedAmbiguity(t *testing.T) {
	changes := []string{"ssh-absent", "ssh-unsupported", "ssh-wrong-human", "ssh-wrong-destination", "grant-absent", "grant-wrong-human", "grant-wrong-destination", "grant-port", "grant-udp", "capability-missing", "capability-false", "capability-other", "shared-user", "no-id", "bad-kind", "bad-value", "wrong-provider", "duplicate-visible-node", "duplicate-hidden-node", "duplicate-typed-key", "orphan-typed-alias"}
	for _, change := range changes {
		t.Run(change, func(t *testing.T) {
			o, r, ref, _ := sharedTargetFixture(t, sharedTargetMachine)
			d := o.cache.Get()
			switch change {
			case "ssh-absent":
				d.Policy.SSH.State = sshPolicyAbsent
			case "ssh-unsupported":
				d.Policy.SSH.State = sshPolicyUnsupported
			case "ssh-wrong-human":
				d.Policy.SSH.Rules[0].Src = []string{"bob@example.com"}
			case "ssh-wrong-destination":
				d.Policy.SSH.Rules[0].Dst = []string{"tag:other"}
			case "grant-absent":
				d.Policy.Grants = nil
				d.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"*:22"}}}
			case "grant-wrong-human":
				d.Policy.Grants[0].BrowserSrc = []string{"bob@example.com"}
			case "grant-wrong-destination":
				d.Policy.Grants[0].Dst = []string{"100.64.0.99"}
			case "grant-port":
				d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:443")}
			case "grant-udp":
				d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "udp:22")}
			case "capability-missing":
				d.MachineSSHCapableByID = nil
			case "capability-false":
				d.MachineSSHCapableByID["node-1"] = false
			case "capability-other":
				d.MachineSSHCapableByID = map[string]bool{"node-2": true}
			case "shared-user":
				d.GrantRoleSelectorsByLogin = nil
			case "no-id":
				d.PresentationDeviceIDsByNodeID = nil
			case "bad-kind", "bad-value", "wrong-provider":
				id := d.PresentationDeviceIDsByNodeID["node-1"]
				if change == "bad-kind" {
					id.Kind = "ID"
				} else if change == "bad-value" {
					id.Value = " padded "
				} else {
					id.Provider = controlPlaneHeadscale
				}
				d.PresentationDeviceIDsByNodeID["node-1"] = id
			case "duplicate-visible-node", "duplicate-hidden-node":
				n := d.Nodes[0]
				if change == "duplicate-hidden-node" {
					n.Tags = nil
				}
				d.Nodes = append(d.Nodes, n)
			case "duplicate-typed-key":
				n := d.Nodes[0]
				n.ID = "node-2"
				n.Tags = nil
				d.Nodes = append(d.Nodes, n)
				d.PresentationDeviceIDsByNodeID[n.ID] = d.PresentationDeviceIDsByNodeID["node-1"]
			case "orphan-typed-alias":
				d.PresentationDeviceIDsByNodeID["not-current"] = d.PresentationDeviceIDsByNodeID["node-1"]
			}
			sharedTargetResult(t, o, r, ref, errMetadataNotVisible)
		})
	}
	for _, idKind := range []deviceIDKind{deviceIDKindID, deviceIDKindNodeID} {
		t.Run(string(idKind), func(t *testing.T) {
			o, r, _, _ := sharedTargetFixture(t, sharedTargetMachine)
			d := o.cache.Get()
			id := d.PresentationDeviceIDsByNodeID["node-1"]
			id.Kind = idKind
			d.PresentationDeviceIDsByNodeID["node-1"] = id
			ref := o.sharedTargetHandle("alice@example.com", sharedTargetMachine, string(idKind), id.Value)
			sharedTargetResult(t, o, r, ref, nil)
			d.Nodes[0].Name = "not-a-canonical-name" // existing matcher still provides a valid narrow IP target
			if f := sharedTargetResult(t, o, r, ref, nil); f.Name != "Machine" {
				t.Fatal("IP fallback leaked in name")
			}
		})
	}
}

func TestSharedTargetsStableAndRelevantRevisions(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		for _, change := range []string{"fresh-poll", "unrelated-node", "reorder-nodes", "reorder-rules", "health", "shared-name", "shared-icon", "backend", "domain", "machine-name", "access-user", "access-action", "access-period", "default-period"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				o, r, ref, now := sharedTargetFixture(t, kind)
				before := sharedTargetResult(t, o, r, ref, nil)
				d := o.cache.Get()
				relevant := false
				switch change {
				case "fresh-poll":
					d.UpdatedAt = now.Add(-time.Second)
				case "unrelated-node":
					d.Nodes = append(d.Nodes, Node{ID: "unrelated", Name: "other.tailnet.ts.net"})
				case "reorder-nodes":
					d.Nodes = append([]Node{{ID: "unrelated"}}, d.Nodes...)
				case "reorder-rules":
					d.Policy.Grants = append([]GrantRule{{BrowserSrc: []string{"foreign@example.com"}}}, d.Policy.Grants...)
					d.Policy.SSH.Rules = append([]SSHRule{{Action: "accept", Src: []string{"foreign@example.com"}, Users: []string{"unused"}}}, d.Policy.SSH.Rules...)
					users := d.Policy.SSH.Rules[1].Users
					for i, j := 0, len(users)-1; i < j; i, j = i+1, j-1 {
						users[i], users[j] = users[j], users[i]
					}
				case "health":
					for i := range d.ProxyHosts {
						d.ProxyHosts[i].Meta.NginxOnline = !d.ProxyHosts[i].Meta.NginxOnline
					}
				case "shared-name", "shared-icon":
					old := o.editor.view.Load()
					doc := cloneServiceMetadataDocument(old.metadata.document)
					if change == "shared-name" {
						name := "New Shared Name"
						doc.Services[0].Name = &name
						relevant = kind == sharedTargetNPM
					} else {
						icon := "portainer"
						doc.Services[0].Icon = &icon
						doc.Machines[0].Icon = icon
						relevant = true
					}
					sharedTargetReplaceView(t, o, doc)
				case "backend":
					for i := range d.ProxyHosts {
						if d.ProxyHosts[i].ID == 3 {
							d.ProxyHosts[i].ForwardPort++
						}
					}
					relevant = kind == sharedTargetNPM
				case "domain":
					for i := range d.ProxyHosts {
						if d.ProxyHosts[i].ID == 3 {
							d.ProxyHosts[i].DomainNames = []string{"changed.example"}
						}
					}
					relevant = kind == sharedTargetNPM
				case "machine-name":
					d.Nodes[0].Name = "renamed.tailnet.ts.net"
					relevant = kind == sharedTargetMachine
				case "access-user":
					d.Policy.SSH.Rules[0].Users = []string{"different"}
					relevant = kind == sharedTargetMachine
				case "access-action":
					d.Policy.SSH.Rules[0].Action = "check"
					relevant = kind == sharedTargetMachine
				case "access-period":
					d.Policy.SSH.Rules[0].Action = "check"
					d.Policy.SSH.Rules[0].CheckPeriod = time.Hour
					relevant = kind == sharedTargetMachine
				case "default-period":
					d.Policy.SSH.Rules[0].Action = "check"
					before = sharedTargetResult(t, o, r, ref, nil)
					d.Policy.SSH.Rules[0].CheckPeriod = 12 * time.Hour
				}
				after := sharedTargetResult(t, o, r, ref, nil)
				if after.Target != before.Target || (after.TargetRevision != before.TargetRevision) != relevant {
					t.Fatalf("revision stability wrong relevant=%v", relevant)
				}
			})
		}
	}
}

func TestSharedTargetsOverlappingCheckRevisionOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		user string
		long time.Duration
	}{
		{"literal", "deploy", 12 * time.Hour},
		{"nonroot", machineNonrootSelector, 12 * time.Hour},
		{"implicit-default", "deploy", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, r, ref, now := sharedTargetFixture(t, sharedTargetMachine)
			data := o.cache.Get()
			long := SSHRule{Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"tag:server"}, Users: []string{tc.user}, CheckPeriod: tc.long}
			short := long
			short.CheckPeriod = time.Hour
			data.Policy.SSH.Rules = []SSHRule{long, short}
			original, err := json.Marshal(data.Policy)
			if err != nil {
				t.Fatal(err)
			}
			before := sharedTargetResult(t, o, r, ref, nil)
			selected, err := o.selectSharedMachine(&Identity{Login: "alice@example.com"}, o.editor.requestData(data), ref)
			if err != nil || len(selected.machine.Card.Access) != 1 || selected.machine.Card.Access[0].CheckPeriod != time.Hour {
				t.Fatalf("revision view did not choose shortest check: %+v, %v", selected.machine.Card.Access, err)
			}
			after, err := json.Marshal(data.Policy)
			if err != nil || !bytes.Equal(original, after) {
				t.Fatal("target selection mutated cached policy")
			}
			portal := MatchMachines(&Identity{Login: "alice@example.com"}, data)
			if len(portal) != 1 || portal[0].Access[0].CheckPeriod != tc.long {
				t.Fatal("incumbent portal projection changed")
			}

			data.Policy.SSH.Rules = []SSHRule{short, long}
			reordered := sharedTargetResult(t, o, r, ref, nil)
			if reordered != before {
				t.Fatal("equivalent overlapping check reorder changed form/revision")
			}

			// A reorder between initial selection and the post-disk selection is
			// not a target change. Inject synchronously via the existing clock seam.
			data.Policy.SSH.Rules = []SSHRule{long, short}
			calls := 0
			o.now = func() time.Time {
				calls++
				if calls == 2 {
					data.Policy.SSH.Rules = []SSHRule{short, long}
				}
				return now
			}
			if got := sharedTargetResult(t, o, r, ref, nil); got != before || calls != 2 {
				t.Fatalf("reordered post-disk check conflicted or changed form; calls=%d", calls)
			}
			o.now = func() time.Time { return now }
			data.Policy.SSH.Rules[0].CheckPeriod = 2 * time.Hour
			if got := sharedTargetResult(t, o, r, ref, nil); got.TargetRevision == before.TargetRevision {
				t.Fatal("changed effective check period did not change revision")
			}
		})
	}
}

func sharedTargetReplaceView(t *testing.T, o *SharedIconOperations, doc serviceMetadataDocument) {
	t.Helper()
	raw, err := serializeServiceMetadataDocument(doc)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseServiceMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.editor.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	o.editor.view.Store(&serviceMetadataEditorView{metadata: m, bytes: raw, revision: o.editor.digest("file", raw)})
}

func TestSharedTargetsRecheckVisibilityBeforeConflictAndNoStaleForm(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		for _, change := range []string{"clock", "snapshot", "visibility", "relevant", "disk", "disk-plus-visibility", "uncertain", "view"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				o, r, ref, now := sharedTargetFixture(t, kind)
				calls := 0
				want := errMetadataStale
				o.now = func() time.Time {
					calls++
					if calls == 2 {
						d := o.cache.Get()
						switch change {
						case "clock":
							return now.Add(91 * time.Second)
						case "snapshot":
							d.UpdatedAt = now.Add(-91 * time.Second)
						case "visibility", "disk-plus-visibility":
							d.Policy = &Policy{}
							want = errMetadataNotVisible
						case "relevant":
							if kind == sharedTargetNPM {
								for i := range d.ProxyHosts {
									if d.ProxyHosts[i].ID == 3 {
										d.ProxyHosts[i].ForwardPort++
									}
								}
							} else {
								d.Nodes[0].Name = "changed.tailnet.ts.net"
							}
							want = errMetadataConflict
						case "disk":
							want = errMetadataConflict
						case "uncertain":
							o.editor.disabled = true
							want = errMetadataUncertain
						case "view":
							view := *o.editor.view.Load()
							o.editor.view.Store(&view)
							want = errMetadataConflict
						}
					}
					return now
				}
				if change == "disk" || change == "disk-plus-visibility" {
					if err := os.WriteFile(o.editor.path, []byte("changed bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				form, err := o.prepareMCPTarget(r, ref)
				if calls != 2 || !errors.Is(err, want) || form != (sharedTargetForm{}) {
					t.Fatalf("calls=%d got %v want %v", calls, err, want)
				}
			})
		}
	}
}

// Value only signals that the entry identity was requested; updates below touch
// only an atomic cache pointer and mutex-protected clock, never live request maps.
type sharedTargetSignalContext struct {
	context.Context
	entered chan struct{}
	once    sync.Once
}

func (c *sharedTargetSignalContext) Value(key any) any {
	c.once.Do(func() { close(c.entered) })
	return c.Context.Value(key)
}

func TestSharedTargetsFreshnessAfterRealEditorWait(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, ref, now := sharedTargetFixture(t, kind)
			signal := &sharedTargetSignalContext{Context: r.Context(), entered: make(chan struct{})}
			r = r.WithContext(signal)
			var mu sync.Mutex
			clock := now
			o.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
			o.editor.mu.Lock()
			done := make(chan error, 1)
			go func() {
				f, err := o.prepareMCPTarget(r, ref)
				if f != (sharedTargetForm{}) {
					done <- errors.New("stale form")
					return
				}
				done <- err
			}()
			<-signal.entered
			mu.Lock()
			clock = now.Add(91 * time.Second)
			mu.Unlock()
			o.editor.mu.Unlock()
			if err := <-done; !errors.Is(err, errMetadataStale) {
				t.Fatal("clock captured before wait", err)
			}
			// Exactly three intervals is eligible, one nanosecond older is not.
			mu.Lock()
			clock = now.Add(90 * time.Second)
			mu.Unlock()
			sharedTargetResult(t, o, r, ref, nil)
			mu.Lock()
			clock = now.Add(90*time.Second + time.Nanosecond)
			mu.Unlock()
			sharedTargetResult(t, o, r, ref, errMetadataStale)
		})
	}
}

func TestSharedTargetsDormantSourceBoundaryAndBoundedNames(t *testing.T) {
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	if got := sharedTargetDisplayName(strings.Repeat("界", 121), "fallback"); len([]rune(got)) != 120 {
		t.Fatal("display bound")
	}
	for _, bad := range []string{"", " \n ", "bad\x00name", string([]byte{0xff})} {
		if sharedTargetDisplayName(bad, "fallback") != "fallback" {
			t.Fatal("unsafe display name")
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "shared_icon_targets.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	public := []string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if ast.IsExported(fn.Name.Name) {
			public = append(public, fn.Name.Name)
		}
	}
	if !reflect.DeepEqual(public, []string{"GetMCPTarget"}) {
		t.Fatal("unexpected public capability", public)
	}
	source, err := os.ReadFile("shared_icon_targets.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"slog.", "log.", ".commit(", "allowV4", "ListMCPTargets", "SetMCPIcon", "ResetMCPIcon", ".ReadForSnapshot("} {
		if bytes.Contains(source, []byte(forbidden)) {
			t.Fatal("scope expanded", forbidden)
		}
	}
	// Configured v4 is still rejected, even though preparation fixtures parse it.
	o, _, _, _ := sharedTargetFixture(t, sharedTargetNPM)
	if _, err := parseBoundRuntimeServiceMetadata(o.editor.view.Load().bytes, o.binding); err != nil {
		t.Fatal("configured bound v4 reader failed", err)
	}
}
