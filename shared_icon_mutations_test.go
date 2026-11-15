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

func sharedIconMutationFixture(t *testing.T, source string) (*SharedIconOperations, *http.Request, sharedTargetRef, sharedMutationCAS, time.Time) {
	t.Helper()
	var o *SharedIconOperations
	var r *http.Request
	var ref sharedTargetRef
	var at time.Time
	if source == "ram" || source == "durable" {
		o, r, ref, at = sharedApplicationFixture(t, source == "durable")
	} else {
		o, r, ref, at = sharedTargetFixture(t, sharedTargetKind(source))
	}
	form := sharedTargetResult(t, o, r, ref, nil)
	return o, r, ref, form.sharedMutationCAS, at
}

// TEST ASSEMBLY ONLY, BELOW CONFIGURED STARTUP. Public setters never succeed in
// S4F. This combines private guarded preparation with the existing SAME-VERSION
// v4 low-level writer. No caller can supply a noop or alternate authority check.
// Production's gated whole-commit branch is separately inspected below.
func sharedIconTestCommit(o *SharedIconOperations, r *http.Request, ref sharedTargetRef, cas sharedMutationCAS, icon string) error {
	identity, err := o.mcpTargetIdentity(r, "")
	if err != nil {
		return err
	}
	login := identity.Login
	if ref.Kind == sharedTargetApplication && o.personal != nil {
		o.personal.mu.Lock()
		defer o.personal.mu.Unlock()
		at := o.now()
		if _, err := o.mcpTargetIdentity(r, login); err != nil {
			return err
		}
		if !o.sharedTargetFresh(o.cache.Get(), at) {
			return errMetadataStale
		}
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	document, err := o.prepareMCPIconLocked(r, ref, cas, icon, login)
	if err != nil {
		return err
	}
	return o.editor.commit(cas.Revision, document, serviceMetadataWriteIcon, func() error {
		return o.recheckMCPIconLocked(r, ref, cas, login)
	})
}

func TestSharedIconMutationPublicGateAndProductionSeams(t *testing.T) {
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		t.Run(source, func(t *testing.T) {
			o, r, ref, cas, _ := sharedIconMutationFixture(t, source)
			old := o.editor.view.Load()
			body := &mcpGuardBody{}
			r.Body = body
			o.editor.config = nil
			o.now = func() time.Time { t.Error("misconfigured setter sampled clock"); return time.Time{} }
			if o.personal != nil {
				o.personal.mu.Lock()
				defer o.personal.mu.Unlock()
			}
			o.editor.mu.Lock()
			for _, icon := range []string{"generic", "", "invalid"} {
				want := errMetadataStale
				if !validServiceIcon(icon) {
					want = errMetadataInvalidFields
				}
				if err := o.SetMCPIcon(r, mcpSharedIconRequest{Target: ref, sharedMutationCAS: cas, Icon: icon}); !errors.Is(err, want) {
					t.Fatal(err)
				}
			}
			if err := o.ResetMCPIcon(r, mcpSharedResetRequest{Target: ref, sharedMutationCAS: cas}); !errors.Is(err, errMetadataStale) {
				t.Fatal(err)
			}
			if err := o.mutateMCPIcon(r, ref, cas, "generic"); !errors.Is(err, errMetadataStale) {
				t.Fatal("private whole-commit bypass", err)
			}
			o.editor.mu.Unlock()
			if body.reads != 0 || body.closes != 0 {
				t.Fatal("body touched")
			}
			sharedNPMUnchanged(t, o.editor, old, old.bytes)
		})
	}
	var nilOps *SharedIconOperations
	if !errors.Is(nilOps.SetMCPIcon(nil, mcpSharedIconRequest{Icon: "generic"}), errMetadataStale) || !errors.Is(nilOps.ResetMCPIcon(nil, mcpSharedResetRequest{}), errMetadataStale) {
		t.Fatal("nil receiver gate")
	}
	if _, present := reflect.TypeOf(mcpSharedResetRequest{}).FieldByName("Icon"); present {
		t.Fatal("reset carries icon")
	}
	// Static assertion is explicitly evidence for the dormant production wiring,
	// not a test of public success with a mutable gate.
	f, err := parser.ParseFile(token.NewFileSet(), "shared_icon_mutations.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	commits, guards, locks := 0, 0, []string{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "SetMCPIcon" || fn.Name.Name == "ResetMCPIcon" || fn.Name.Name == "mutateMCPIcon" {
			first, ok := fn.Body.List[0].(*ast.IfStmt)
			if !ok {
				t.Fatal("entry not gated")
			}
			not, ok := first.Cond.(*ast.UnaryExpr)
			if !ok || not.Op != token.NOT || not.X.(*ast.Ident).Name != "serviceMetadataV4RuntimeEnabled" {
				t.Fatal("wrong immutable gate")
			}
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "serviceMetadataV4RuntimeEnabled" {
				guards++
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "commit" {
				commits++
				if fn.Name.Name != "mutateMCPIcon" || len(call.Args) != 4 || call.Args[2].(*ast.Ident).Name != "serviceMetadataWriteIcon" {
					t.Fatal("alternate commit path")
				}
				closure, ok := call.Args[3].(*ast.FuncLit)
				if !ok || len(closure.Body.List) != 2 {
					t.Fatal("not a core-owned gated final closure")
				}
				check, ok := closure.Body.List[0].(*ast.IfStmt)
				if !ok || check.Cond.(*ast.UnaryExpr).X.(*ast.Ident).Name != "serviceMetadataV4RuntimeEnabled" {
					t.Fatal("missing final gate")
				}
				ret := closure.Body.List[1].(*ast.ReturnStmt)
				if ret.Results[0].(*ast.CallExpr).Fun.(*ast.SelectorExpr).Sel.Name != "recheckMCPIconLocked" {
					t.Fatal("missing mandatory authority")
				}
			}
			if fn.Name.Name == "mutateMCPIcon" && sel.Sel.Name == "Lock" {
				mu := sel.X.(*ast.SelectorExpr)
				locks = append(locks, mu.X.(*ast.SelectorExpr).Sel.Name)
			}
			return true
		})
	}
	if commits != 1 || guards != 5 || !reflect.DeepEqual(locks, []string{"personal", "editor"}) {
		t.Fatal("production seam drift", commits, guards, locks)
	}
}

func TestSharedIconMutationPurePromotionLosslessAndDetached(t *testing.T) {
	for _, provider := range []controlPlaneProvider{controlPlaneHeadscale, controlPlaneTailscale} {
		for _, version := range []int{1, 2, 3, 4} {
			t.Run(fmt.Sprintf("%s/v%d", provider, version), func(t *testing.T) {
				name, link, category, icon, zero := "Authored", "https://example.test/app?q=1#part", "Tools", "grafana", 0
				d := serviceMetadataDocument{Version: version, Services: []serviceMetadataEntry{{ProxyHostID: 3, Name: &name, URLNull: true}, {ProxyHostID: 9, NameNull: true, URL: &link}}}
				if version >= 2 {
					d.Services[0].Category, d.Services[0].Order = &category, &zero
				}
				if version >= 3 {
					d.Services[0].Icon = &icon
				}
				binding := serviceMetadataBinding{Namespace: "source", ControlPlane: provider}
				if version == 4 {
					d.Namespace, d.ControlPlane = binding.Namespace, binding.ControlPlane
					d.Machines = []machineIconEntry{}
					d.Applications = []applicationIconEntry{}
				}
				before := cloneServiceMetadataDocument(d)
				got, err := sharedIconDocumentV4(d, binding)
				if err != nil || got.Version != 4 || got.Namespace != binding.Namespace || got.ControlPlane != provider || got.Machines == nil || got.Applications == nil || !reflect.DeepEqual(got.Services, d.Services) {
					t.Fatal("promotion", err)
				}
				if _, err = serializeServiceMetadataDocument(got); err != nil {
					t.Fatal(err)
				}
				*got.Services[0].Name = "Changed"
				*got.Services[1].URL = "https://other.example/"
				if version >= 2 {
					*got.Services[0].Category = "Other"
					*got.Services[0].Order = 1
				}
				if version >= 3 {
					*got.Services[0].Icon = "generic"
				}
				if !reflect.DeepEqual(d, before) {
					t.Fatal("shallow clone")
				}
				for _, bad := range []serviceMetadataBinding{{}, {Namespace: "Bad", ControlPlane: provider}, {Namespace: "source", ControlPlane: "invalid"}} {
					if _, err := sharedIconDocumentV4(d, bad); err == nil {
						t.Fatal("invalid binding")
					}
				}
				if version == 4 {
					if _, err := sharedIconDocumentV4(d, serviceMetadataBinding{Namespace: "other", ControlPlane: provider}); err == nil {
						t.Fatal("rebound v4")
					}
				}
			})
		}
	}
	// Complete original validation precedes acquiring v4's null-only rule.
	for _, bad := range []string{"null-only", "icon-v1", "category-v1", "duplicate", "presence", "version", "nil-services", "v4-fields", "order", "machine", "application"} {
		t.Run(bad, func(t *testing.T) {
			d := metadataV4(t).document
			switch bad {
			case "null-only":
				d = serviceMetadataDocument{Version: 3, Services: []serviceMetadataEntry{{ProxyHostID: 3, NameNull: true}}}
			case "icon-v1":
				d = serviceMetadataDocument{Version: 1, Services: []serviceMetadataEntry{{ProxyHostID: 3, Icon: &[]string{"generic"}[0]}}}
			case "category-v1":
				d = serviceMetadataDocument{Version: 1, Services: []serviceMetadataEntry{{ProxyHostID: 3, Category: &[]string{"Tools"}[0]}}}
			case "duplicate":
				d.Services = append(d.Services, d.Services[0])
			case "presence":
				d.Services[0].Name = &[]string{"Name"}[0]
				d.Services[0].NameNull = true
			case "version":
				d.Version = 5
			case "nil-services":
				d.Services = nil
			case "v4-fields":
				d.Version = 3
			case "order":
				d.Services[0].Order = &[]int{-1}[0]
			case "machine":
				d.Machines[0].Icon = "bad"
			case "application":
				d.Applications[0].TargetDigest = "bad"
			}
			before := cloneServiceMetadataDocument(d)
			if got, err := sharedIconDocumentV4(d, serviceMetadataBinding{Namespace: d.Namespace, ControlPlane: d.ControlPlane}); err == nil || !reflect.DeepEqual(got, serviceMetadataDocument{}) {
				t.Fatal("invalid original repaired")
			}
			if !reflect.DeepEqual(d, before) {
				t.Fatal("invalid input changed")
			}
		})
	}
}

func TestSharedIconMutationTypedValidation(t *testing.T) {
	o, r, ref, cas, _ := sharedIconMutationFixture(t, "npm")
	for _, bad := range []string{"kind", "handle", "padbits", "empty-doc", "long-doc", "empty-target", "long-target", "icon", "empty-login"} {
		t.Run(bad, func(t *testing.T) {
			rf, cs, icon, login := ref, cas, "generic", "alice@example.com"
			switch bad {
			case "kind":
				rf.Kind = "NPM"
			case "handle":
				rf.Handle = "3"
			case "padbits":
				rf.Handle = strings.Repeat("A", 42) + "B"
			case "empty-doc":
				cs.Revision = ""
			case "long-doc":
				cs.Revision = strings.Repeat("x", 257)
			case "empty-target":
				cs.TargetRevision = ""
			case "long-target":
				cs.TargetRevision = strings.Repeat("x", 257)
			case "icon":
				icon = "https://evil.example/icon.svg"
			case "empty-login":
				login = ""
			}
			o.editor.mu.Lock()
			got, err := o.prepareMCPIconLocked(r, rf, cs, icon, login)
			o.editor.mu.Unlock()
			if err == nil || !reflect.DeepEqual(got, serviceMetadataDocument{}) {
				t.Fatal("invalid typed mutation accepted")
			}
		})
	}
	if validServiceIcon("") {
		t.Fatal("set must require nonempty icon")
	}
	for _, icon := range serviceIconIDs {
		if err := validateSharedIconMutation(ref, cas, icon); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSharedIconMutationAllKindsPreserveWholeDocumentAndPrivateState(t *testing.T) {
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		for _, reset := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/reset=%v", source, reset), func(t *testing.T) {
				o, r, ref, _, _ := sharedIconMutationFixture(t, source)
				d := cloneServiceMetadataDocument(o.editor.view.Load().metadata.document)
				category, zero := "Tools", 0
				d.Services[0].Category, d.Services[0].Order = &category, &zero
				d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: 88, NameNull: true, URLNull: true}, serviceMetadataEntry{ProxyHostID: 99, Name: &[]string{"Other"}[0], URLNull: true, Icon: &[]string{"gitea"}[0]})
				d.Machines = append(d.Machines, machineIconEntry{DeviceIDType: deviceIDKindNodeID, DeviceID: "foreign-machine", Icon: "portainer"})
				d.Applications = append(d.Applications, applicationIconEntry{TargetDigest: "sha256:" + strings.Repeat("b", 64), Icon: "immich"})
				sharedTargetReplaceView(t, o, d)
				form := sharedTargetResult(t, o, r, ref, nil)
				before := o.editor.view.Load()
				var state PersonalServicesState
				var private []byte
				var version personalStorageVersion
				if o.personal != nil {
					state = sharedApplicationState(t, o)
					if o.personal.storage != nil {
						private, _ = os.ReadFile(o.personal.config.File)
						_, version, _ = o.personal.storage.Read()
					}
				}
				icon := "jellyfin"
				if reset {
					icon = ""
				}
				if err := sharedIconTestCommit(o, r, ref, form.sharedMutationCAS, icon); err != nil {
					t.Fatal(err)
				}
				after := o.editor.view.Load()
				want := cloneServiceMetadataDocument(before.metadata.document)
				switch ref.Kind {
				case sharedTargetNPM:
					want.Services[0].Icon = nil
					if !reset {
						want.Services[0].Icon = &icon
					}
				case sharedTargetMachine:
					if reset {
						want.Machines = want.Machines[1:]
					} else {
						want.Machines[0].Icon = icon
					}
				case sharedTargetApplication:
					_, digest := sharedApplicationReference(t, o, "alice@example.com")
					for i, row := range want.Applications {
						if row.TargetDigest == digest {
							if reset {
								want.Applications = append(want.Applications[:i], want.Applications[i+1:]...)
							} else {
								want.Applications[i].Icon = icon
							}
							break
						}
					}
				}
				encoded, err := serializeServiceMetadataDocument(want)
				if err != nil || !bytes.Equal(after.bytes, encoded) {
					t.Fatal("unrelated fields/arrays changed", err)
				}
				original, _ := serializeServiceMetadataDocument(before.metadata.document)
				if !bytes.Equal(original, before.bytes) {
					t.Fatal("old view mutated")
				}
				if after.revision == before.revision {
					t.Fatal("shared revision not advanced")
				}
				if o.personal != nil {
					if !reflect.DeepEqual(state, sharedApplicationState(t, o)) {
						t.Fatal("private state/revision changed")
					}
					if o.personal.storage != nil {
						disk, _ := os.ReadFile(o.personal.config.File)
						_, v, err := o.personal.storage.Read()
						if err != nil || v != version || !bytes.Equal(disk, private) {
							t.Fatal("private file/version changed")
						}
					}
				}
				if _, err := parseBoundRuntimeServiceMetadata(after.bytes, o.binding); err != nil {
					t.Fatal("configured reader unexpectedly enabled")
				}
			})
		}
	}
}

func TestSharedIconMutationLegacyPromotionStillWriterBlocked(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		for _, reset := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/reset=%v", version, reset), func(t *testing.T) {
				o, r, ref, _, _ := sharedIconMutationFixture(t, "npm")
				raw := []byte(fmt.Sprintf(`{"version":%d,"services":[{"proxy_host_id":3,"name":"Original","url":null}]}`, version))
				m, err := parseServiceMetadata(raw)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(o.editor.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				o.editor.view.Store(&serviceMetadataEditorView{metadata: m, bytes: raw, revision: o.editor.digest("file", raw)})
				form := sharedTargetResult(t, o, r, ref, nil)
				old := o.editor.view.Load()
				icon := "generic"
				if reset {
					icon = ""
				}
				o.editor.mu.Lock()
				d, err := o.prepareMCPIconLocked(r, ref, form.sharedMutationCAS, icon, "alice@example.com")
				o.editor.mu.Unlock()
				if err != nil || d.Version != 4 || d.Services[0].Name == nil || *d.Services[0].Name != "Original" || !d.Services[0].URLNull {
					t.Fatal("pure promotion", err)
				}
				hit := false
				o.editor.failpoint = func(string) error { hit = true; return nil }
				if err := o.editor.commit(form.Revision, d, serviceMetadataWriteNPM, func() error { t.Fatal("NPM promotion final check"); return nil }); !errors.Is(err, errMetadataStorage) || hit {
					t.Fatal("legacy transition allowed", err, hit)
				}
				sharedNPMUnchanged(t, o.editor, old, raw)
				// Ordinary full dialog remains its independent v3 write decision.
				get := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
				bf, err := o.GetBrowserNPM(get, 3)
				if err != nil {
					t.Fatal(err)
				}
				err = o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, serviceMetadataEditRequest{Action: "save", Revision: bf.Revision, TargetRevision: bf.TargetRevision, CSRF: bf.CSRF, Name: "Browser", Icon: "generic"})
				if err != nil || o.editor.view.Load().metadata.document.Version != 3 {
					t.Fatal("browser promoted", err)
				}
			})
		}
	}
}

func TestSharedIconMutationAggregateEncodedBoundsAndShrinking(t *testing.T) {
	binding := serviceMetadataBinding{Namespace: "source", ControlPlane: controlPlaneTailscale}
	d := serviceMetadataDocument{Version: 4, Namespace: binding.Namespace, ControlPlane: binding.ControlPlane, Services: []serviceMetadataEntry{}, Machines: []machineIconEntry{}, Applications: []applicationIconEntry{}}
	icon := "generic"
	for i := 1; i <= maxServiceMetadataEntries; i++ {
		d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: i, Icon: &icon})
	}
	promoted, err := sharedIconDocumentV4(d, binding)
	if err != nil {
		t.Fatal(err)
	}
	key := ProviderDeviceID{Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: "node"}
	if _, err := mutateMachineMetadataIcon(promoted, key, "generic"); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	if shrunk, err := mutateServiceMetadataIcon(promoted, 1, ""); err != nil || len(shrunk.Services) != 1023 {
		t.Fatal("aggregate shrinking", err)
	}
	d.Services = append(d.Services, d.Services[0])
	if _, err := sharedIconDocumentV4(d, binding); err == nil {
		t.Fatal("invalid aggregate original")
	}
	// Compact input is valid but canonical escaping/indentation crosses the cap.
	// Grow by one bounded Machine row; removing exactly that row must still work.
	d.Services = []serviceMetadataEntry{}
	link := "https://example.test/" + strings.Repeat("&", 2027)
	for i := 1; i <= 21; i++ {
		d.Services = append(d.Services, serviceMetadataEntry{ProxyHostID: i, URL: &link})
	}
	for i := 0; i < 1000; i++ {
		key.Value = fmt.Sprintf("node-%04d-%s", i, strings.Repeat("a", 110))
		d.Machines = append(d.Machines, machineIconEntry{DeviceIDType: key.Kind, DeviceID: key.Value, Icon: "generic"})
		if _, err := serializeServiceMetadataDocument(d); err != nil {
			break
		}
	}
	if _, err := serializeServiceMetadataDocument(d); err == nil {
		t.Fatal("expected oversized canonical fixture")
	}
	if err := validateServiceMetadataDocumentModel(d); err != nil {
		t.Fatal("invalid original model", err)
	}
	promoted, err = sharedIconDocumentV4(d, binding)
	if err != nil {
		t.Fatal("premature output bound", err)
	}
	if _, err := mutateMachineMetadataIcon(promoted, key, "generic"); err == nil {
		t.Fatal("oversized unchanged result")
	}
	if shrunk, err := mutateMachineMetadataIcon(promoted, key, ""); err != nil || len(shrunk.Machines) != len(d.Machines)-1 {
		t.Fatal("shrinking icon reset rejected", err)
	}
}

func TestSharedIconMutationConflictAndVisibilityOrder(t *testing.T) {
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		for _, change := range []string{"target-conflict", "document-conflict", "missing", "foreign", "invisible", "unsupported-id", "replacement"} {
			t.Run(source+"/"+change, func(t *testing.T) {
				o, r, ref, cas, _ := sharedIconMutationFixture(t, source)
				old := o.editor.view.Load()
				want := errMetadataConflict
				switch change {
				case "target-conflict":
					cas.TargetRevision = "old"
				case "document-conflict":
					cas.Revision = "old"
				case "missing":
					ref.Handle = strings.Repeat("A", 43)
					want = errMetadataNotVisible
				case "foreign":
					r = sharedTargetRequest(t, o, "bob@example.com")
					want = errMetadataNotVisible
				case "invisible":
					o.cache.Get().Policy = &Policy{}
					want = errMetadataNotVisible
				case "unsupported-id":
					if source == "npm" {
						o.cache.Get().ProxyHosts = nil
					} else {
						o.cache.Get().PresentationDeviceIDsByNodeID = nil
					}
					want = errMetadataNotVisible
				case "replacement":
					if source == "npm" {
						o.cache.Get().ProxyHosts = nil
					} else {
						o.cache.Get().PresentationDeviceIDsByNodeID["node-1"] = ProviderDeviceID{Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: "replacement"}
					}
					want = errMetadataNotVisible
				}
				if want == errMetadataNotVisible {
					cas.Revision, cas.TargetRevision = "wrong-doc", "wrong-target"
				}
				if err := sharedIconTestCommit(o, r, ref, cas, "generic"); !errors.Is(err, want) {
					t.Fatalf("%v want %v", err, want)
				}
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			})
		}
	}
}

func TestSharedIconMutationLateConcreteAuthority(t *testing.T) {
	changes := []string{"source", "raw-login", "both-logins", "origin", "fetch", "host", "method", "version", "content", "path", "query", "authorization", "cookie", "context", "cancel", "allowlist", "mode", "shared-origin", "mcp-origin", "binding", "namespace", "provider", "stale", "disabled", "nil-writer", "visibility", "target", "owner", "private-uncertain", "grant", "ssh", "capability", "typed-id"}
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		for _, change := range changes {
			t.Run(source+"/"+change, func(t *testing.T) {
				o, r, ref, cas, at := sharedIconMutationFixture(t, source)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				old := o.editor.view.Load()
				want := errMetadataForbidden
				hit := false
				o.editor.failpoint = func(stage string) error {
					if stage != "recheck" {
						return nil
					}
					hit = true
					switch change {
					case "source":
						r.RemoteAddr = "192.0.2.1:1"
					case "raw-login":
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					case "both-logins":
						IdentityFromContext(r.Context()).Login = "bob@example.com"
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					case "origin":
						r.Header.Set("Origin", "https://foreign.example")
					case "fetch":
						r.Header.Set("Sec-Fetch-Site", "same-origin")
					case "host":
						r.Host = "foreign.example"
					case "method":
						r.Method = http.MethodGet
					case "version":
						r.Header.Set("MCP-Protocol-Version", "invalid")
					case "content":
						r.Header.Set("Content-Type", "text/plain")
					case "path":
						r.URL.Path = "/mcp/"
					case "query":
						r.URL.ForceQuery = true
					case "authorization":
						r.Header["Authorization"] = []string{""}
					case "cookie":
						r.Header["Cookie"] = []string{""}
					case "context":
						*r = *r.WithContext(context.Background())
					case "cancel":
						cancel()
					case "allowlist":
						delete(o.editor.config.Logins, "alice@example.com")
					case "mode":
						o.mode = serviceEditBrowser
					case "shared-origin":
						o.editor.config.Origin = "http://foreign.example"
					case "mcp-origin":
						o.mcp.Origin = "http://foreign.example"
					case "binding":
						o.editor.config.Binding.Namespace = "changed"
						want = errMetadataStale
					case "namespace":
						o.binding.Namespace = ""
						want = errMetadataStale
					case "provider":
						o.cache.Get().ControlPlane.Provider = controlPlaneHeadscale
						want = errMetadataStale
					case "stale":
						o.now = func() time.Time { return at.Add(91 * time.Second) }
						want = errMetadataStale
					case "disabled":
						o.editor.disabled = true
						want = errMetadataUncertain
					case "nil-writer":
						o.editor.view.Store(nil)
						want = errMetadataStale
					case "visibility":
						o.cache.Get().Policy = &Policy{}
						want = errMetadataNotVisible
					case "target":
						want = errMetadataConflict
						if source == "npm" {
							o.cache.Get().ProxyHosts[2].ForwardPort++
						} else if source == "machine" {
							o.cache.Get().Nodes[0].Name = "renamed.tailnet.ts.net"
						} else if o.personal.storage != nil {
							o.personal.storage.mu.Lock()
							o.personal.storage.confirmed.Collections[0].Revision = "8"
							o.personal.storage.mu.Unlock()
						} else {
							c := o.personal.collections["alice@example.com"]
							c.revision++
							o.personal.collections["alice@example.com"] = c
						}
					case "owner":
						if o.personal != nil {
							o.cache.Get().TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-owner", DirectMember: true}
							want = errMetadataNotVisible
							if source == "ram" {
								want = errMetadataConflict
							}
						} else {
							o.cache.Get().Policy = &Policy{}
							want = errMetadataNotVisible
						}
					case "private-uncertain":
						if o.personal != nil && o.personal.storage != nil {
							o.personal.storage.mu.Lock()
							o.personal.storage.uncertain = true
							o.personal.storage.mu.Unlock()
							want = errMetadataUncertain
						} else {
							o.cache.Get().Policy = &Policy{}
							want = errMetadataNotVisible
						}
					case "grant":
						o.cache.Get().Policy.Grants = nil
						if source == "npm" {
							o.cache.Get().Policy.ACLs = nil
						}
						want = errMetadataNotVisible
					case "ssh":
						o.cache.Get().Policy.SSH.Rules = nil
						if source != "machine" {
							o.cache.Get().Policy = &Policy{}
						}
						want = errMetadataNotVisible
					case "capability":
						o.cache.Get().MachineSSHCapableByID = nil
						if source != "machine" {
							o.cache.Get().Policy = &Policy{}
						}
						want = errMetadataNotVisible
					case "typed-id":
						o.cache.Get().PresentationDeviceIDsByNodeID = nil
						if source == "npm" {
							o.cache.Get().ProxyHosts = nil
						}
						want = errMetadataNotVisible
					}
					return nil
				}
				err := sharedIconTestCommit(o, r, ref, cas, "jellyfin")
				if !hit || !errors.Is(err, want) {
					t.Fatalf("hit %v got %v want %v", hit, err, want)
				}
				if change == "nil-writer" {
					if o.editor.view.Load() != nil {
						t.Fatal("published after nil writer")
					}
					o.editor.view.Store(old)
				}
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			})
		}
	}
}

func TestSharedIconMutationFinalOwnedReadReplacement(t *testing.T) {
	for _, source := range []string{"ram", "durable"} {
		for _, change := range []string{"identical", "owner-removed", "owner-reassigned", "grant-revoked", "device-replaced", "stale", "provider"} {
			t.Run(source+"/"+change, func(t *testing.T) {
				o, r, ref, cas, at := sharedIconMutationFixture(t, source)
				old := o.cache.Get()
				before := o.editor.view.Load()
				policyBefore, _ := json.Marshal(old.Policy)
				next := *old
				policy := *old.Policy
				next.Policy = &policy
				next.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{}
				for k, v := range old.TailscaleUsersByLogin {
					next.TailscaleUsersByLogin[k] = v
				}
				next.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{}
				for k, v := range old.PresentationDeviceIDsByNodeID {
					next.PresentationDeviceIDsByNodeID[k] = v
				}
				want := errMetadataNotVisible
				switch change {
				case "identical":
					want = nil
				case "owner-removed":
					delete(next.TailscaleUsersByLogin, "alice@example.com")
				case "owner-reassigned":
					next.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "replacement-owner", DirectMember: true}
				case "grant-revoked":
					next.Policy.Grants = nil
				case "device-replaced":
					key := next.PresentationDeviceIDsByNodeID["node-1"]
					key.Value = "replacement-device"
					next.PresentationDeviceIDsByNodeID["node-1"] = key
				case "stale":
					next.UpdatedAt = at.Add(-91 * time.Second)
					want = errMetadataStale
				case "provider":
					next.ControlPlane.Provider = controlPlaneHeadscale
					want = errMetadataStale
				}
				hit := false
				o.editor.failpoint = func(stage string) error {
					if stage == "recheck" {
						// Final selected recheck: clock before selection, durable owner
						// clock if applicable, then post-owned-read current-cache checkpoint.
						calls, trigger := 0, 2
						if source == "durable" {
							trigger = 3
						}
						o.now = func() time.Time {
							calls++
							if calls == trigger {
								hit = true
								o.cache.data.Store(&next)
							}
							return at
						}
					}
					return nil
				}
				err := sharedIconTestCommit(o, r, ref, cas, "jellyfin")
				if !hit || !errors.Is(err, want) {
					t.Fatalf("replacement checkpoint %v got %v want %v", hit, err, want)
				}
				if want != nil {
					sharedNPMUnchanged(t, o.editor, before, before.bytes)
				}
				policyAfter, _ := json.Marshal(old.Policy)
				if !bytes.Equal(policyBefore, policyAfter) || old.TailscaleUsersByLogin["alice@example.com"].ID != "alice-id" || old.PresentationDeviceIDsByNodeID["node-1"].Value != "node-1" || old.UpdatedAt != at || old.ControlPlane.Provider != controlPlaneTailscale {
					t.Fatal("mutated captured snapshot, not replacement")
				}
			})
		}
	}
}

func TestSharedIconMutationWaitFreshnessAndLockIsolation(t *testing.T) {
	for _, lock := range []string{"personal", "editor", "storage"} {
		for _, change := range []string{"stale", "cancel"} {
			t.Run(lock+"/"+change, func(t *testing.T) {
				o, r, ref, cas, at := sharedIconMutationFixture(t, "durable")
				old := o.editor.view.Load()
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				var clockMu sync.Mutex
				clock := at
				o.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
				var held *sync.Mutex
				switch lock {
				case "personal":
					held = &o.personal.mu
				case "editor":
					held = &o.editor.mu
				case "storage":
					held = &o.personal.storage.mu
				}
				held.Lock()
				started, done := make(chan struct{}), make(chan error, 1)
				go func() { close(started); done <- sharedIconTestCommit(o, r, ref, cas, "jellyfin") }()
				<-started
				if change == "stale" {
					clockMu.Lock()
					clock = at.Add(91 * time.Second)
					clockMu.Unlock()
				} else {
					cancel()
				}
				held.Unlock()
				want := errMetadataStale
				if change == "cancel" {
					want = errMetadataForbidden
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("%v want %v", err, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("deadlock")
				}
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			})
		}
	}
	for _, source := range []string{"npm", "machine"} {
		t.Run(source+"/private-independent", func(t *testing.T) {
			o, r, ref, cas, _ := sharedIconMutationFixture(t, source)
			o.personal = &PersonalServicesStore{closed: true, storage: &personalSnapshotStorage{uncertain: true}}
			o.personal.mu.Lock()
			o.personal.storage.mu.Lock()
			done := make(chan error, 1)
			go func() { done <- sharedIconTestCommit(o, r, ref, cas, "jellyfin") }()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("private lock blocked unrelated source")
			}
			o.personal.storage.mu.Unlock()
			o.personal.mu.Unlock()
		})
	}
}

func TestSharedIconMutationFinalPrivateWaitRechecksClockAndRequest(t *testing.T) {
	for _, change := range []string{"stale", "cancel", "healthy"} {
		t.Run(change, func(t *testing.T) {
			o, r, ref, cas, at := sharedIconMutationFixture(t, "durable")
			old := o.editor.view.Load()
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			synced, startFinal, reached := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			clock, final, signaled := at, false, false
			o.now = func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				if final && !signaled {
					signaled = true
					close(reached)
				}
				return clock
			}
			o.editor.failpoint = func(stage string) error {
				if stage == "recheck" {
					close(synced)
					<-startFinal
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { done <- sharedIconTestCommit(o, r, ref, cas, "jellyfin") }()
			<-synced
			o.personal.storage.mu.Lock()
			mu.Lock()
			final = true
			mu.Unlock()
			close(startFinal)
			<-reached
			// Only synchronized time/cancellation changes here, never racing headers.
			// The healthy case also proves no recursive storage lock at final check.
			want := errMetadataForbidden
			switch change {
			case "stale":
				mu.Lock()
				clock = at.Add(91 * time.Second)
				mu.Unlock()
				want = errMetadataStale
			case "cancel":
				cancel()
			case "healthy":
				want = nil
			}
			o.personal.storage.mu.Unlock()
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("%v want %v", err, want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("recursive storage lock")
			}
			if want != nil {
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			}
		})
	}
}

func TestSharedIconMutationConcurrentPersonalEditPinned(t *testing.T) {
	for _, source := range []string{"ram", "durable"} {
		t.Run(source, func(t *testing.T) {
			o, r, ref, cas, at := sharedIconMutationFixture(t, source)
			state := sharedApplicationState(t, o)
			mutation := PersonalServicesMutation{StoreID: state.StoreID, Generation: state.Generation, Revision: state.Revision, CSRF: o.personal.csrf("alice@example.com", at), Action: "update", ID: state.Document.Entries[0].ID, Name: "Edited after shared commit", URL: state.Document.Entries[0].URL}
			var request *http.Request
			IdentityMiddleware(o.trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { request = r })).ServeHTTP(httptest.NewRecorder(), personalHTTPRequest(http.MethodPost, nil))
			if request == nil {
				t.Fatal("middleware fixture")
			}
			reached, release := make(chan struct{}), make(chan struct{})
			o.editor.failpoint = func(stage string) error {
				if stage == "recheck" {
					close(reached)
					<-release
				}
				return nil
			}
			saved := make(chan error, 1)
			go func() { saved <- sharedIconTestCommit(o, r, ref, cas, "jellyfin") }()
			<-reached
			if o.personal.mu.TryLock() {
				o.personal.mu.Unlock()
				close(release)
				t.Fatal("personal not held across sync/final commit")
			}
			if o.editor.mu.TryLock() {
				o.editor.mu.Unlock()
				close(release)
				t.Fatal("editor not held")
			}
			started, edited := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				_, _, err := o.personal.MutateBrowser(request, mutation, o.cache, o.trusted, o.interval, func() time.Time { return at })
				edited <- err
			}()
			<-started
			select {
			case err := <-edited:
				close(release)
				t.Fatal("personal edit escaped pin", err)
			default:
			}
			close(release)
			select {
			case err := <-saved:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("shared deadlock")
			}
			select {
			case err := <-edited:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("personal deadlock")
			}
			o.editor.failpoint = nil
			form := sharedTargetResult(t, o, r, ref, nil)
			if form.TargetRevision == cas.TargetRevision || form.Name != mutation.Name {
				t.Fatal("personal edit not reflected")
			}
			cas.Revision = form.Revision
			if err := sharedIconTestCommit(o, r, ref, cas, "generic"); !errors.Is(err, errMetadataConflict) {
				t.Fatal("old personal CAS silently merged", err)
			}
		})
	}
}

func TestSharedIconMutationStablePollForeignChangesAndCrossSourceCAS(t *testing.T) {
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		t.Run(source, func(t *testing.T) {
			o, r, ref, cas, at := sharedIconMutationFixture(t, source)
			if o.personal != nil {
				sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
					for i := range s.Collections {
						if s.Collections[i].Login == "bob@example.com" {
							s.Collections[i].Revision = "99"
							s.Collections[i].Document.Entries[0].Name = "Foreign changed"
						}
					}
				})
			}
			next := *o.cache.Get()
			next.UpdatedAt = at.Add(time.Second)
			next.ProxyHosts = append([]ProxyHost(nil), next.ProxyHosts...)
			for i, j := 0, len(next.ProxyHosts)-1; i < j; i, j = i+1, j-1 {
				next.ProxyHosts[i], next.ProxyHosts[j] = next.ProxyHosts[j], next.ProxyHosts[i]
			}
			o.cache.data.Store(&next)
			o.now = func() time.Time { return at.Add(time.Second) }
			// Valid MCP browser admission is distinct from the ordinary CSRF API.
			r.Header.Set("Origin", o.origin)
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
			if err := sharedIconTestCommit(o, r, ref, cas, "jellyfin"); err != nil {
				t.Fatal("stable evidence changed", err)
			}
			// A different source save invalidates document CAS even if this target
			// remains identical. No silent rebase or field-level merge.
			form := sharedTargetResult(t, o, r, ref, nil)
			other := sharedTargetNPM
			if ref.Kind == sharedTargetNPM {
				other = sharedTargetMachine
			}
			otherRef := o.sharedTargetHandle("alice@example.com", other, "3")
			if other == sharedTargetMachine {
				otherRef = o.sharedTargetHandle("alice@example.com", other, "id", "node-1")
			}
			otherForm := sharedTargetResult(t, o, r, otherRef, nil)
			if err := sharedIconTestCommit(o, r, otherRef, otherForm.sharedMutationCAS, "immich"); err != nil {
				t.Fatal(err)
			}
			old := o.editor.view.Load()
			if err := sharedIconTestCommit(o, r, ref, form.sharedMutationCAS, "generic"); !errors.Is(err, errMetadataConflict) {
				t.Fatal("cross-source merged", err)
			}
			sharedNPMUnchanged(t, o.editor, old, old.bytes)
		})
	}
	// Headscale NPM remains legacy-policy based, without human owner evidence.
	t.Run("headscale-npm", func(t *testing.T) {
		o, r, _, _, _ := sharedIconMutationFixture(t, "npm")
		o.binding.ControlPlane = controlPlaneHeadscale
		o.editor.config.Binding = o.binding
		d := cloneServiceMetadataDocument(o.editor.view.Load().metadata.document)
		d.ControlPlane = controlPlaneHeadscale
		d.Machines = []machineIconEntry{}
		d.Applications = []applicationIconEntry{}
		sharedTargetReplaceView(t, o, d)
		o.cache.Get().ControlPlane.Provider = controlPlaneHeadscale
		o.cache.Get().TailscaleUsersByLogin = nil
		ref := o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "3")
		form := sharedTargetResult(t, o, r, ref, nil)
		if err := sharedIconTestCommit(o, r, ref, form.sharedMutationCAS, "immich"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSharedIconMutationWriterFailureAndUncertainty(t *testing.T) {
	for _, source := range []string{"npm", "machine", "ram", "durable"} {
		for _, stage := range []string{"create", "write", "filesync", "close", "recheck", "rename", "attempt-error", "attempt-after-rename", "dirsync", "publish", "post-rename-cancel"} {
			t.Run(source+"/"+stage, func(t *testing.T) {
				o, r, ref, cas, _ := sharedIconMutationFixture(t, source)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				old := o.editor.view.Load()
				sentinel := errors.New("fixture")
				hit := false
				o.editor.failpoint = func(s string) error {
					if s == stage {
						hit = true
						return sentinel
					}
					if stage == "post-rename-cancel" && s == "dirsync" {
						hit = true
						cancel()
					}
					return nil
				}
				if strings.HasPrefix(stage, "attempt-") {
					o.editor.rename = func(a int, b string, c int, d string) error {
						hit = true
						if stage == "attempt-after-rename" {
							if err := unix.Renameat(a, b, c, d); err != nil {
								return err
							}
						}
						return sentinel
					}
				}
				err := sharedIconTestCommit(o, r, ref, cas, "jellyfin")
				uncertain := strings.HasPrefix(stage, "attempt-") || stage == "dirsync" || stage == "publish"
				want := errMetadataStorage
				if uncertain {
					want = errMetadataUncertain
				}
				if stage == "post-rename-cancel" {
					want = nil
				}
				if !hit || !errors.Is(err, want) {
					t.Fatalf("%v got %v want %v", hit, err, want)
				}
				if o.editor.disabled != uncertain {
					t.Fatal("uncertainty state")
				}
				if !uncertain && stage != "post-rename-cancel" {
					sharedNPMUnchanged(t, o.editor, old, old.bytes)
				}
				if uncertain {
					if o.editor.view.Load() != old {
						t.Fatal("uncertain published")
					}
					o.editor.failpoint = nil
					o.editor.rename = nil
					if err := sharedIconTestCommit(o, r, ref, cas, "generic"); !errors.Is(err, errMetadataUncertain) {
						t.Fatal("uncertain retry", err)
					}
					sharedTargetResult(t, o, r, ref, errMetadataUncertain)
					if page, err := o.prepareMCPTargetList(r, sharedTargetListRequest{Kind: ref.Kind}); !errors.Is(err, errMetadataUncertain) || !reflect.DeepEqual(page, sharedTargetPage{}) {
						t.Fatal("uncertain list", err)
					}
					if _, err := o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3); !errors.Is(err, errMetadataUncertain) {
						t.Fatal("uncertain browser get", err)
					}
					br := serviceMetadataEditRequest{Action: "reset", Revision: old.revision, TargetRevision: "old", CSRF: o.editor.csrf("alice@example.com", o.now())}
					if err := o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, br); !errors.Is(err, errMetadataUncertain) {
						t.Fatal("uncertain browser save", err)
					}
					if err := o.SetMCPIcon(r, mcpSharedIconRequest{Target: ref, sharedMutationCAS: cas, Icon: "generic"}); !errors.Is(err, errMetadataUncertain) {
						t.Fatal("public uncertainty bypass")
					}
					if err := o.ResetMCPIcon(r, mcpSharedResetRequest{Target: ref, sharedMutationCAS: cas}); !errors.Is(err, errMetadataUncertain) {
						t.Fatal("public reset uncertainty bypass")
					}
				}
				if stage == "post-rename-cancel" {
					disk, _ := os.ReadFile(o.editor.path)
					if o.editor.view.Load() == old || !bytes.Equal(disk, o.editor.view.Load().bytes) {
						t.Fatal("post-rename cancellation claimed rollback")
					}
				}
			})
		}
	}
}
