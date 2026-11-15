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
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Positive operation evidence always uses the actual IdentityMiddleware request,
// a real configured private store, concrete MCP headers and the production entry.
// No auth callback, fake browser request/CSRF, test-only commit assembly or gate.
type personalOpsFixture struct {
	s      *PersonalServicesStore
	cache  *Cache
	config *MCPConfig
	at     time.Time
}

func newPersonalOpsFixture(t *testing.T, records []personalStorageCollection) *personalOpsFixture {
	t.Helper()
	s := durableTestStore(t, durableTestConfig(t, records))
	cache := durableTestCache(t)
	return &personalOpsFixture{s: s, cache: cache, config: &MCPConfig{Origin: s.config.Origin, Host: s.config.Host}, at: cache.Get().UpdatedAt}
}
func (f *personalOpsFixture) now() time.Time { return f.at }
func (f *personalOpsFixture) request(t *testing.T, browser bool, login string) *http.Request {
	t.Helper()
	r := mcpTestRequest("", true)
	r.Host = f.config.Host
	r.URL.Host = f.config.Host
	r.Header.Set("Tailscale-User-Login", login)
	if browser {
		r.Header.Set("Origin", f.config.Origin)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("Sec-Fetch-Dest", "empty")
	}
	return personalOpsAdmit(t, r)
}
func personalOpsAdmit(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	var actual *http.Request
	IdentityMiddleware(mcpTrusted(), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { actual = r })).ServeHTTP(httptest.NewRecorder(), r)
	if actual == nil {
		t.Fatal("middleware rejected fixture")
	}
	return actual
}
func (f *personalOpsFixture) read(r *http.Request) (PersonalServicesState, *CacheData, error) {
	return f.s.ReadMCP(r, f.config, f.cache, mcpTrusted(), 30*time.Second, f.now)
}
func (f *personalOpsFixture) mutate(r *http.Request, req personalMCPMutation) (PersonalServicesState, *CacheData, error) {
	return f.s.MutateMCP(r, req, f.config, f.cache, mcpTrusted(), 30*time.Second, f.now)
}
func personalOpsCreate(state PersonalServicesState) personalMCPMutation {
	return personalMCPMutation{StoreID: state.StoreID, Generation: state.Generation, Revision: state.Revision, Action: "create", Name: "Own authored title", URL: "https://server.tailnet.ts.net/app"}
}
func personalOpsState(t *testing.T, f *personalOpsFixture, r *http.Request) PersonalServicesState {
	t.Helper()
	state, data, err := f.read(r)
	if err != nil || data != f.cache.Get() {
		t.Fatal("owned read", err)
	}
	return state
}
func personalOpsZero(t *testing.T, state PersonalServicesState, data *CacheData, err error) {
	t.Helper()
	if err == nil || !reflect.DeepEqual(state, PersonalServicesState{}) || data != nil {
		t.Fatalf("error exposed private result: %v zero=%v data=%v", err, reflect.DeepEqual(state, PersonalServicesState{}), data != nil)
	}
}
func personalOpsBytes(t *testing.T, f *personalOpsFixture) []byte {
	t.Helper()
	b, err := os.ReadFile(f.s.config.File)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPersonalOperationsMCPCRUDAndInactive(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, inactive := range []bool{false, true} {
			t.Run(fmt.Sprintf("browser=%v/inactive=%v", browser, inactive), func(t *testing.T) {
				f := newPersonalOpsFixture(t, []personalStorageCollection{})
				r := f.request(t, browser, "alice@example.com")
				if inactive {
					d := *f.cache.Get()
					d.Nodes = nil
					d.GrantRoleSelectorsByLogin = nil
					f.cache.data.Store(&d)
				}
				body := &mcpGuardBody{}
				r.Body = body
				first := personalOpsState(t, f, r)
				if !first.Durable || first.Initialized || first.Revision != "0" || first.Document.Entries == nil {
					t.Fatal("absent")
				}
				req := personalOpsCreate(first)
				saved, data, err := f.mutate(r, req)
				if err != nil || data != f.cache.Get() || saved.Revision != "1" || !saved.Initialized || len(saved.Document.Entries) != 1 {
					t.Fatal("create", err)
				}
				if saved.Document.Entries[0].Icon != "" {
					t.Fatal("computed default materialized")
				}
				_, statuses := ProjectPersonalServices(IdentityFromContext(r.Context()), data, saved, f.at, 30*time.Second)
				if len(statuses) != 1 || (statuses[0].Status == "active") == inactive {
					t.Fatal("projection mismatch")
				}
				req = personalOpsCreate(saved)
				req.Action = "update"
				req.ID = saved.Document.Entries[0].ID
				req.Name = "Repaired"
				req.Icon = "generic"
				updated, _, err := f.mutate(r, req)
				if err != nil || updated.Revision != "2" || updated.Document.Entries[0].Name != "Repaired" {
					t.Fatal("update", err)
				}
				req = personalMCPMutation{StoreID: updated.StoreID, Generation: updated.Generation, Revision: updated.Revision, Action: "delete", ID: req.ID}
				empty, _, err := f.mutate(r, req)
				if err != nil || empty.Revision != "3" || !empty.Initialized || len(empty.Document.Entries) != 0 {
					t.Fatal("delete", err)
				}
				if got := personalOpsState(t, f, r); !reflect.DeepEqual(got, empty) {
					t.Fatal("empty not authoritative")
				}
				if body.reads != 0 || body.closes != 0 || len(f.s.requests) != 0 {
					t.Fatal("body reread or admission leaked")
				}
				encoded, _ := json.Marshal(saved)
				for _, secret := range []string{"alice-id", "fixture-1", "namespace", "user_id", "csrf", "identity_scope", "private_file", "total"} {
					if bytes.Contains(encoded, []byte(secret)) {
						t.Fatal("private boundary", secret)
					}
				}
				saved.Document.Entries[0].Name = "client change"
				if len(personalOpsState(t, f, r).Document.Entries) != 0 {
					t.Fatal("result aliases store")
				}
			})
		}
	}
}

func TestPersonalOperationsUnavailableAndTypedBoundary(t *testing.T) {
	for _, name := range []string{"nil-store", "volatile", "closed", "nil-config", "config-origin", "config-host", "personal-origin", "personal-host", "personal-file", "personal-namespace", "nil-clock", "nil-cache", "nil-trust", "nil-request", "nil-slots", "wrong-slots", "zero-interval", "overflow-interval"} {
		t.Run(name, func(t *testing.T) {
			f := newPersonalOpsFixture(t, []personalStorageCollection{})
			r := f.request(t, false, "alice@example.com")
			req := personalOpsCreate(personalOpsState(t, f, r))
			s := f.s
			cfg := f.config
			cache := f.cache
			trusted := mcpTrusted()
			now := f.now
			interval := 30 * time.Second
			switch name {
			case "nil-store":
				s = nil
			case "volatile":
				s = personalTestStore(t)
			case "closed":
				_ = s.Close()
			case "nil-config":
				cfg = nil
			case "config-origin":
				cfg.Origin = "http://other.example:8081"
			case "config-host":
				cfg.Host = "other.example:8081"
			case "personal-origin":
				s.config.Origin += "/"
			case "personal-host":
				s.config.Host = "other.example"
			case "personal-file":
				s.config.File = ""
			case "personal-namespace":
				s.config.Namespace = "other"
			case "nil-clock":
				now = nil
			case "nil-cache":
				cache = nil
			case "nil-trust":
				trusted = nil
			case "nil-request":
				r = nil
			case "nil-slots":
				s.requests = nil
			case "wrong-slots":
				s.requests = make(chan struct{}, 9)
			case "zero-interval":
				interval = 0
			case "overflow-interval":
				interval = time.Duration(math.MaxInt64)
			}
			state, data, err := s.ReadMCP(r, cfg, cache, trusted, interval, now)
			personalOpsZero(t, state, data, err)
			state, data, err = s.MutateMCP(r, req, cfg, cache, trusted, interval, now)
			personalOpsZero(t, state, data, err)
			if s != nil && len(s.requests) != 0 {
				t.Fatal("leaked slot")
			}
		})
	}
	for _, name := range []string{"restore", "import", "unknown", "empty-action", "create-id", "delete-name", "delete-url", "delete-icon", "bad-id", "missing-name", "bad-url", "bad-icon", "bad-store", "bad-generation", "bad-revision", "oversize-name", "oversize-url"} {
		t.Run(name, func(t *testing.T) {
			f := newPersonalOpsFixture(t, []personalStorageCollection{})
			r := f.request(t, false, "alice@example.com")
			req := personalOpsCreate(personalOpsState(t, f, r))
			before := personalOpsBytes(t, f)
			switch name {
			case "restore", "import", "unknown":
				req.Action = name
			case "empty-action":
				req.Action = ""
			case "create-id":
				req.ID = personalTestEntry(0).ID
			case "delete-name", "delete-url", "delete-icon":
				req.Action = "delete"
				req.ID = personalTestEntry(0).ID
				req.Name = ""
				req.URL = ""
				if name == "delete-name" {
					req.Name = "x"
				}
				if name == "delete-url" {
					req.URL = "https://server.tailnet.ts.net"
				}
				if name == "delete-icon" {
					req.Icon = "generic"
				}
			case "bad-id":
				req.Action = "update"
				req.ID = "1"
			case "missing-name":
				req.Name = ""
			case "bad-url":
				req.URL = "https://example.com"
			case "bad-icon":
				req.Icon = "remote"
			case "bad-store":
				req.StoreID = ""
			case "bad-generation":
				req.Generation = strings.Repeat("A", 32)
			case "bad-revision":
				req.Revision = "01"
			case "oversize-name":
				req.Name = strings.Repeat("x", 121)
			case "oversize-url":
				req.URL = "https://server.tailnet.ts.net/" + strings.Repeat("x", 2048)
			}
			state, data, err := f.mutate(r, req)
			personalOpsZero(t, state, data, err)
			if !errors.Is(err, errPersonalInvalid) || !bytes.Equal(before, personalOpsBytes(t, f)) || len(f.s.requests) != 0 {
				t.Fatal("typed boundary", err)
			}
		})
	}
}

// Replacement snapshots never mutate A. Grant/device loss is intentionally NOT
// management revocation. Owner changes/freshness/provider loss are revocation.
func personalOpsReplacement(f *personalOpsFixture, name string) *CacheData {
	old := f.cache.Get()
	d := *old
	d.TailscaleUsersByLogin = make(map[string]TailscaleUserIdentity)
	for k, v := range old.TailscaleUsersByLogin {
		d.TailscaleUsersByLogin[k] = v
	}
	switch name {
	case "removed":
		delete(d.TailscaleUsersByLogin, "alice@example.com")
	case "shared":
		d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
	case "reassigned":
		d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "replacement", DirectMember: true}
	case "invalid-owner":
		d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: " padded", DirectMember: true}
	case "missing-map":
		d.TailscaleUsersByLogin = nil
	case "stale":
		d.UpdatedAt = f.at.Add(-91 * time.Second)
	case "future":
		d.UpdatedAt = f.at.Add(time.Second)
	case "zero-time":
		d.UpdatedAt = time.Time{}
	case "provider":
		d.ControlPlane.Provider = controlPlaneHeadscale
	case "policy":
		d.Policy = nil
	case "cold":
		return nil
	case "inactive":
		d.Nodes = nil
		d.GrantRoleSelectorsByLogin = nil
	}
	return &d
}
func TestPersonalOperationsOwnershipAtEntry(t *testing.T) {
	for _, name := range []string{"removed", "shared", "reassigned", "invalid-owner", "missing-map", "stale", "future", "zero-time", "provider", "policy", "cold", "renamed", "wrong-login"} {
		t.Run(name, func(t *testing.T) {
			f := newPersonalOpsFixture(t, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", []PersonalService{personalTestEntry(0)})})
			r := f.request(t, false, "alice@example.com")
			req := personalOpsCreate(personalOpsState(t, f, r))
			before := personalOpsBytes(t, f)
			if name == "wrong-login" {
				r = f.request(t, false, "mallory@example.com")
			} else if name == "renamed" {
				d := personalOpsReplacement(f, "removed")
				d.TailscaleUsersByLogin["new@example.com"] = TailscaleUserIdentity{ID: "alice-id", DirectMember: true}
				f.cache.data.Store(d)
				r = f.request(t, false, "new@example.com")
			} else {
				f.cache.data.Store(personalOpsReplacement(f, name))
			}
			state, data, err := f.read(r)
			personalOpsZero(t, state, data, err)
			state, data, err = f.mutate(r, req)
			personalOpsZero(t, state, data, err)
			if !bytes.Equal(before, personalOpsBytes(t, f)) {
				t.Fatal("owner denial changed file")
			}
		})
	}
}

func TestPersonalOperationsReplacementAfterReadAndBeforeReturn(t *testing.T) {
	for _, mode := range []string{"read-after-storage", "read-return", "mcp-mutation", "browser-mutation"} {
		for _, name := range []string{"identical", "removed", "shared", "reassigned", "stale", "provider", "cold", "inactive"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				f := newPersonalOpsFixture(t, []personalStorageCollection{})
				r := f.request(t, false, "alice@example.com")
				first := personalOpsState(t, f, r)
				req := personalOpsCreate(first)
				original := f.cache.Get()
				originalJSON, _ := json.Marshal(original)
				next := personalOpsReplacement(f, name)
				before := personalOpsBytes(t, f)
				calls := 0
				trigger := 3
				if mode == "read-return" || strings.Contains(mode, "mutation") {
					trigger = 4
				}
				now := func() time.Time {
					calls++
					if calls == trigger {
						f.cache.data.Store(next)
					}
					return f.at
				}
				var state PersonalServicesState
				var data *CacheData
				var err error
				switch mode {
				case "read-after-storage", "read-return":
					state, data, err = f.s.ReadMCP(r, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
				case "mcp-mutation":
					state, data, err = f.s.MutateMCP(r, req, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
				case "browser-mutation":
					br := personalOpsAdmit(t, personalHTTPRequest("POST", nil))
					breq := personalTestMutation(first, "create")
					breq.StoreID = first.StoreID
					breq.CSRF = f.s.csrf("alice@example.com", f.at)
					state, data, err = f.s.MutateBrowser(br, breq, f.cache, mcpTrusted(), 30*time.Second, now)
				}
				afterJSON, _ := json.Marshal(original)
				if calls < trigger || f.cache.Get() != next || !bytes.Equal(originalJSON, afterJSON) {
					t.Fatal("did not replace intact snapshot A")
				}
				success := name == "identical" || name == "inactive"
				if success {
					if err != nil || data != next || !state.Durable {
						t.Fatal("current owner lost management", err)
					}
				} else {
					personalOpsZero(t, state, data, err)
					if !bytes.Equal(before, personalOpsBytes(t, f)) {
						t.Fatal("denied replacement committed")
					}
				}
			})
		}
	}
}

func personalOpsRequestChanges(t *testing.T, f *personalOpsFixture) map[string]func(*http.Request) {
	return map[string]func(*http.Request){
		"source":           func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1" },
		"raw-login":        func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "bob@example.com") },
		"context-login":    func(r *http.Request) { IdentityFromContext(r.Context()).Login = "bob@example.com" },
		"both-login":       func(r *http.Request) { other := f.request(t, false, "bob@example.com"); *r = *other },
		"no-context":       func(r *http.Request) { *r = *r.WithContext(context.Background()) },
		"missing-login":    func(r *http.Request) { r.Header.Del("Tailscale-User-Login") },
		"padded-login":     func(r *http.Request) { r.Header.Set("Tailscale-User-Login", " alice@example.com") },
		"method":           func(r *http.Request) { r.Method = "GET" },
		"host":             func(r *http.Request) { r.Host = "evil.example" },
		"path":             func(r *http.Request) { r.URL.Path = "/mcp/" },
		"raw-path":         func(r *http.Request) { r.URL.RawPath = "/%6dcp" },
		"query":            func(r *http.Request) { r.URL.RawQuery = "x=1" },
		"force-query":      func(r *http.Request) { r.URL.ForceQuery = true },
		"version":          func(r *http.Request) { r.Header.Del("MCP-Protocol-Version") },
		"wrong-version":    func(r *http.Request) { r.Header.Set("MCP-Protocol-Version", "old") },
		"content":          func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") },
		"accept":           func(r *http.Request) { r.Header.Del("Accept") },
		"origin":           func(r *http.Request) { r.Header.Set("Origin", "null") },
		"fetch":            func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") },
		"authorization":    func(r *http.Request) { r.Header["Authorization"] = []string{""} },
		"cookie":           func(r *http.Request) { r.Header["Cookie"] = []string{""} },
		"session":          func(r *http.Request) { r.Header["Mcp-Session-Id"] = []string{""} },
		"duplicate-login":  func(r *http.Request) { r.Header["tailscale-user-login"] = []string{"alice@example.com"} },
		"duplicate-origin": func(r *http.Request) { r.Header["Origin"] = []string{f.config.Origin, f.config.Origin} },
		"duplicate-version": func(r *http.Request) {
			r.Header["Mcp-Protocol-Version"] = []string{mcpProtocolVersion, mcpProtocolVersion}
		},
		"trailer":  func(r *http.Request) { r.Trailer = http.Header{"X": []string{"y"}} },
		"oversize": func(r *http.Request) { r.ContentLength = mcpMaxRequestBytes + 1 },
		"cancel": func(r *http.Request) {
			ctx, cancel := context.WithCancel(r.Context())
			*r = *r.WithContext(ctx)
			cancel()
		},
		"mcp-config":      func(r *http.Request) { f.config.Origin = "http://other.example:8081" },
		"personal-config": func(r *http.Request) { f.s.config.Host = "other.example:8081" },
		"storage-config":  func(r *http.Request) { f.s.config.Namespace = "other" },
	}
}
func TestPersonalOperationsConcreteRequestCheckpoints(t *testing.T) {
	for _, browser := range []bool{false, true} {
		for _, checkpoint := range []string{"entry-read", "entry-mutate", "read-after-storage", "read-return", "final", "post-sync"} {
			// Enumerate names only here; every closure used below belongs to its fixture.
			names := personalOpsRequestChanges(t, &personalOpsFixture{})
			for name := range names {
				// A different fully admitted caller BEFORE entry is a legitimate own request,
				// not a late caller substitution. Pinning is tested at all later checkpoints.
				if name == "both-login" && strings.HasPrefix(checkpoint, "entry") {
					continue
				}
				t.Run(fmt.Sprintf("browser=%v/%s/%s", browser, checkpoint, name), func(t *testing.T) {
					f := newPersonalOpsFixture(t, []personalStorageCollection{})
					r := f.request(t, browser, "alice@example.com")
					req := personalOpsCreate(personalOpsState(t, f, r))
					change := personalOpsRequestChanges(t, f)[name]
					before := personalOpsBytes(t, f)
					body := &mcpGuardBody{}
					r.Body = body
					hit := false
					apply := func() { hit = true; change(r) }
					if strings.HasPrefix(checkpoint, "entry") {
						apply()
					}
					calls := 0
					now := func() time.Time {
						calls++
						if checkpoint == "read-after-storage" && calls == 3 || checkpoint == "read-return" && calls == 4 {
							apply()
						}
						return f.at
					}
					f.s.storage.failpoint = func(stage string) error {
						if checkpoint == "final" && stage == "recheck" || checkpoint == "post-sync" && stage == "publish" {
							apply()
						}
						return nil
					}
					var state PersonalServicesState
					var data *CacheData
					var err error
					if strings.Contains(checkpoint, "read") {
						state, data, err = f.s.ReadMCP(r, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
					} else {
						state, data, err = f.s.MutateMCP(r, req, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
					}
					personalOpsZero(t, state, data, err)
					if !hit || len(f.s.requests) != 0 || body.reads != 0 || body.closes != 0 {
						t.Fatal("checkpoint/admission/body")
					}
					after := personalOpsBytes(t, f)
					if (checkpoint == "post-sync") == bytes.Equal(before, after) {
						t.Fatal("commit/withholding truth")
					}
				})
			}
		}
	}
}

func TestPersonalOperationsOwnerFinalAndPostSync(t *testing.T) {
	for _, transport := range []string{"native", "browser-origin", "browser-api"} {
		for _, stage := range []string{"recheck", "publish"} {
			for _, name := range []string{"removed", "reassigned", "shared", "stale", "provider", "cold", "inactive"} {
				t.Run(transport+"/"+stage+"/"+name, func(t *testing.T) {
					f := newPersonalOpsFixture(t, []personalStorageCollection{})
					r := f.request(t, transport == "browser-origin", "alice@example.com")
					first := personalOpsState(t, f, r)
					req := personalOpsCreate(first)
					before := personalOpsBytes(t, f)
					old := f.cache.Get()
					next := personalOpsReplacement(f, name)
					oldJSON, _ := json.Marshal(old)
					hit := false
					f.s.storage.failpoint = func(at string) error {
						if at == stage {
							hit = true
							f.cache.data.Store(next)
						}
						return nil
					}
					var state PersonalServicesState
					var data *CacheData
					var err error
					if transport == "browser-api" {
						br := personalOpsAdmit(t, personalHTTPRequest("POST", nil))
						breq := personalTestMutation(first, "create")
						breq.StoreID = first.StoreID
						breq.CSRF = f.s.csrf("alice@example.com", f.at)
						state, data, err = f.s.MutateBrowser(br, breq, f.cache, mcpTrusted(), 30*time.Second, f.now)
					} else {
						state, data, err = f.mutate(r, req)
					}
					checkJSON, _ := json.Marshal(old)
					if !hit || !bytes.Equal(oldJSON, checkJSON) {
						t.Fatal("replacement fixture")
					}
					if name == "inactive" {
						if err != nil || data != next {
							t.Fatal("inactive management denied", err)
						}
					} else {
						personalOpsZero(t, state, data, err)
					}
					committed := stage == "publish" || name == "inactive"
					if committed == bytes.Equal(before, personalOpsBytes(t, f)) {
						t.Fatal("commit truth")
					}
					f.s.storage.failpoint = nil
					f.cache.data.Store(old)
					current := personalOpsState(t, f, r)
					if committed {
						if current.Revision != "1" {
							t.Fatal("committed write lost")
						}
						state, data, err = f.mutate(r, req)
						personalOpsZero(t, state, data, err)
						if !errors.Is(err, errPersonalConflict) {
							t.Fatal("replay not conflicted", err)
						}
					} else if current.Revision != "0" {
						t.Fatal("denial changed revision")
					}
				})
			}
		}
	}
}

func TestPersonalOperationsLockWaitRechecks(t *testing.T) {
	for _, op := range []string{"read", "mcp", "browser"} {
		for _, lock := range []string{"personal", "storage"} {
			for _, name := range []string{"removed", "stale", "provider", "cold", "cancel", "both-login", "inactive"} {
				t.Run(op+"/"+lock+"/"+name, func(t *testing.T) {
					f := newPersonalOpsFixture(t, []personalStorageCollection{})
					r := f.request(t, false, "alice@example.com")
					first := personalOpsState(t, f, r)
					req := personalOpsCreate(first)
					breq := personalTestMutation(first, "create")
					breq.StoreID = first.StoreID
					breq.CSRF = f.s.csrf("alice@example.com", f.at)
					if op == "browser" {
						r = personalOpsAdmit(t, personalHTTPRequest("POST", nil))
					}
					before := personalOpsBytes(t, f)
					ready := make(chan struct{})
					resume := make(chan struct{})
					done := make(chan struct{})
					calls := 0
					trigger := 1
					if lock == "storage" {
						trigger = 3
						if op == "read" {
							trigger = 2
						}
					}
					now := func() time.Time {
						calls++
						if calls == trigger {
							close(ready)
							<-resume
						}
						return f.at
					}
					if lock == "personal" {
						f.s.mu.Lock()
					} else {
						f.s.storage.mu.Lock()
					}
					var state PersonalServicesState
					var data *CacheData
					var err error
					go func() {
						defer close(done)
						switch op {
						case "read":
							state, data, err = f.s.ReadMCP(r, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
						case "mcp":
							state, data, err = f.s.MutateMCP(r, req, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
						case "browser":
							state, data, err = f.s.MutateBrowser(r, breq, f.cache, mcpTrusted(), 30*time.Second, now)
						}
					}()
					<-ready
					if name == "cancel" {
						ctx, cancel := context.WithCancel(r.Context())
						*r = *r.WithContext(ctx)
						cancel()
					} else if name == "both-login" {
						IdentityFromContext(r.Context()).Login = "bob@example.com"
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					} else {
						f.cache.data.Store(personalOpsReplacement(f, name))
					}
					close(resume)
					if lock == "personal" {
						f.s.mu.Unlock()
					} else {
						f.s.storage.mu.Unlock()
					}
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Fatal("lock recheck deadlocked")
					}
					if name == "inactive" {
						if err != nil {
							t.Fatal("inactive", err)
						}
					} else {
						personalOpsZero(t, state, data, err)
						if !bytes.Equal(before, personalOpsBytes(t, f)) {
							t.Fatal("wait denial wrote")
						}
					}
					if len(f.s.requests) != 0 {
						t.Fatal("wait leaked token")
					}
				})
			}
		}
	}
}

func TestPersonalOperationsCASIsolationAndRestart(t *testing.T) {
	f := newPersonalOpsFixture(t, []personalStorageCollection{})
	a := f.request(t, false, "alice@example.com")
	b := f.request(t, true, "bob@example.com")
	first := personalOpsState(t, f, a)
	alice, _, err := f.mutate(a, personalOpsCreate(first))
	if err != nil {
		t.Fatal(err)
	}
	bob := personalOpsState(t, f, b)
	breq := personalOpsCreate(bob)
	breq.Name = "Foreign private"
	if _, _, err = f.mutate(b, breq); err != nil {
		t.Fatal(err)
	}
	if current := personalOpsState(t, f, a); !reflect.DeepEqual(current, alice) {
		t.Fatal("foreign activity changed own CAS")
	}
	for _, name := range []string{"store", "generation", "revision", "foreign-id", "cross-user-revision"} {
		t.Run(name, func(t *testing.T) {
			req := personalOpsCreate(alice)
			r := a
			switch name {
			case "store":
				req.StoreID = strings.Repeat("b", 32)
			case "generation":
				req.Generation = strings.Repeat("b", 32)
			case "revision":
				req.Revision = "0"
			case "foreign-id":
				req.Action = "delete"
				req.Name = ""
				req.URL = ""
				req.ID = personalOpsState(t, f, b).Document.Entries[0].ID
			case "cross-user-revision":
				req.Revision = "0"
				r = b
			}
			state, data, err := f.mutate(r, req)
			personalOpsZero(t, state, data, err)
			if !errors.Is(err, errPersonalConflict) {
				t.Fatal(err)
			}
		})
	}
	// Equal epoch/store/revision values are collection-local, not identity tokens:
	// a foreign entry ID is still absent from the current caller's collection.
	cfg := f.s.config
	if err = f.s.Close(); err != nil {
		t.Fatal(err)
	}
	f.s = durableTestStore(t, &cfg)
	restarted := personalOpsState(t, f, a)
	if restarted.Generation == alice.Generation || restarted.StoreID != alice.StoreID || restarted.Revision != alice.Revision || !reflect.DeepEqual(restarted.Document, alice.Document) {
		t.Fatal("restart semantics")
	}
	state, data, err := f.mutate(a, personalOpsCreate(alice))
	personalOpsZero(t, state, data, err)
	if !errors.Is(err, errPersonalConflict) {
		t.Fatal("old process CAS")
	}
}

func TestPersonalOperationsCapacityAndBounds(t *testing.T) {
	for _, name := range []string{"collections", "entries", "bytes", "revision-overflow"} {
		t.Run(name, func(t *testing.T) {
			records := []personalStorageCollection{}
			want := errPersonalCapacity
			switch name {
			case "collections":
				for i := 0; i < 128; i++ {
					records = append(records, durableRecord(fmt.Sprintf("user%d@example.com", i), fmt.Sprintf("id-%d", i), "1", []PersonalService{}))
				}
			case "entries":
				entries := []PersonalService{}
				for i := 0; i < 128; i++ {
					entries = append(entries, personalTestEntry(i))
				}
				records = append(records, durableRecord("alice@example.com", "alice-id", "1", entries))
			case "bytes":
				entries := []PersonalService{}
				for i := 0; i < 29; i++ {
					e := personalTestEntry(i)
					e.Name = strings.Repeat("n", 120)
					e.URL = "https://server.tailnet.ts.net/" + strings.Repeat("p", 1960)
					entries = append(entries, e)
				}
				records = append(records, durableRecord("alice@example.com", "alice-id", "1", entries))
			case "revision-overflow":
				records = append(records, durableRecord("alice@example.com", "alice-id", "18446744073709551615", []PersonalService{}))
				want = errPersonalUnavailable
			}
			f := newPersonalOpsFixture(t, records)
			r := f.request(t, false, "alice@example.com")
			req := personalOpsCreate(personalOpsState(t, f, r))
			if name == "bytes" {
				req.Name = strings.Repeat("n", 120)
				req.URL = "https://server.tailnet.ts.net/" + strings.Repeat("p", 1960)
			}
			before := personalOpsBytes(t, f)
			state, data, err := f.mutate(r, req)
			personalOpsZero(t, state, data, err)
			if !errors.Is(err, want) || !bytes.Equal(before, personalOpsBytes(t, f)) {
				t.Fatal(name, err)
			}
		})
	}
}

func TestPersonalOperationsFailureAndCrossAdapterUncertainty(t *testing.T) {
	for _, adapter := range []string{"mcp", "browser"} {
		for _, stage := range []string{"create", "write", "filesync", "close", "recheck", "finalcheck", "rename", "rename-error", "rename-performed-error", "afterrename", "dirsync", "publish"} {
			t.Run(adapter+"/"+stage, func(t *testing.T) {
				f := newPersonalOpsFixture(t, []personalStorageCollection{})
				r := f.request(t, false, "alice@example.com")
				first := personalOpsState(t, f, r)
				req := personalOpsCreate(first)
				before := personalOpsBytes(t, f)
				f.s.storage.failpoint = func(at string) error {
					if at == stage {
						return syscall.EIO
					}
					return nil
				}
				if strings.HasPrefix(stage, "rename-") {
					f.s.storage.rename = func(a int, b string, c int, d string) error {
						if stage == "rename-performed-error" {
							if err := unix.Renameat(a, b, c, d); err != nil {
								return err
							}
						}
						return syscall.EIO
					}
				}
				var state PersonalServicesState
				var data *CacheData
				var err error
				if adapter == "mcp" {
					state, data, err = f.mutate(r, req)
				} else {
					br := personalOpsAdmit(t, personalHTTPRequest("POST", nil))
					breq := personalTestMutation(first, "create")
					breq.StoreID = first.StoreID
					breq.CSRF = f.s.csrf("alice@example.com", f.at)
					state, data, err = f.s.MutateBrowser(br, breq, f.cache, mcpTrusted(), 30*time.Second, f.now)
				}
				personalOpsZero(t, state, data, err)
				uncertain := strings.HasPrefix(stage, "rename-") || stage == "afterrename" || stage == "dirsync" || stage == "publish"
				if errors.Is(err, errPersonalStorageUncertain) != uncertain {
					t.Fatal("uncertainty truth", err)
				}
				if !uncertain && !bytes.Equal(before, personalOpsBytes(t, f)) {
					t.Fatal("pre-rename failure changed file")
				}
				f.s.storage.failpoint = nil
				f.s.storage.rename = nil
				if uncertain {
					state, data, err = f.read(r)
					personalOpsZero(t, state, data, err)
					if !errors.Is(err, errPersonalStorageUncertain) {
						t.Fatal(err)
					}
					state, data, err = f.mutate(r, req)
					personalOpsZero(t, state, data, err)
					if !errors.Is(err, errPersonalStorageUncertain) {
						t.Fatal(err)
					}
					h := durableTestHandler(f.s, f.cache, f.now)
					w := httptest.NewRecorder()
					h.ServeHTTP(w, personalHTTPRequest("GET", nil))
					if w.Code != 503 || strings.Contains(w.Body.String(), "document") {
						t.Fatal("browser uncertainty read")
					}
					breq := personalTestMutation(first, "create")
					breq.StoreID = first.StoreID
					breq.CSRF = f.s.csrf("alice@example.com", f.at)
					w, _ = personalHTTPPost(t, h, breq)
					if w.Code != 503 || strings.Contains(w.Body.String(), "document") {
						t.Fatal("browser uncertainty write")
					}
				} else if got := personalOpsState(t, f, r); got.Revision != "0" || got.Initialized {
					t.Fatal("old state changed")
				}
				if len(f.s.requests) != 0 {
					t.Fatal("failpoint leaked admission")
				}
			})
		}
	}
}

func TestPersonalOperationsSharedUncertaintyIndependent(t *testing.T) {
	f := newPersonalOpsFixture(t, []personalStorageCollection{})
	r := f.request(t, false, "alice@example.com")
	o, sharedRequest, ref, _ := sharedTargetFixture(t, sharedTargetNPM)
	o.personal = f.s
	o.editor.mu.Lock()
	o.editor.disabled = true
	o.editor.mu.Unlock()
	first := personalOpsState(t, f, r)
	if _, _, err := f.mutate(r, personalOpsCreate(first)); err != nil {
		t.Fatal("shared uncertainty disabled private", err)
	}
	// Conversely, private uncertainty must not affect ordinary shared NPM browser
	// reads, which neither read the private store nor take its mutex.
	f.s.storage.mu.Lock()
	f.s.storage.uncertain = true
	f.s.storage.mu.Unlock()
	o.editor.mu.Lock()
	o.editor.disabled = false
	o.editor.mu.Unlock()
	// The existing selected-NPM guarded fixture is used only for the independent
	// read boundary; production public new-icon setters remain false-gate blocked.
	func() { f.s.mu.Lock(); defer f.s.mu.Unlock(); sharedTargetResult(t, o, sharedRequest, ref, nil) }()
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
}

func TestPersonalOperationsBrowserRestoreRAMAndCSRF(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(fmt.Sprintf("durable=%v", durable), func(t *testing.T) {
			f := newPersonalOpsFixture(t, []personalStorageCollection{})
			if !durable {
				f.s = personalTestStore(t)
			}
			h := durableTestHandler(f.s, f.cache, f.now)
			first := personalHTTPRead(t, h)
			doc := PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}
			req := PersonalServicesMutation{StoreID: first.StoreID, Generation: first.Generation, Revision: first.Revision, CSRF: first.CSRF, Action: "restore", Document: &doc}
			w, saved := personalHTTPPost(t, h, req)
			if w.Code != 200 || !saved.Initialized {
				t.Fatal("restore", w.Code)
			}
			req.Generation = saved.Generation
			req.Revision = saved.Revision
			req.CSRF = saved.CSRF
			w, _ = personalHTTPPost(t, h, req)
			if w.Code != 409 {
				t.Fatal("restore into initialized")
			}
			bad := durableMutation(saved, "create")
			bad.CSRF = "fake"
			w, _ = personalHTTPPost(t, h, bad)
			if w.Code != 403 {
				t.Fatal("fake CSRF")
			}
			if durable {
				for _, stage := range []string{"recheck", "publish"} {
					t.Run(stage, func(t *testing.T) {
						fresh := personalHTTPRead(t, h)
						breq := durableMutation(fresh, "create")
						before := personalOpsBytes(t, f)
						br := personalOpsAdmit(t, personalHTTPRequest("POST", nil))
						f.s.storage.failpoint = func(at string) error {
							if at == stage {
								br.Method = "GET"
							}
							return nil
						}
						state, data, err := f.s.MutateBrowser(br, breq, f.cache, mcpTrusted(), 30*time.Second, f.now)
						personalOpsZero(t, state, data, err)
						f.s.storage.failpoint = nil
						if (stage == "publish") == bytes.Equal(before, personalOpsBytes(t, f)) {
							t.Fatal("browser late method semantics")
						}
					})
				}
			}
		})
	}
}

// A blocking browser body holds the real HTTP slot before decoding. MCP callers
// block at their first real clock checkpoint after acquiring the same channel.
type personalOpsBlockedBody struct {
	ready   chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *personalOpsBlockedBody) Read(p []byte) (int, error) {
	b.once.Do(func() { b.ready <- struct{}{}; <-b.release })
	return 0, io.EOF
}
func (b *personalOpsBlockedBody) Close() error { return nil }
func TestPersonalOperationsSharedAdmission(t *testing.T) {
	f := newPersonalOpsFixture(t, []personalStorageCollection{})
	r := f.request(t, false, "alice@example.com")
	req := personalOpsCreate(personalOpsState(t, f, r))
	// Seven existing slots + the actual eighth operation succeeds: no reacquisition.
	for i := 0; i < 7; i++ {
		f.s.requests <- struct{}{}
	}
	if _, _, err := f.read(r); err != nil {
		t.Fatal("read double acquired", err)
	}
	f.s.storage.failpoint = func(stage string) error {
		if stage == "filesync" && len(f.s.requests) != 8 {
			t.Fatal("mutation lacks exactly one slot")
		}
		return nil
	}
	saved, _, err := f.mutate(r, req)
	if err != nil {
		t.Fatal("mutation double acquired", err)
	}
	h := newPersonalServicesHandler(f.s, f.cache, mcpTrusted(), 30*time.Second)
	b := personalServicesResponse{PersonalServicesState: saved, CSRF: f.s.csrf("alice@example.com", time.Now())}
	w, _ := personalHTTPPost(t, h, durableMutation(b, "create"))
	if w.Code != 200 {
		t.Fatal("browser common body reacquired", w.Code)
	}
	f.s.storage.failpoint = nil
	for i := 0; i < 7; i++ {
		<-f.s.requests
	}
	// Four actual browser requests + four actual MCP operations occupy eight.
	ready := make(chan struct{}, 8)
	release := make(chan struct{})
	done := make(chan struct{}, 8)
	for i := 0; i < 4; i++ {
		br := personalHTTPRequest("POST", nil)
		br.Body = &personalOpsBlockedBody{ready: ready, release: release}
		go func() { defer func() { done <- struct{}{} }(); h.ServeHTTP(httptest.NewRecorder(), br) }()
	}
	req = personalOpsCreate(personalOpsState(t, f, r))
	for i := 0; i < 4; i++ {
		mr := f.request(t, false, "alice@example.com")
		go func() {
			defer func() { done <- struct{}{} }()
			once := sync.Once{}
			now := func() time.Time { once.Do(func() { ready <- struct{}{}; <-release }); return f.at }
			_, _, _ = f.s.MutateMCP(mr, req, f.config, f.cache, mcpTrusted(), 30*time.Second, now)
		}()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			t.Fatal("admission fixture stalled")
		}
	}
	if len(f.s.requests) != 8 {
		t.Fatal("not same eight slots")
	}
	state, data, err := f.read(r)
	personalOpsZero(t, state, data, err)
	state, data, err = f.mutate(r, req)
	personalOpsZero(t, state, data, err)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("GET", nil))
	if w.Code != 503 {
		t.Fatal("browser bypassed saturation")
	}
	close(release)
	for i := 0; i < 8; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("admission deadlock")
		}
	}
	if len(f.s.requests) != 0 {
		t.Fatal("admission leak")
	}
	personalOpsState(t, f, r)
}

func TestPersonalOperationsStructuralBoundary(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "personal_services_ops.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	commits := 0
	ast.Inspect(parsed, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Commit" {
				commits++
			}
		}
		return true
	})
	if commits != 1 {
		t.Fatal("duplicated durable writer body", commits)
	}
	typ := reflect.TypeOf(personalMCPMutation{})
	if typ.NumField() != 8 {
		t.Fatal("typed boundary fields")
	}
	for _, name := range []string{"Document", "Login", "Owner", "CSRF", "Authorized", "SkipCSRF"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatal("forbidden input", name)
		}
	}
	f := newPersonalOpsFixture(t, []personalStorageCollection{})
	r := f.request(t, false, "alice@example.com")
	req := personalOpsCreate(personalOpsState(t, f, r))
	before := personalOpsBytes(t, f)
	for _, kind := range []personalOperationTransport{0, 99} {
		state, data, err := f.s.mutateOperation(personalOperationRequest{transport: kind, request: r, mcp: f.config}, PersonalServicesMutation{Action: req.Action}, f.cache, mcpTrusted(), 30*time.Second, f.now)
		personalOpsZero(t, state, data, err)
	}
	if !bytes.Equal(before, personalOpsBytes(t, f)) {
		t.Fatal("unknown transport wrote")
	}
	if _, err := f.s.Mutate("alice@example.com", PersonalServicesMutation{}); !errors.Is(err, errPersonalUnavailable) {
		t.Fatal("durable identity escape")
	}
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
}
