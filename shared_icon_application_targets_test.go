package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Safe preseeded private/shared files and actual IdentityMiddleware admission.
// No production reader gate, public tool or mutation capability is enabled.
func sharedApplicationFixture(t *testing.T, durable bool) (*SharedIconOperations, *http.Request, sharedTargetRef, time.Time) {
	t.Helper()
	o, r, _, at := sharedTargetFixture(t, sharedTargetNPM)
	data := o.cache.Get()
	data.Policy.Grants[0].BrowserSrc = []string{"alice@example.com", "bob@example.com"}
	data.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22"), mustGrantCapability(t, "tcp:443"), mustGrantCapability(t, "tcp:8443")}
	data.GrantRoleSelectorsByLogin["bob@example.com"] = []string{"autogroup:member"}
	data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{
		"alice@example.com": {ID: "alice-id", DirectMember: true}, "bob@example.com": {ID: "bob-id", DirectMember: true},
	}
	a, b := personalTestEntry(0), personalTestEntry(1)
	a.Name, a.Icon, a.URL = "Own application", "", "https://server.tailnet.ts.net/private-path"
	b.Name, b.Icon, b.URL = "Other authored name", "", a.URL
	records := []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "7", []PersonalService{a}), durableRecord("bob@example.com", "bob-id", "3", []PersonalService{b})}
	if durable {
		cfg := durableTestConfig(t, records)
		raw, err := serializePersonalSnapshot(personalStorageSnapshot{Version: 1, Namespace: o.binding.Namespace, StoreID: strings.Repeat("a", 32), Collections: records})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(cfg.File, raw, 0600); err != nil {
			t.Fatal(err)
		}
		cfg.Namespace = o.binding.Namespace
		o.personal = durableTestStore(t, cfg)
	} else {
		o.personal = personalTestStore(t)
		t.Cleanup(func() { _ = o.personal.Close() })
		o.personal.collections["alice@example.com"] = personalCollection{revision: 7, document: copyPersonalDocument(records[0].Document)}
		o.personal.collections["bob@example.com"] = personalCollection{revision: 3, document: copyPersonalDocument(records[1].Document)}
	}
	ref, digest := sharedApplicationReference(t, o, "alice@example.com")
	doc := cloneServiceMetadataDocument(o.editor.view.Load().metadata.document)
	doc.Applications = []applicationIconEntry{{TargetDigest: digest, Icon: "grafana"}}
	sharedTargetReplaceView(t, o, doc)
	return o, r, ref, at
}

func sharedApplicationReference(t *testing.T, o *SharedIconOperations, login string) (sharedTargetRef, string) {
	t.Helper()
	state, err := o.personal.ReadForSnapshot(login, o.cache.Get(), o.interval, o.now)
	if err != nil || !state.Initialized || len(state.Document.Entries) == 0 {
		t.Fatal("reference fixture state", err)
	}
	entry := state.Document.Entries[0]
	evaluator := newPersonalServiceEvaluator(&Identity{Login: login}, o.cache.Get(), o.now(), o.interval)
	evidence, active := evaluator.evaluate(entry)
	digest, ok := personalApplicationDigest(o.binding, o.cache.Get(), evidence)
	if !active || !ok {
		t.Fatal("reference fixture evidence")
	}
	return o.sharedTargetHandle(login, sharedTargetApplication, entry.ID, digest), digest
}

func sharedApplicationState(t *testing.T, o *SharedIconOperations) PersonalServicesState {
	t.Helper()
	state, err := o.personal.ReadForSnapshot("alice@example.com", o.cache.Get(), o.interval, o.now)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

// Fixtures alone can edit preseeded confirmed state; these changes are not an
// alternative production writer. Actual concurrent edits below use MutateBrowser.
func sharedApplicationChange(t *testing.T, o *SharedIconOperations, change func(*personalStorageSnapshot)) {
	t.Helper()
	s := o.personal
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.storage != nil {
		s.storage.mu.Lock()
		defer s.storage.mu.Unlock()
		snapshot := copyPersonalSnapshot(s.storage.confirmed)
		change(&snapshot)
		s.storage.confirmed = snapshot
		return
	}
	snapshot := personalStorageSnapshot{}
	for login, c := range s.collections {
		snapshot.Collections = append(snapshot.Collections, durableRecord(login, "", "7", c.document.Entries))
	}
	change(&snapshot)
	s.collections = map[string]personalCollection{}
	for _, record := range snapshot.Collections {
		s.collections[record.Login] = personalCollection{revision: 7, document: copyPersonalDocument(record.Document)}
	}
}

func TestSharedApplicationOwnFormsPrecedencePrivacyAndNoPersistence(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, authored := range []string{"", "generic", "portainer"} {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+authored, func(t *testing.T) {
				o, r, ref, _ := sharedApplicationFixture(t, durable)
				sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
					for i := range s.Collections {
						if s.Collections[i].Login == "alice@example.com" {
							s.Collections[i].Document.Entries[0].Icon = authored
						}
					}
				})
				before := sharedApplicationState(t, o)
				var disk []byte
				var version personalStorageVersion
				var snapshot personalStorageSnapshot
				if durable {
					var err error
					disk, err = os.ReadFile(o.personal.config.File)
					if err != nil {
						t.Fatal(err)
					}
					snapshot, version, err = o.personal.storage.Read()
					if err != nil {
						t.Fatal(err)
					}
				} else if o.personal.config.File != "" || o.personal.config.Namespace != "" || o.personal.storage != nil {
					t.Fatal("RAM gained private file dependency")
				}
				originalView := o.editor.view.Load()
				body := &mcpGuardBody{}
				r.Body = body
				form := sharedTargetResult(t, o, r, ref, nil)
				want := "grafana"
				if authored != "" {
					want = authored
				}
				if form.Name != "Own application" || form.SharedIcon != "grafana" || form.EffectiveIcon != want || form.Target != ref || len(form.TargetRevision) != 43 {
					t.Fatal("wrong own form", form)
				}
				encoded, _ := json.Marshal(form)
				var fields map[string]json.RawMessage
				_ = json.Unmarshal(encoded, &fields)
				if len(fields) != 6 {
					t.Fatal("extra response fields")
				}
				_, digest := sharedApplicationReference(t, o, "alice@example.com")
				for _, secret := range []string{before.Document.Entries[0].ID, digest, "private-path", "node-1", "server.tailnet.ts.net", "alice-id", "bob-id", "Other authored name", "alice@example.com", "bob@example.com", "collections", "total", "store_id", "generation"} {
					if bytes.Contains(encoded, []byte(secret)) {
						t.Fatal("private evidence leaked", secret)
					}
				}
				if body.reads != 0 || body.closes != 0 {
					t.Fatal("body touched")
				}
				sharedNPMUnchanged(t, o.editor, originalView, originalView.bytes)
				// Changing just the shared default must change its target CAS, never authored
				// fields/private revisions/API records, even when explicit generic masks it.
				doc := cloneServiceMetadataDocument(originalView.metadata.document)
				doc.Applications[0].Icon = "jellyfin"
				sharedTargetReplaceView(t, o, doc)
				after := sharedTargetResult(t, o, r, ref, nil)
				want = "jellyfin"
				if authored != "" {
					want = authored
				}
				if after.SharedIcon != "jellyfin" || after.EffectiveIcon != want || after.TargetRevision == form.TargetRevision {
					t.Fatal("default precedence/revision")
				}
				if !reflect.DeepEqual(before, sharedApplicationState(t, o)) {
					t.Fatal("selected get/default materialized private state")
				}
				api := personalHTTPRead(t, durableTestHandler(o.personal, o.cache, o.now))
				if !reflect.DeepEqual(api.PersonalServicesState, before) {
					t.Fatal("API authored state changed")
				}
				if durable {
					afterDisk, err := os.ReadFile(o.personal.config.File)
					if err != nil || !bytes.Equal(disk, afterDisk) {
						t.Fatal("private disk written")
					}
					afterSnapshot, afterVersion, err := o.personal.storage.Read()
					if err != nil || version != afterVersion || !reflect.DeepEqual(snapshot, afterSnapshot) {
						t.Fatal("confirmed private snapshot changed")
					}
				}
				doc.Applications = []applicationIconEntry{}
				sharedTargetReplaceView(t, o, doc)
				fallback := sharedTargetResult(t, o, r, ref, nil)
				want = "generic"
				if authored != "" {
					want = authored
				}
				if fallback.SharedIcon != "" || fallback.EffectiveIcon != want {
					t.Fatal("generic fallback")
				}
			})
		}
	}
}

func TestSharedApplicationTwoOwnersSameDefaultCallerBound(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "ram", true: "durable"}[durable], func(t *testing.T) {
			o, r, a, _ := sharedApplicationFixture(t, durable)
			b, bd := sharedApplicationReference(t, o, "bob@example.com")
			_, ad := sharedApplicationReference(t, o, "alice@example.com")
			bob := sharedTargetRequest(t, o, "bob@example.com")
			af, bf := sharedTargetResult(t, o, r, a, nil), sharedTargetResult(t, o, bob, b, nil)
			if ad != bd || a == b || af.TargetRevision == bf.TargetRevision || af.SharedIcon != bf.SharedIcon || bf.Name != "Other authored name" {
				t.Fatal("shared destination/caller isolation")
			}
			sharedTargetResult(t, o, r, b, errMetadataNotVisible)
			sharedTargetResult(t, o, bob, a, errMetadataNotVisible)
			fields := []string{"alice@example.com", o.binding.Namespace, "tailscale", "application", personalTestEntry(0).ID, ad}
			raw, _ := json.Marshal(fields)
			mac := hmac.New(sha256.New, o.editor.key[:])
			mac.Write([]byte("shared-target-handle-v1\x00"))
			mac.Write(raw)
			if a.Handle != base64.RawURLEncoding.EncodeToString(mac.Sum(nil)) {
				t.Fatal("application handle domain changed")
			}
			// Foreign activity does not affect own target revision or disclose a count.
			sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
				for i := range s.Collections {
					if s.Collections[i].Login == "bob@example.com" {
						s.Collections[i].Revision = "91"
						s.Collections[i].Document.Entries = append(s.Collections[i].Document.Entries, personalTestEntry(9))
					}
				}
			})
			if sharedTargetResult(t, o, r, a, nil) != af {
				t.Fatal("foreign collection influenced own form")
			}
		})
	}
}

func TestSharedApplicationMissingInactiveAndOwnerFailures(t *testing.T) {
	changes := []string{"foreign", "absent", "empty", "missing-entry", "removed-owner", "shared-owner", "invalid-owner", "renamed-owner", "no-users", "no-roles", "no-node", "renamed-node", "ambiguous-host", "no-grant", "wrong-port", "udp", "wrong-human", "wrong-destination", "no-typed-id", "bad-kind", "bad-value", "id-provider", "cold", "future", "stale", "provider", "no-policy", "no-personal", "closed-personal", "empty-origin", "wrong-origin", "noncanonical-origin", "wrong-host", "namespace", "private-uncertain", "private-closed", "owner-reassigned"}
	for _, durable := range []bool{false, true} {
		for _, change := range changes {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+change, func(t *testing.T) {
				o, r, ref, at := sharedApplicationFixture(t, durable)
				s := o.personal
				d := o.cache.Get()
				want := errMetadataNotVisible
				switch change {
				case "foreign":
					ref, _ = sharedApplicationReference(t, o, "bob@example.com")
				case "absent", "empty", "missing-entry":
					sharedApplicationChange(t, o, func(p *personalStorageSnapshot) {
						for i, c := range p.Collections {
							if c.Login == "alice@example.com" {
								if change == "absent" {
									p.Collections = append(p.Collections[:i], p.Collections[i+1:]...)
								} else if change == "empty" {
									p.Collections[i].Document.Entries = []PersonalService{}
								} else {
									p.Collections[i].Document.Entries[0].ID = personalTestEntry(10).ID
								}
								break
							}
						}
					})
				case "removed-owner":
					delete(d.TailscaleUsersByLogin, "alice@example.com")
				case "shared-owner":
					d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
				case "invalid-owner":
					d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: " padded", DirectMember: true}
				case "renamed-owner":
					delete(d.TailscaleUsersByLogin, "alice@example.com")
					d.TailscaleUsersByLogin["new@example.com"] = TailscaleUserIdentity{ID: "alice-id", DirectMember: true}
					d.GrantRoleSelectorsByLogin["new@example.com"] = []string{"autogroup:member"}
					o.editor.config.Logins["new@example.com"] = true
					r = sharedTargetRequest(t, o, "new@example.com")
				case "no-users":
					d.TailscaleUsersByLogin = nil
				case "no-roles":
					d.GrantRoleSelectorsByLogin = nil
				case "no-node":
					d.Nodes = nil
				case "renamed-node":
					d.Nodes[0].Name = "renamed.tailnet.ts.net"
				case "ambiguous-host":
					n := d.Nodes[0]
					n.ID = "duplicate"
					d.Nodes = append(d.Nodes, n)
				case "no-grant":
					d.Policy.Grants = nil
					d.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"*"}}}
				case "wrong-port":
					d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:22")}
				case "udp":
					d.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "udp:443")}
				case "wrong-human":
					d.Policy.Grants[0].BrowserSrc = []string{"bob@example.com"}
				case "wrong-destination":
					d.Policy.Grants[0].Dst = []string{"100.64.0.99"}
				case "no-typed-id":
					d.PresentationDeviceIDsByNodeID = nil
				case "bad-kind", "bad-value", "id-provider":
					id := d.PresentationDeviceIDsByNodeID["node-1"]
					if change == "bad-kind" {
						id.Kind = "ID"
					} else if change == "bad-value" {
						id.Value = " padded "
					} else {
						id.Provider = controlPlaneHeadscale
					}
					d.PresentationDeviceIDsByNodeID["node-1"] = id
				case "cold":
					d.UpdatedAt = time.Time{}
					want = errMetadataStale
				case "future":
					d.UpdatedAt = at.Add(time.Nanosecond)
					want = errMetadataStale
				case "stale":
					d.UpdatedAt = at.Add(-90*time.Second - time.Nanosecond)
					want = errMetadataStale
				case "provider":
					d.ControlPlane.Provider = controlPlaneHeadscale
					want = errMetadataStale
				case "no-policy":
					d.Policy = nil
					want = errMetadataStale
				case "no-personal":
					o.personal = nil
					defer func() { o.personal = s }()
				case "closed-personal":
					s.closed = true
				case "empty-origin":
					s.config.Origin = ""
				case "wrong-origin":
					s.config.Origin = "https://portal.example:8081"
				case "noncanonical-origin":
					s.config.Origin += "/"
				case "wrong-host":
					s.config.Host = "wrong.example"
				case "namespace":
					s.config.Namespace = "foreign"
				case "private-uncertain", "private-closed":
					if durable {
						s.storage.mu.Lock()
						if change == "private-uncertain" {
							s.storage.uncertain = true
							want = errMetadataUncertain
						} else {
							s.storage.closed = true
							defer func() { s.storage.closed = false }()
						}
						s.storage.mu.Unlock()
					} else {
						s.config.File = "/must-not-open/absent.json"
					}
				case "owner-reassigned":
					before := sharedTargetResult(t, o, r, ref, nil)
					d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-id", DirectMember: true}
					if !durable {
						// RAM has no historical owner field by contract. Current authority is still
						// mandatory, and a form for the previous owner must conflict, not be adopted.
						s.mu.Lock()
						o.editor.mu.Lock()
						_, err := o.selectedMCPTargetLocked(r, ref, "alice@example.com", before.TargetRevision)
						o.editor.mu.Unlock()
						s.mu.Unlock()
						if !errors.Is(err, errMetadataConflict) {
							t.Fatal("RAM old-owner CAS adopted", err)
						}
						return
					}
				}
				sharedTargetResult(t, o, r, ref, want)
			})
		}
	}
}

func TestSharedApplicationRevisionsAndDestinationChanges(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, change := range []string{"fresh-poll", "unrelated", "name", "icon", "generation", "collection-revision", "store-id", "replacement", "id-kind", "path", "escaped-path", "port", "root-equivalence", "root-path", "rename-and-own-url"} {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+change, func(t *testing.T) {
				o, r, ref, at := sharedApplicationFixture(t, durable)
				if change == "root-equivalence" {
					sharedApplicationChange(t, o, func(p *personalStorageSnapshot) {
						for i := range p.Collections {
							p.Collections[i].Document.Entries[0].URL = "https://server.tailnet.ts.net/"
						}
					})
					ref, _ = sharedApplicationReference(t, o, "alice@example.com")
				}
				before := sharedTargetResult(t, o, r, ref, nil)
				_, oldDigest := sharedApplicationReference(t, o, "alice@example.com")
				relevant := true
				newHandle := false
				d := o.cache.Get()
				switch change {
				case "fresh-poll":
					d.UpdatedAt = at.Add(-time.Second)
					relevant = false
				case "unrelated":
					d.Nodes = append(d.Nodes, Node{ID: "unrelated", Name: "other.tailnet.ts.net"})
					relevant = false
				case "generation":
					o.personal.generation = strings.Repeat("b", 32)
				case "collection-revision":
					if durable {
						o.personal.storage.confirmed.Collections[0].Revision = "8"
					} else {
						c := o.personal.collections["alice@example.com"]
						c.revision++
						o.personal.collections["alice@example.com"] = c
					}
				case "store-id":
					if durable {
						o.personal.storage.confirmed.StoreID = strings.Repeat("b", 32)
					} else {
						relevant = false
					}
				case "replacement":
					d.Nodes[0].ID = "replacement"
					d.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{"replacement": {Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: "replacement"}}
					newHandle = true
				case "id-kind":
					key := d.PresentationDeviceIDsByNodeID["node-1"]
					key.Kind = deviceIDKindNodeID
					d.PresentationDeviceIDsByNodeID["node-1"] = key
					newHandle = true
				default:
					sharedApplicationChange(t, o, func(p *personalStorageSnapshot) {
						for i := range p.Collections {
							if p.Collections[i].Login != "alice@example.com" {
								continue
							}
							e := &p.Collections[i].Document.Entries[0]
							switch change {
							case "name":
								e.Name = "New own name"
							case "icon":
								e.Icon = "generic"
							case "path":
								e.URL = "https://server.tailnet.ts.net/different"
								newHandle = true
							case "escaped-path":
								e.URL = "https://server.tailnet.ts.net/%E7%95%8C"
								newHandle = true
							case "port":
								e.URL = "https://server.tailnet.ts.net:8443/private-path"
								newHandle = true
							case "root-equivalence":
								e.URL = "https://server.tailnet.ts.net"
							case "root-path":
								e.URL = "https://server.tailnet.ts.net/"
								newHandle = true
							case "rename-and-own-url":
								d.Nodes[0].Name = "renamed.tailnet.ts.net"
								e.URL = "https://renamed.tailnet.ts.net/private-path"
							}
						}
					})
				}
				next, digest := sharedApplicationReference(t, o, "alice@example.com")
				if newHandle {
					sharedTargetResult(t, o, r, ref, errMetadataNotVisible)
					if digest == oldDigest || next == ref {
						t.Fatal("destination not bound")
					}
				} else if next != ref || digest != oldDigest {
					t.Fatal("non-destination changed handle")
				}
				after := sharedTargetResult(t, o, r, next, nil)
				if (after.TargetRevision != before.TargetRevision) != relevant {
					t.Fatal("wrong target revision relevance")
				}
				if relevant && !newHandle {
					o.personal.mu.Lock()
					o.editor.mu.Lock()
					_, err := o.selectedMCPTargetLocked(r, ref, "alice@example.com", before.TargetRevision)
					o.editor.mu.Unlock()
					o.personal.mu.Unlock()
					if !errors.Is(err, errMetadataConflict) {
						t.Fatal("changed CAS accepted", err)
					}
				}
			})
		}
	}
}

func TestSharedApplicationLockedReadPublicParityAndVolatileCompatibility(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, change := range []string{"normal", "absent", "empty", "invalid-login", "cold", "shared", "reassigned", "uncertain", "nil-clock", "closed"} {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+change, func(t *testing.T) {
				o, _, _, _ := sharedApplicationFixture(t, durable)
				s := o.personal
				login := "alice@example.com"
				data := o.cache.Get()
				clock := o.now
				switch change {
				case "absent":
					login = "absent@example.com"
					data.TailscaleUsersByLogin[login] = TailscaleUserIdentity{ID: "absent-id", DirectMember: true}
				case "empty":
					sharedApplicationChange(t, o, func(p *personalStorageSnapshot) {
						for i := range p.Collections {
							p.Collections[i].Document.Entries = []PersonalService{}
						}
					})
				case "invalid-login":
					login = " alice@example.com "
				case "cold":
					data = nil
				case "shared":
					data.TailscaleUsersByLogin[login] = TailscaleUserIdentity{ID: "alice-id"}
				case "reassigned":
					data.TailscaleUsersByLogin[login] = TailscaleUserIdentity{ID: "new-id", DirectMember: true}
				case "uncertain":
					if durable {
						s.storage.uncertain = true
					}
				case "nil-clock":
					clock = nil
				case "closed":
					s.closed = true
				}
				public, pe := s.ReadForSnapshot(login, data, o.interval, clock)
				s.mu.Lock()
				locked, le := s.readForSnapshotLocked(login, data, o.interval, clock)
				s.mu.Unlock()
				if !reflect.DeepEqual(public, locked) || !errors.Is(pe, le) {
					t.Fatal("public/locked read drift", pe, le)
				}
				if !durable && (change == "cold" || change == "shared" || change == "reassigned") && (pe != nil || !public.Initialized || len(public.Document.Entries) != 1) {
					t.Fatal("ordinary volatile semantics narrowed")
				}
				if pe == nil && len(public.Document.Entries) > 0 {
					public.Document.Entries[0].Name = "detached"
					locked.Document.Entries[0].Icon = "generic"
					again, err := s.ReadForSnapshot(login, data, o.interval, clock)
					if err != nil || again.Document.Entries[0].Name == "detached" || again.Document.Entries[0].Icon != "" {
						t.Fatal("read leaked mutable authored alias")
					}
				}
				if pe != nil && (public.Document.Entries != nil || locked.Document.Entries != nil) {
					t.Fatal("failed read leaked content")
				}
			})
		}
	}
}

func TestSharedApplicationPostDiskAuthorityBeforeConflict(t *testing.T) {
	changes := []string{"cancel", "raw-login", "both-logins", "allowlist", "method", "host", "origin", "fetch", "mcp-origin", "shared-origin", "personal-origin", "personal-host", "mode", "binding", "writer", "owner-removed", "owner-shared", "owner-reassigned", "renamed-owner", "access", "typed-id", "clock", "snapshot", "private-uncertain", "generation", "collection-revision", "authored", "disk", "disk-and-access"}
	for _, durable := range []bool{false, true} {
		for _, change := range changes {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+change, func(t *testing.T) {
				o, r, ref, at := sharedApplicationFixture(t, durable)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				trigger := 4
				if durable {
					trigger = 5
				}
				calls := 0
				want := errMetadataForbidden
				if change == "disk" || change == "disk-and-access" {
					if err := os.WriteFile(o.editor.path, []byte("external changed bytes"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				o.now = func() time.Time {
					calls++
					if calls == trigger {
						d := o.cache.Get()
						switch change {
						case "cancel":
							cancel()
						case "raw-login":
							r.Header.Set("Tailscale-User-Login", "bob@example.com")
						case "both-logins":
							r.Header.Set("Tailscale-User-Login", "bob@example.com")
							IdentityFromContext(r.Context()).Login = "bob@example.com"
						case "allowlist":
							delete(o.editor.config.Logins, "alice@example.com")
						case "method":
							r.Method = http.MethodGet
						case "host":
							r.Host = "foreign.example"
						case "origin":
							r.Header.Set("Origin", "http://foreign.example")
						case "fetch":
							r.Header.Set("Sec-Fetch-Site", "same-origin")
						case "mcp-origin":
							o.mcp.Origin = "http://foreign.example"
						case "shared-origin":
							o.editor.config.Origin = "http://foreign.example"
						case "personal-origin":
							o.personal.config.Origin = "http://foreign.example"
							want = errMetadataNotVisible
						case "personal-host":
							o.personal.config.Host = "foreign.example"
							want = errMetadataNotVisible
						case "mode":
							o.mode = serviceEditOff
						case "binding":
							o.editor.config.Binding.Namespace = "foreign"
							want = errMetadataStale
						case "writer":
							o.editor.disabled = true
							want = errMetadataUncertain
						case "owner-removed", "renamed-owner":
							delete(d.TailscaleUsersByLogin, "alice@example.com")
							if change == "renamed-owner" {
								d.TailscaleUsersByLogin["new@example.com"] = TailscaleUserIdentity{ID: "alice-id", DirectMember: true}
							}
							want = errMetadataNotVisible
						case "owner-shared":
							d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
							want = errMetadataNotVisible
						case "owner-reassigned":
							d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-id", DirectMember: true}
							want = errMetadataNotVisible
							if !durable {
								want = errMetadataConflict
							}
						case "access", "disk-and-access":
							d.Policy.Grants = nil
							want = errMetadataNotVisible
						case "typed-id":
							d.PresentationDeviceIDsByNodeID = nil
							want = errMetadataNotVisible
						case "clock":
							want = errMetadataStale
							return at.Add(91 * time.Second)
						case "snapshot":
							copy := *d
							copy.UpdatedAt = at.Add(-91 * time.Second)
							o.cache.data.Store(&copy)
							want = errMetadataStale
						case "private-uncertain":
							if durable {
								o.personal.storage.uncertain = true
								want = errMetadataUncertain
							} else {
								o.personal.closed = true
								want = errMetadataNotVisible
							}
						case "generation":
							o.personal.generation = strings.Repeat("b", 32)
							want = errMetadataConflict
						case "collection-revision", "authored":
							if durable {
								if change == "authored" {
									o.personal.storage.confirmed.Collections[0].Document.Entries[0].Name = "changed"
								} else {
									o.personal.storage.confirmed.Collections[0].Revision = "8"
								}
							} else {
								c := o.personal.collections["alice@example.com"]
								if change == "authored" {
									c.document = copyPersonalDocument(c.document)
									c.document.Entries[0].Name = "changed"
								} else {
									c.revision++
								}
								o.personal.collections["alice@example.com"] = c
							}
							want = errMetadataConflict
						case "disk":
							want = errMetadataConflict
						}
					}
					return at
				}
				form, err := o.prepareMCPTarget(r, ref)
				if calls < trigger || !errors.Is(err, want) || form != (sharedTargetForm{}) {
					t.Fatalf("postdisk calls=%d want=%v got=%v zero=%v", calls, want, err, form == (sharedTargetForm{}))
				}
			})
		}
	}
}

func TestSharedApplicationPersonalThenEditorWaitFreshness(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, wait := range []string{"personal", "editor"} {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+wait, func(t *testing.T) {
				o, r, ref, at := sharedApplicationFixture(t, durable)
				entered := &sharedTargetSignalContext{Context: r.Context(), entered: make(chan struct{})}
				r = r.WithContext(entered)
				var clockMu sync.Mutex
				clock := at
				calls := 0
				personalHeld := make(chan struct{})
				o.now = func() time.Time {
					clockMu.Lock()
					defer clockMu.Unlock()
					calls++
					if calls == 1 {
						close(personalHeld)
					}
					return clock
				}
				if wait == "personal" {
					o.personal.mu.Lock()
				} else {
					o.editor.mu.Lock()
				}
				done := make(chan error, 1)
				go func() {
					form, err := o.prepareMCPTarget(r, ref)
					if form != (sharedTargetForm{}) {
						done <- errors.New("leaked stale form")
					} else {
						done <- err
					}
				}()
				if wait == "personal" {
					<-entered.entered
					if !o.editor.mu.TryLock() {
						o.personal.mu.Unlock()
						t.Fatal("editor acquired before personal")
					}
					o.editor.mu.Unlock()
				} else {
					<-personalHeld
					if o.personal.mu.TryLock() {
						o.personal.mu.Unlock()
						o.editor.mu.Unlock()
						t.Fatal("personal not pinned before editor wait")
					}
				}
				clockMu.Lock()
				clock = at.Add(91 * time.Second)
				clockMu.Unlock()
				if wait == "personal" {
					o.personal.mu.Unlock()
				} else {
					o.editor.mu.Unlock()
				}
				select {
				case err := <-done:
					if !errors.Is(err, errMetadataStale) {
						t.Fatal("pre-wait clock reused", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("lock choreography deadlock")
				}
				clockMu.Lock()
				clock = at.Add(90 * time.Second)
				clockMu.Unlock()
				sharedTargetResult(t, o, r, ref, nil)
				clockMu.Lock()
				clock = at.Add(90*time.Second + time.Nanosecond)
				clockMu.Unlock()
				sharedTargetResult(t, o, r, ref, errMetadataStale)
			})
		}
	}
}

func TestSharedApplicationAdmissionAfterEachWaitAndOwnedRead(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, stage := range []string{"personal-wait", "editor-wait", "owned-read", "final-owned-read"} {
			for _, change := range []string{"cancel", "login", "allowlist", "personal-origin", "owner", "clock"} {
				t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+stage+"/"+change, func(t *testing.T) {
					o, r, ref, at := sharedApplicationFixture(t, durable)
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					r = r.WithContext(ctx)
					trigger := map[string]int{"personal-wait": 1, "editor-wait": 2, "owned-read": 3, "final-owned-read": 5}[stage]
					if durable && stage == "owned-read" {
						trigger = 4
					}
					if durable && stage == "final-owned-read" {
						trigger = 7
					}
					calls := 0
					want := errMetadataForbidden
					o.now = func() time.Time {
						calls++
						if calls == trigger {
							switch change {
							case "cancel":
								cancel()
							case "login":
								r.Header.Set("Tailscale-User-Login", "bob@example.com")
							case "allowlist":
								delete(o.editor.config.Logins, "alice@example.com")
							case "personal-origin":
								o.personal.config.Origin = "http://foreign.example"
								want = errMetadataNotVisible
							case "owner":
								delete(o.cache.Get().TailscaleUsersByLogin, "alice@example.com")
								want = errMetadataNotVisible
							case "clock":
								want = errMetadataStale
								return at.Add(91 * time.Second)
							}
						}
						return at
					}
					form, err := o.prepareMCPTarget(r, ref)
					if calls < trigger || form != (sharedTargetForm{}) || !errors.Is(err, want) {
						t.Fatalf("checkpoint %d calls %d: got %v want %v", trigger, calls, err, want)
					}
				})
			}
		}
	}
}

func TestSharedApplicationFreshnessAfterTransientStorageWait(t *testing.T) {
	o, r, ref, at := sharedApplicationFixture(t, true)
	var mu sync.Mutex
	clock := at
	calls := 0
	reached := make(chan struct{})
	o.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls == 2 {
			close(reached)
		}
		return clock
	}
	o.personal.storage.mu.Lock()
	done := make(chan error, 1)
	go func() {
		form, err := o.prepareMCPTarget(r, ref)
		if form != (sharedTargetForm{}) {
			done <- errors.New("stale private form")
		} else {
			done <- err
		}
	}()
	<-reached
	mu.Lock()
	clock = at.Add(91 * time.Second)
	mu.Unlock()
	o.personal.storage.mu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, errMetadataStale) {
			t.Fatal("private wait freshness", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("transient private lock deadlock")
	}
}

func TestSharedApplicationReplacementSnapshotAfterFinalOwnedRead(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, change := range []string{"identical", "owner-removed", "owner-reassigned", "grant-revoked", "device-replaced", "stale", "provider"} {
			t.Run(map[bool]string{false: "ram", true: "durable"}[durable]+"/"+change, func(t *testing.T) {
				o, r, ref, at := sharedApplicationFixture(t, durable)
				before := sharedTargetResult(t, o, r, ref, nil)
				original := o.cache.Get()
				policyBefore, err := json.Marshal(original.Policy)
				if err != nil {
					t.Fatal(err)
				}
				next := *original
				policy := *original.Policy
				policy.Grants = append([]GrantRule(nil), original.Policy.Grants...)
				next.Policy = &policy
				next.TailscaleUsersByLogin = make(map[string]TailscaleUserIdentity)
				for login, owner := range original.TailscaleUsersByLogin {
					next.TailscaleUsersByLogin[login] = owner
				}
				next.PresentationDeviceIDsByNodeID = make(map[string]ProviderDeviceID)
				for id, key := range original.PresentationDeviceIDsByNodeID {
					next.PresentationDeviceIDsByNodeID[id] = key
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
				// Publish B at the post-read clock checkpoint in the SECOND selection,
				// after disk inspection. A stays fresh with independent owner/policy
				// objects; mutating A's maps would not reproduce replacement publication.
				trigger := 5
				if durable {
					trigger = 7
				}
				calls := 0
				o.now = func() time.Time {
					calls++
					if calls == trigger {
						o.cache.data.Store(&next)
					}
					return at
				}
				form, err := o.prepareMCPTarget(r, ref)
				if calls != trigger || o.cache.Get() != &next {
					t.Fatalf("final replacement checkpoint not exercised: %d", calls)
				}
				if !errors.Is(err, want) {
					t.Fatalf("replacement result %v; want %v", err, want)
				}
				if want != nil && form != (sharedTargetForm{}) {
					t.Fatal("revoked replacement returned private form")
				}
				if want == nil && form != before {
					t.Fatal("equivalent replacement changed selected form")
				}
				policyAfter, err := json.Marshal(original.Policy)
				if err != nil || !bytes.Equal(policyBefore, policyAfter) || original.TailscaleUsersByLogin["alice@example.com"].ID != "alice-id" || original.PresentationDeviceIDsByNodeID["node-1"].Value != "node-1" || original.UpdatedAt != at || original.ControlPlane.Provider != controlPlaneTailscale {
					t.Fatal("test changed old authority instead of replacing it")
				}
			})
		}
	}
}

func TestSharedApplicationVolatileOrdinaryManagementAndRenderingUnchanged(t *testing.T) {
	o, r, ref, at := sharedApplicationFixture(t, false)
	delete(o.cache.Get().TailscaleUsersByLogin, "alice@example.com")
	state := sharedApplicationState(t, o)
	cards, _ := ProjectPersonalServices(&Identity{Login: "alice@example.com"}, o.cache.Get(), state, at, o.interval)
	if len(cards) != 1 {
		t.Fatal("ordinary RAM rendering gained owner-ID requirement")
	}
	api := personalHTTPRead(t, durableTestHandler(o.personal, o.cache, o.now))
	if !reflect.DeepEqual(api.PersonalServicesState, state) {
		t.Fatal("ordinary RAM API changed")
	}
	sharedTargetResult(t, o, r, ref, errMetadataNotVisible)
	mutate := PersonalServicesMutation{Generation: state.Generation, Revision: state.Revision, Action: "update", ID: state.Document.Entries[0].ID, Name: "Still manageable", URL: state.Document.Entries[0].URL}
	if _, err := o.personal.Mutate("alice@example.com", mutate); err != nil {
		t.Fatal("ordinary RAM mutation gained owner-ID requirement", err)
	}
}

func TestSharedApplicationConcurrentPersonalEditPinnedAndCAS(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "ram", true: "durable"}[durable], func(t *testing.T) {
			o, r, ref, at := sharedApplicationFixture(t, durable)
			before := sharedTargetResult(t, o, r, ref, nil)
			state := sharedApplicationState(t, o)
			mutation := PersonalServicesMutation{StoreID: state.StoreID, Generation: state.Generation, Revision: state.Revision, CSRF: o.personal.csrf("alice@example.com", at), Action: "update", ID: state.Document.Entries[0].ID, Name: "Edited after read", URL: state.Document.Entries[0].URL}
			raw := personalHTTPRequest(http.MethodPost, nil)
			var request *http.Request
			IdentityMiddleware(o.trusted, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { request = r })).ServeHTTP(httptest.NewRecorder(), raw)
			if request == nil {
				t.Fatal("mutation admission fixture")
			}
			reached, release := make(chan struct{}), make(chan struct{})
			trigger := 4
			if durable {
				trigger = 5
			}
			calls := 0
			o.now = func() time.Time {
				calls++
				if calls == trigger {
					close(reached)
					<-release
				}
				return at
			}
			type result struct {
				form sharedTargetForm
				err  error
			}
			got := make(chan result, 1)
			go func() { f, e := o.prepareMCPTarget(r, ref); got <- result{f, e} }()
			<-reached
			if o.personal.mu.TryLock() {
				o.personal.mu.Unlock()
				close(release)
				t.Fatal("personal entry not pinned through disk/recheck")
			}
			editStarted, edited := make(chan struct{}), make(chan error, 1)
			go func() {
				close(editStarted)
				_, _, err := o.personal.MutateBrowser(request, mutation, o.cache, o.trusted, o.interval, func() time.Time { return at })
				edited <- err
			}()
			<-editStarted
			select {
			case err := <-edited:
				close(release)
				t.Fatal("personal edit escaped lock", err)
			default:
			}
			close(release)
			select {
			case result := <-got:
				if result.err != nil || result.form != before {
					t.Fatal("pinned selected read changed", result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("selected get deadlocked")
			}
			select {
			case err := <-edited:
				if err != nil {
					t.Fatal("personal edit failed", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("personal lock not released")
			}
			o.now = func() time.Time { return at }
			after := sharedTargetResult(t, o, r, ref, nil)
			if after.Name != "Edited after read" || after.TargetRevision == before.TargetRevision {
				t.Fatal("authored edit did not invalidate target CAS")
			}
			o.personal.mu.Lock()
			o.editor.mu.Lock()
			_, err := o.selectedMCPTargetLocked(r, ref, "alice@example.com", before.TargetRevision)
			o.editor.mu.Unlock()
			o.personal.mu.Unlock()
			if !errors.Is(err, errMetadataConflict) {
				t.Fatal("old target CAS accepted", err)
			}
		})
	}
}
