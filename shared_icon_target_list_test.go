package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func sharedListFixture(t *testing.T, kind sharedTargetKind, n int, durable bool) (*SharedIconOperations, *http.Request, time.Time) {
	t.Helper()
	var o *SharedIconOperations
	var r *http.Request
	var at time.Time
	if kind == sharedTargetApplication {
		o, r, _, at = sharedApplicationFixture(t, durable)
		sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
			for i := range s.Collections {
				if s.Collections[i].Login == "alice@example.com" {
					entries := make([]PersonalService, 0, n)
					for j := 0; j < n; j++ {
						e := personalTestEntry(j)
						e.Name = fmt.Sprintf("Application %03d", j)
						e.URL = fmt.Sprintf("https://server.tailnet.ts.net/private-%03d", j)
						entries = append(entries, e)
					}
					s.Collections[i].Document.Entries = entries
				}
			}
		})
	} else {
		o, r, _, at = sharedTargetFixture(t, kind)
		d := o.cache.Get()
		if kind == sharedTargetNPM {
			matches := evaluateServices(&Identity{Login: "alice@example.com"}, o.editor.requestData(d))
			if len(matches) == 0 {
				t.Fatal("NPM fixture")
			}
			base := matches[0].ProxyHost
			d.ProxyHosts = nil
			for i := 0; i < n; i++ {
				h := base
				h.ID = i + 100
				h.DomainNames = []string{fmt.Sprintf("service-%03d.example", i)}
				d.ProxyHosts = append(d.ProxyHosts, h)
			}
		} else {
			base := d.Nodes[0]
			d.Nodes = nil
			d.MachineSSHCapableByID = map[string]bool{}
			d.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{}
			for i := 0; i < n; i++ {
				node := base
				node.ID = fmt.Sprintf("internal-device-%03d", i)
				node.Name = fmt.Sprintf("machine-%03d.tailnet.ts.net", i)
				node.Addresses = []string{fmt.Sprintf("100.64.%d.%d", i/250, i%250+1)}
				d.Nodes = append(d.Nodes, node)
				d.MachineSSHCapableByID[node.ID] = true
				d.PresentationDeviceIDsByNodeID[node.ID] = ProviderDeviceID{Provider: controlPlaneTailscale, Kind: deviceIDKindID, Value: node.ID}
			}
		}
	}
	return o, r, at
}

func sharedListResult(t *testing.T, o *SharedIconOperations, r *http.Request, req sharedTargetListRequest, want error) sharedTargetPage {
	t.Helper()
	p, err := o.prepareMCPTargetList(r, req)
	if !errors.Is(err, want) {
		t.Fatalf("list got %v want %v", err, want)
	}
	if err != nil && (p.Targets != nil || p.NextCursor != "") {
		t.Fatal("error disclosed partial page")
	}
	return p
}

func TestSharedTargetListPagesAndSelectedParity(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		for _, n := range []int{0, 1, 64, 65, 128} {
			for _, limit := range []int{0, 1, 17, 64} {
				t.Run(fmt.Sprintf("%s/%d/%d", kind, n, limit), func(t *testing.T) {
					o, r, _ := sharedListFixture(t, kind, n, false)
					original := o.editor.view.Load()
					var before PersonalServicesState
					if kind == sharedTargetApplication {
						before = sharedApplicationState(t, o)
					}
					all := []sharedTargetSummary{}
					seen := map[string]bool{}
					cursor := ""
					pages := 0
					for {
						p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind, Limit: limit, Cursor: cursor}, nil)
						pages++
						wantLimit := limit
						if wantLimit == 0 {
							wantLimit = 64
						}
						if p.Targets == nil || len(p.Targets) > wantLimit || pages > n+1 {
							t.Fatal("page bounds")
						}
						if len(encodedSharedPage(t, p, json.RawMessage(`"`+strings.Repeat("<", 256)+`"`))) > 256*1024 {
							t.Fatal("transport bound")
						}
						for _, s := range p.Targets {
							if s.Target.Kind != kind || seen[s.Target.Handle] {
								t.Fatal("duplicate/cross-kind")
							}
							seen[s.Target.Handle] = true
							all = append(all, s)
						}
						if p.NextCursor == "" {
							break
						}
						if len(p.Targets) == 0 || len(p.NextCursor) > 256 {
							t.Fatal("invalid continuation")
						}
						cursor = p.NextCursor
					}
					if len(all) != n || !sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Target.Handle < all[j].Target.Handle }) {
						t.Fatalf("projection/sort %d want %d", len(all), n)
					}
					// Full selected-get evidence/summary/CAS remains the source of truth.
					for _, s := range all {
						f := sharedTargetResult(t, o, r, s.Target, nil)
						if f.sharedTargetSummary != s {
							t.Fatal("list/get drift")
						}
					}
					if kind == sharedTargetApplication && !reflect.DeepEqual(before, sharedApplicationState(t, o)) {
						t.Fatal("list changed personal state")
					}
					sharedNPMUnchanged(t, o.editor, original, original.bytes)
				})
			}
		}
	}
}

func TestSharedTargetListPublicGateAndLimits(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication, "", "NPM"} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, _, _ := sharedTargetFixture(t, sharedTargetNPM)
			o.personal = &PersonalServicesStore{}
			o.editor.config = nil
			o.now = func() time.Time { t.Error("misconfigured list sampled clock"); return time.Time{} }
			body := &mcpGuardBody{}
			r.Body = body
			o.editor.mu.Lock()
			o.personal.mu.Lock()
			p, err := o.ListMCPTargets(r, sharedTargetListRequest{Kind: kind, Cursor: strings.Repeat("x", 257), Limit: -1})
			o.personal.mu.Unlock()
			o.editor.mu.Unlock()
			if !errors.Is(err, errMetadataStale) || p.Targets != nil || p.NextCursor != "" || body.reads != 0 || body.closes != 0 {
				t.Fatal("public gate effects")
			}
		})
	}
	var absent *SharedIconOperations
	if _, err := absent.ListMCPTargets(nil, sharedTargetListRequest{}); !errors.Is(err, errMetadataStale) {
		t.Fatal("nil gate")
	}
	o, r, _ := sharedListFixture(t, sharedTargetNPM, 65, false)
	for _, req := range []sharedTargetListRequest{{}, {Kind: "NPM"}, {Kind: " npm"}, {Kind: sharedTargetNPM, Limit: -1}, {Kind: sharedTargetNPM, Limit: 65}} {
		sharedListResult(t, o, r, req, errMetadataInvalidFields)
	}
	for _, n := range []int{0, 1, 64} {
		p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Limit: n}, nil)
		want := n
		if n == 0 {
			want = 64
		}
		if len(p.Targets) != want {
			t.Fatal("limit")
		}
	}
}

func TestSharedTargetListCursorStrictnessAndBinding(t *testing.T) {
	o, r, _ := sharedListFixture(t, sharedTargetNPM, 3, false)
	p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Limit: 1}, nil)
	cursor := p.NextCursor
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) != 65 || raw[0] != 1 {
		t.Fatal("cursor layout")
	}
	if base64.RawURLEncoding.EncodeToString(raw[1:33]) != p.Targets[0].Target.Handle {
		t.Fatal("cursor anchor")
	}
	invalid := []string{" " + cursor, cursor + "=", cursor + "\n", strings.Repeat("x", 257), strings.Repeat("A", 87), "null", `{"version":1,"handle":"x","mac":"x"}`, cursor[:86] + "!"}
	// Canonical pad bits, version, tag and length are all checked.
	for _, index := range []int{0, 1, 33, 64} {
		b := append([]byte(nil), raw...)
		b[index] ^= 1
		invalid = append(invalid, base64.RawURLEncoding.EncodeToString(b))
	}
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := strings.IndexByte(alphabet, cursor[86])
	invalid = append(invalid, cursor[:86]+string(alphabet[last|1]))
	for i, c := range invalid {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Cursor: c}, errMetadataInvalidFields)
		})
	}
	for _, change := range []string{"user", "kind", "process", "namespace", "provider"} {
		t.Run(change, func(t *testing.T) {
			other, req, _ := sharedListFixture(t, sharedTargetNPM, 3, false)
			other.editor.key = o.editor.key
			kind := sharedTargetNPM
			switch change {
			case "user":
				other.editor.config.Logins["bob@example.com"] = true
				req = sharedTargetRequest(t, other, "bob@example.com")
			case "kind":
				kind = sharedTargetMachine
			case "process":
				other.editor.key[0] ^= 1
			case "namespace":
				other.binding.Namespace = "another-source"
				other.editor.config.Binding = other.binding
			case "provider":
				other.binding.ControlPlane = controlPlaneHeadscale
				other.editor.config.Binding = other.binding
			}
			sharedListResult(t, other, req, sharedTargetListRequest{Kind: kind, Cursor: cursor}, errMetadataInvalidFields)
		})
	}
	// A well-MACed missing opaque anchor reveals no reason.
	missing := o.sharedTargetCursor("alice@example.com", sharedTargetNPM, o.sharedTargetHandle("alice@example.com", sharedTargetNPM, "999999").Handle)
	sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Cursor: missing}, errMetadataConflict)
}

func TestSharedTargetListCurrentPagesStableOrderingAndChangingLimit(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, at := sharedListFixture(t, kind, 65, false)
			first := sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind, Limit: 1}, nil)
			before := sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind, Cursor: first.NextCursor}, nil)
			d := *o.cache.Get()
			d.UpdatedAt = at.Add(time.Second)
			o.now = func() time.Time { return at.Add(time.Second) }
			d.ProxyHosts = append([]ProxyHost(nil), d.ProxyHosts...)
			d.Nodes = append([]Node(nil), d.Nodes...)
			for i, j := 0, len(d.ProxyHosts)-1; i < j; i, j = i+1, j-1 {
				d.ProxyHosts[i], d.ProxyHosts[j] = d.ProxyHosts[j], d.ProxyHosts[i]
			}
			for i, j := 0, len(d.Nodes)-1; i < j; i, j = i+1, j-1 {
				d.Nodes[i], d.Nodes[j] = d.Nodes[j], d.Nodes[i]
			}
			o.cache.data.Store(&d)
			if kind == sharedTargetApplication {
				sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
					for i := range s.Collections {
						if s.Collections[i].Login == "alice@example.com" {
							es := s.Collections[i].Document.Entries
							for a, b := 0, len(es)-1; a < b; a, b = a+1, b-1 {
								es[a], es[b] = es[b], es[a]
							}
						} else {
							s.Collections[i].Revision = "99"
							s.Collections[i].Document.Entries[0].Name = "Foreign unseen change"
						}
					}
				})
			}
			after := sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind, Cursor: first.NextCursor, Limit: 64}, nil)
			if !reflect.DeepEqual(before, after) || len(after.Targets) != 64 || after.NextCursor != "" {
				t.Fatal("poll/order/foreign change or limit affected cursor")
			}
			// Remove just the cursor anchor from CURRENT evidence, not global maps.
			anchor := first.Targets[0].Target
			switch kind {
			case sharedTargetNPM:
				for i, h := range d.ProxyHosts {
					if o.sharedTargetHandle("alice@example.com", kind, fmt.Sprint(h.ID)) == anchor {
						d.ProxyHosts = append(d.ProxyHosts[:i], d.ProxyHosts[i+1:]...)
						break
					}
				}
			case sharedTargetMachine:
				for _, node := range d.Nodes {
					key := d.PresentationDeviceIDsByNodeID[node.ID]
					if o.sharedTargetHandle("alice@example.com", kind, string(key.Kind), key.Value) == anchor {
						delete(d.MachineSSHCapableByID, node.ID)
						break
					}
				}
			case sharedTargetApplication:
				sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
					for i := range s.Collections {
						if s.Collections[i].Login == "alice@example.com" {
							es := s.Collections[i].Document.Entries
							ev := newPersonalServiceEvaluator(&Identity{Login: "alice@example.com"}, &d, o.now(), o.interval)
							for j, e := range es {
								v, _ := ev.evaluate(e)
								digest, _ := personalApplicationDigest(o.binding, &d, v)
								if o.sharedTargetHandle("alice@example.com", kind, e.ID, digest) == anchor {
									s.Collections[i].Document.Entries = append(es[:j], es[j+1:]...)
									break
								}
							}
						}
					}
				})
			}
			sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind, Cursor: first.NextCursor}, errMetadataConflict)
		})
	}
}

func TestSharedTargetListPrivacyAndIndependentStores(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprint(durable), func(t *testing.T) {
			o, r, _ := sharedListFixture(t, sharedTargetApplication, 65, durable)
			before := sharedApplicationState(t, o)
			view := o.editor.view.Load()
			var disk []byte
			if durable {
				disk, _ = os.ReadFile(o.personal.config.File)
			}
			p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetApplication}, nil)
			b, _ := json.Marshal(p)
			for _, secret := range []string{"Other authored name", "bob@example.com", "alice@example.com", "alice-id", "bob-id", "private-", "ps_", "sha256:", "node-1", "100.64.", "server.tailnet.ts.net", "store_id", "generation", "revision", "total", "count", "offset", "forward_host"} {
				if bytes.Contains(b, []byte(secret)) {
					t.Fatal("private list leak", secret)
				}
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(b, &fields)
			if len(fields) != 2 {
				t.Fatal("unexpected page shape")
			}
			if !reflect.DeepEqual(before, sharedApplicationState(t, o)) {
				t.Fatal("private mutation")
			}
			sharedNPMUnchanged(t, o.editor, view, view.bytes)
			if durable {
				after, _ := os.ReadFile(o.personal.config.File)
				if !bytes.Equal(disk, after) {
					t.Fatal("private disk mutation")
				}
				o.personal.storage.uncertain = true
				sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetApplication}, errMetadataUncertain)
			}
			// Other kinds must not acquire either private mutex, even on uncertainty.
			o.personal.mu.Lock()
			if durable {
				o.personal.storage.mu.Lock()
			}
			for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
				done := make(chan error, 1)
				go func() { _, err := o.prepareMCPTargetList(r, sharedTargetListRequest{Kind: kind}); done <- err }()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("other kind acquired private lock")
				}
			}
			if durable {
				o.personal.storage.mu.Unlock()
			}
			o.personal.mu.Unlock()
		})
	}
}

func TestSharedTargetListEmptyAbsentAndEligibility(t *testing.T) {
	for _, change := range []string{"absent", "empty", "inactive", "owner-missing", "owner-shared", "owner-reassigned", "typed-missing", "typed-collision", "hostname-ambiguous", "private-config", "closed"} {
		for _, durable := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", change, durable), func(t *testing.T) {
				o, r, _ := sharedListFixture(t, sharedTargetApplication, 1, durable)
				want := error(nil)
				switch change {
				case "absent":
					sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
						kept := []personalStorageCollection{}
						for _, c := range s.Collections {
							if c.Login != "alice@example.com" {
								kept = append(kept, c)
							}
						}
						s.Collections = kept
					})
				case "empty":
					sharedApplicationChange(t, o, func(s *personalStorageSnapshot) {
						for i := range s.Collections {
							if s.Collections[i].Login == "alice@example.com" {
								s.Collections[i].Document.Entries = []PersonalService{}
							}
						}
					})
				case "inactive":
					o.cache.Get().Policy.Grants = nil
				case "owner-missing":
					delete(o.cache.Get().TailscaleUsersByLogin, "alice@example.com")
					want = errMetadataNotVisible
				case "owner-shared":
					o.cache.Get().TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
					want = errMetadataNotVisible
				case "owner-reassigned":
					o.cache.Get().TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-owner", DirectMember: true}
					if durable {
						want = errMetadataNotVisible
					}
				case "typed-missing":
					o.cache.Get().PresentationDeviceIDsByNodeID = nil
				case "typed-collision":
					o.cache.Get().PresentationDeviceIDsByNodeID["other"] = o.cache.Get().PresentationDeviceIDsByNodeID["node-1"]
				case "hostname-ambiguous":
					node := o.cache.Get().Nodes[0]
					node.ID = "other"
					o.cache.Get().Nodes = append(o.cache.Get().Nodes, node)
				case "private-config":
					o.personal.config.Origin = "http://other.example"
					want = errMetadataNotVisible
				case "closed":
					o.personal.closed = true
					want = errMetadataNotVisible
				}
				p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetApplication}, want)
				if want == nil && change != "owner-reassigned" && (len(p.Targets) != 0 || p.NextCursor != "") {
					t.Fatal("ineligible application listed")
				}
			})
		}
	}
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine} {
		for _, change := range []string{"duplicate", "invisible", "unrenderable", "typed"} {
			t.Run(string(kind)+"/"+change, func(t *testing.T) {
				o, r, _ := sharedListFixture(t, kind, 1, false)
				d := o.cache.Get()
				if kind == sharedTargetNPM {
					switch change {
					case "duplicate":
						d.ProxyHosts = append(d.ProxyHosts, d.ProxyHosts[0])
					case "invisible":
						d.ProxyHosts[0].Enabled = false
					case "unrenderable", "typed":
						d.ProxyHosts[0].DomainNames = nil
					}
				} else {
					switch change {
					case "duplicate":
						d.Nodes = append(d.Nodes, d.Nodes[0])
					case "invisible":
						d.Policy.Grants = nil
					case "unrenderable":
						d.MachineSSHCapableByID = nil
					case "typed":
						d.PresentationDeviceIDsByNodeID = nil
					}
				}
				p := sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind}, nil)
				if len(p.Targets) != 0 {
					t.Fatal("ambiguous/ineligible target")
				}
			})
		}
	}
}

func TestSharedTargetListAdmissionAndFinalRechecks(t *testing.T) {
	changes := []string{"source", "raw-login", "both-logins", "origin", "fetch", "host", "method", "path", "version", "content", "authorization", "cancel", "allowlist", "mode", "shared-origin", "mcp-origin", "binding", "disabled", "stale", "future", "provider", "nil-policy", "private-config"}
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		for _, stage := range []string{"entry", "after-lock", "post-disk", "final-assembly", "post-owned-read"} {
			if stage == "post-owned-read" && kind != sharedTargetApplication {
				continue
			}
			for _, change := range changes {
				if change == "private-config" && kind != sharedTargetApplication {
					continue
				}
				t.Run(string(kind)+"/"+stage+"/"+change, func(t *testing.T) {
					o, r, at := sharedListFixture(t, kind, 2, false)
					ctx, cancel := context.WithCancel(r.Context())
					defer cancel()
					r = r.WithContext(ctx)
					want := errMetadataForbidden
					mutate := func() {
						switch change {
						case "source":
							r.RemoteAddr = "192.0.2.2:1"
						case "raw-login":
							r.Header.Set("Tailscale-User-Login", "bob@example.com")
						case "both-logins":
							r.Header.Set("Tailscale-User-Login", "mallory@example.com")
							IdentityFromContext(r.Context()).Login = "mallory@example.com"
						case "origin":
							r.Header.Set("Origin", "http://other.example")
						case "fetch":
							r.Header.Set("Sec-Fetch-Site", "same-origin")
						case "host":
							r.Host = "other.example"
						case "method":
							r.Method = http.MethodGet
						case "path":
							r.URL.RawPath = "/m%63p"
						case "version":
							r.Header.Del("MCP-Protocol-Version")
						case "content":
							r.Header.Set("Content-Type", "text/plain")
						case "authorization":
							r.Header["Authorization"] = []string{""}
						case "cancel":
							cancel()
						case "allowlist":
							delete(o.editor.config.Logins, "alice@example.com")
						case "mode":
							o.mode = serviceEditOff
						case "shared-origin":
							o.editor.config.Origin = "http://other.example"
						case "mcp-origin":
							o.mcp.Origin = "http://other.example"
						case "binding":
							o.editor.config.Binding.Namespace = "other"
							want = errMetadataStale
						case "disabled":
							o.editor.disabled = true
							want = errMetadataUncertain
						case "stale":
							d := *o.cache.Get()
							d.UpdatedAt = at.Add(-91 * time.Second)
							o.cache.data.Store(&d)
							want = errMetadataStale
						case "future":
							d := *o.cache.Get()
							d.UpdatedAt = at.Add(time.Second)
							o.cache.data.Store(&d)
							want = errMetadataStale
						case "provider":
							d := *o.cache.Get()
							d.ControlPlane.Provider = controlPlaneHeadscale
							o.cache.data.Store(&d)
							want = errMetadataStale
						case "nil-policy":
							d := *o.cache.Get()
							d.Policy = nil
							o.cache.data.Store(&d)
							want = errMetadataStale
						case "private-config":
							o.personal.config.Host = "other.example"
							want = errMetadataNotVisible
						}
					}
					trigger := 1
					switch stage {
					case "post-disk":
						trigger = 2
					case "final-assembly":
						trigger = 3
					}
					if kind == sharedTargetApplication {
						switch stage {
						case "after-lock":
							trigger = 2
						case "post-disk":
							trigger = 4
						case "final-assembly":
							trigger = 6
						case "post-owned-read":
							trigger = 7
						}
					}
					calls := 0
					if stage == "entry" {
						mutate()
					} else {
						o.now = func() time.Time {
							calls++
							if calls == trigger {
								mutate()
							}
							return at
						}
					}
					p, err := o.prepareMCPTargetList(r, sharedTargetListRequest{Kind: kind})
					if !errors.Is(err, want) || p.Targets != nil || p.NextCursor != "" {
						t.Fatalf("late guard got %v want %v", err, want)
					}
					if stage != "entry" && calls < trigger {
						t.Fatal("checkpoint missed", calls, trigger)
					}
				})
			}
		}
	}
}

func TestSharedTargetListFinalOwnedReadReplacement(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, change := range []string{"identical", "owner-removed", "owner-reassigned", "grant-revoked", "device-replaced", "stale", "provider"} {
			t.Run(fmt.Sprintf("%v/%s", durable, change), func(t *testing.T) {
				o, r, at := sharedListFixture(t, sharedTargetApplication, 2, durable)
				req := sharedTargetListRequest{Kind: sharedTargetApplication, Limit: 1}
				first := sharedListResult(t, o, r, req, nil)
				req.Cursor = first.NextCursor
				before := sharedListResult(t, o, r, req, nil)
				old := o.cache.Get()
				next := *old
				policy := *old.Policy
				policy.Grants = append([]GrantRule(nil), old.Policy.Grants...)
				next.Policy = &policy
				next.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{}
				for k, v := range old.TailscaleUsersByLogin {
					next.TailscaleUsersByLogin[k] = v
				}
				next.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{}
				for k, v := range old.PresentationDeviceIDsByNodeID {
					next.PresentationDeviceIDsByNodeID[k] = v
				}
				want := error(nil)
				switch change {
				case "owner-removed":
					delete(next.TailscaleUsersByLogin, "alice@example.com")
					want = errMetadataNotVisible
				case "owner-reassigned":
					next.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-owner", DirectMember: true}
					want = errMetadataNotVisible
				case "grant-revoked":
					next.Policy.Grants = nil
					want = errMetadataConflict
				case "device-replaced":
					key := next.PresentationDeviceIDsByNodeID["node-1"]
					key.Value = "replacement"
					next.PresentationDeviceIDsByNodeID["node-1"] = key
					want = errMetadataConflict
				case "stale":
					next.UpdatedAt = at.Add(-91 * time.Second)
					want = errMetadataStale
				case "provider":
					next.ControlPlane.Provider = controlPlaneHeadscale
					want = errMetadataStale
				}
				trigger := 7
				if durable {
					trigger = 10
				}
				calls := 0
				o.now = func() time.Time {
					calls++
					if calls == trigger {
						o.cache.data.Store(&next)
					}
					return at
				}
				p := sharedListResult(t, o, r, req, want)
				if calls != trigger || o.cache.Get() != &next {
					t.Fatal("not final post-read replacement", calls, trigger)
				}
				if want == nil && !reflect.DeepEqual(p, before) {
					t.Fatal("equivalent replacement")
				}
				if old.TailscaleUsersByLogin["alice@example.com"].ID != "alice-id" || len(old.Policy.Grants) == 0 || old.UpdatedAt != at || old.PresentationDeviceIDsByNodeID["node-1"].Value != "node-1" {
					t.Fatal("mutated old snapshot")
				}
			})
		}
	}
}

func TestSharedTargetListLocksFreshnessAndCancellation(t *testing.T) {
	for _, lock := range []string{"editor", "personal", "storage"} {
		for _, change := range []string{"stale", "cancel"} {
			t.Run(lock+"/"+change, func(t *testing.T) {
				o, r, at := sharedListFixture(t, sharedTargetApplication, 1, true)
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				var mu *sync.Mutex
				switch lock {
				case "editor":
					mu = &o.editor.mu
				case "personal":
					mu = &o.personal.mu
				case "storage":
					mu = &o.personal.storage.mu
				}
				mu.Lock()
				started := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					close(started)
					p, err := o.prepareMCPTargetList(r, sharedTargetListRequest{Kind: sharedTargetApplication})
					if p.Targets != nil {
						done <- fmt.Errorf("returned page")
						return
					}
					done <- err
				}()
				<-started
				if change == "cancel" {
					cancel()
				} else {
					d := *o.cache.Get()
					d.UpdatedAt = at.Add(-91 * time.Second)
					o.cache.data.Store(&d)
				}
				mu.Unlock()
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("wait lost guard")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("lock order deadlock")
				}
			})
		}
	}
	// At the post-owned-read clock checkpoint both operation locks remain pinned,
	// but storage.mu must already be released (never recursively acquired).
	o, r, at := sharedListFixture(t, sharedTargetApplication, 1, true)
	calls := 0
	o.now = func() time.Time {
		calls++
		if calls == 10 {
			if o.personal.mu.TryLock() {
				o.personal.mu.Unlock()
				t.Error("personal not pinned")
			}
			if o.editor.mu.TryLock() {
				o.editor.mu.Unlock()
				t.Error("editor not pinned")
			}
			if !o.personal.storage.mu.TryLock() {
				t.Error("storage not transient")
			} else {
				o.personal.storage.mu.Unlock()
			}
		}
		return at
	}
	sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetApplication}, nil)
	if calls != 10 {
		t.Fatal("owned-read bound", calls)
	}
}

func TestSharedTargetListCurrentTargetChangesAndFinalRevocation(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		for _, change := range []string{"access", "name", "replacement", "stale", "unrelated"} {
			for _, late := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/late-%v", kind, change, late), func(t *testing.T) {
					o, r, at := sharedListFixture(t, kind, 3, false)
					req := sharedTargetListRequest{Kind: kind, Limit: 1}
					first := sharedListResult(t, o, r, req, nil)
					req.Cursor = first.NextCursor
					original := sharedListResult(t, o, r, req, nil)
					next := *o.cache.Get()
					policy := *next.Policy
					next.Policy = &policy
					next.ProxyHosts = append([]ProxyHost(nil), next.ProxyHosts...)
					next.Nodes = append([]Node(nil), next.Nodes...)
					want := error(nil)
					mutate := func() {
						switch change {
						case "access":
							policy.ACLs = nil
							policy.Grants = nil
							want = errMetadataConflict
						case "stale":
							next.UpdatedAt = at.Add(-91 * time.Second)
							want = errMetadataStale
						case "name":
							if kind == sharedTargetNPM {
								for i := range next.ProxyHosts {
									next.ProxyHosts[i].DomainNames = []string{fmt.Sprintf("new-%d.example", i)}
								}
							} else if kind == sharedTargetMachine {
								for i := range next.Nodes {
									next.Nodes[i].Name = fmt.Sprintf("new-%d.tailnet.ts.net", i)
								}
							} else {
								// Fixture mutation under the already held personal lock in the late
								// case models an authored evidence change without another writer.
								c := o.personal.collections["alice@example.com"]
								c.document = copyPersonalDocument(c.document)
								for i := range c.document.Entries {
									c.document.Entries[i].Name = "Changed name"
								}
								o.personal.collections["alice@example.com"] = c
							}
							if late {
								want = errMetadataConflict
							}
						case "replacement":
							if kind == sharedTargetNPM {
								for i := range next.ProxyHosts {
									next.ProxyHosts[i].ID += 1000
								}
							} else {
								next.PresentationDeviceIDsByNodeID = map[string]ProviderDeviceID{}
								for k, v := range o.cache.Get().PresentationDeviceIDsByNodeID {
									v.Value += "-replacement"
									next.PresentationDeviceIDsByNodeID[k] = v
								}
							}
							want = errMetadataConflict
						case "unrelated":
							next.ProxyHosts = append(next.ProxyHosts, ProxyHost{ID: 99999, Enabled: false})
						}
						o.cache.data.Store(&next)
					}
					if !late {
						mutate()
					} else {
						calls := 0
						trigger := 3
						if kind == sharedTargetApplication {
							trigger = 6
						}
						o.now = func() time.Time {
							calls++
							if calls == trigger {
								mutate()
							}
							return at
						}
					}
					p, err := o.prepareMCPTargetList(r, req)
					if !errors.Is(err, want) {
						t.Fatalf("current target change %v want %v", err, want)
					}
					if want != nil && (p.Targets != nil || p.NextCursor != "") {
						t.Fatal("partial revoked page")
					}
					if change == "unrelated" && !reflect.DeepEqual(p, original) {
						t.Fatal("unrelated change affected page")
					}
					if change == "name" && !late && reflect.DeepEqual(p, original) {
						t.Fatal("new current presentation not used")
					}
				})
			}
		}
	}
}

func TestSharedTargetListEarlierInsertionNeedsNewListing(t *testing.T) {
	o, r, _ := sharedListFixture(t, sharedTargetNPM, 3, false)
	first := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Limit: 1}, nil)
	anchor := first.Targets[0].Target.Handle
	before := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Cursor: first.NextCursor}, nil)
	d := *o.cache.Get()
	host := d.ProxyHosts[0]
	found := false
	for id := 10000; id < 20000; id++ {
		if o.sharedTargetHandle("alice@example.com", sharedTargetNPM, fmt.Sprint(id)).Handle < anchor {
			host.ID = id
			found = true
			break
		}
	}
	if !found {
		t.Fatal("fixture cannot find earlier handle")
	}
	host.DomainNames = []string{"inserted.example"}
	d.ProxyHosts = append(append([]ProxyHost(nil), d.ProxyHosts...), host)
	o.cache.data.Store(&d)
	after := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM, Cursor: first.NextCursor}, nil)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("cursor acts as offset")
	}
	fresh := sharedListResult(t, o, r, sharedTargetListRequest{Kind: sharedTargetNPM}, nil)
	if len(fresh.Targets) != 4 || fresh.Targets[0].Name != "inserted.example" {
		t.Fatal("fresh listing missed earlier insertion")
	}
}

func TestSharedTargetListDiskConflictAndNoPrivateMutation(t *testing.T) {
	for _, kind := range []sharedTargetKind{sharedTargetNPM, sharedTargetMachine, sharedTargetApplication} {
		t.Run(string(kind), func(t *testing.T) {
			o, r, _ := sharedListFixture(t, kind, 2, false)
			view := o.editor.view.Load()
			changed := append(append([]byte(nil), view.bytes...), byte(' '))
			if err := os.WriteFile(o.editor.path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			sharedListResult(t, o, r, sharedTargetListRequest{Kind: kind}, errMetadataConflict)
			if o.editor.view.Load() != view || o.editor.disabled {
				t.Fatal("read changed confirmed writer")
			}
			actual, _ := os.ReadFile(o.editor.path)
			if !bytes.Equal(actual, changed) {
				t.Fatal("list repaired external disk")
			}
		})
	}
}

func TestSharedTargetListByteBudgetAndOversizedItem(t *testing.T) {
	o, r, _ := sharedListFixture(t, sharedTargetNPM, 1, false)
	_ = r
	// Synthetic summary inputs stress the pure paginator beyond current display
	// bounds so a future widening cannot silently return a truncated success.
	targets := make([]sharedSelectedTarget, 65)
	for i := range targets {
		targets[i].summary = sharedTargetSummary{Target: o.sharedTargetHandle("alice@example.com", sharedTargetNPM, fmt.Sprint(i)), Name: strings.Repeat("<\"\\界", 400)}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].summary.Target.Handle < targets[j].summary.Target.Handle })
	cursor := ""
	seen := 0
	pages := 0
	for {
		p, err := o.sharedTargetPage("alice@example.com", sharedTargetNPM, cursor, 64, targets)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		seen += len(p.Targets)
		bound, _ := mcpToolResultSizeBound(p)
		wire := encodedSharedPage(t, p, json.RawMessage(`"`+strings.Repeat("<", 256)+`"`))
		if bound > 256*1024 || len(wire) > 256*1024 || len(wire) > bound || len(p.Targets) == 0 {
			t.Fatal("unsafe byte page")
		}
		if p.NextCursor == "" {
			break
		}
		cursor, err = o.sharedTargetCursorAnchor("alice@example.com", sharedTargetNPM, p.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
	}
	if seen != 65 || pages < 2 {
		t.Fatal("byte pagination did not split")
	}
	targets[0].summary.Name = strings.Repeat("<", 256*1024)
	p, err := o.sharedTargetPage("alice@example.com", sharedTargetNPM, "", 64, targets[:1])
	if !errors.Is(err, errMetadataStale) || p.Targets != nil || p.NextCursor != "" {
		t.Fatal("single oversized item truncated success")
	}
	for _, name := range []string{strings.Repeat("😀", 121), strings.Repeat("<", 121), "private\x00control", "\xff"} {
		got := sharedTargetDisplayName(name, "Service")
		if strings.ContainsAny(name, "\x00\xff") {
			if got != "Service" {
				t.Fatal("control fallback")
			}
		} else if len([]rune(got)) != 120 {
			t.Fatal("display rune bound")
		}
	}
}
