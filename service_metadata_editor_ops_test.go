package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// Every positive/direct-core request is captured from the real middleware. Late
// mutations run synchronously in the writer's fixture seams, not in racing maps.
func sharedNPMRequest(t *testing.T, o *SharedIconOperations, method, login string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, o.origin+serviceMetadataEditorRoute+"3", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Tailscale-User-Login", login)
	if method == http.MethodPost {
		r.Header.Set("Origin", o.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	var authenticated *http.Request
	w := httptest.NewRecorder()
	IdentityMiddleware(o.trusted, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		authenticated = request
	})).ServeHTTP(w, r)
	if authenticated == nil {
		t.Fatalf("fixture not admitted by middleware: %d", w.Code)
	}
	return authenticated
}

func sharedNPMFixture(t *testing.T) (*SharedIconOperations, serviceMetadataEditRequest, time.Time) {
	t.Helper()
	e, cache, _ := editorFixture(t)
	o := editorTestOperations(e, cache)
	now := time.Now().Truncate(time.Second)
	o.now = func() time.Time { return now }
	data := *cache.Get()
	data.UpdatedAt = now
	cache.data.Store(&data)
	form, err := o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3)
	if err != nil {
		t.Fatal(err)
	}
	return o, serviceMetadataEditRequest{Action: "save", Revision: form.Revision, TargetRevision: form.TargetRevision,
		CSRF: form.CSRF, Name: "Shared Wiki", URL: "https://shared.example/path", Icon: "generic"}, now
}

func sharedNPMUnchanged(t *testing.T, e *ServiceMetadataEditor, view *serviceMetadataEditorView, disk []byte) {
	t.Helper()
	after, err := os.ReadFile(e.path)
	if err != nil || !bytes.Equal(after, disk) || e.view.Load() != view {
		t.Fatalf("denied operation changed disk/confirmed view: %v", err)
	}
	files, err := os.ReadDir(filepath.Dir(e.path))
	if err != nil || len(files) != 2 {
		t.Fatalf("temporary leaked or fixture files changed: %v %v", files, err)
	}
}

func sharedNPMResult(t *testing.T, o *SharedIconOperations, r *http.Request, id int, req serviceMetadataEditRequest, save bool, want error) {
	t.Helper()
	var err error
	if save {
		err = o.SaveBrowserNPM(r, id, req)
	} else {
		var form serviceMetadataEditForm
		form, err = o.GetBrowserNPM(r, id)
		if err != nil && !reflect.DeepEqual(form, serviceMetadataEditForm{}) {
			t.Fatal("failed GET disclosed partial editable state", form)
		}
	}
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestSharedNPMOperationsConstructorDetachedAndDormant(t *testing.T) {
	e, cache, _ := editorFixture(t)
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cfg := &Config{PortalEditMode: serviceEditShared, ControlPlane: controlPlaneHeadscale, StateNamespace: "source-one",
		TrustedProxyCIDR: trusted, PollInterval: time.Second, MCP: &MCPConfig{Origin: "http://mcp.example", Host: "mcp.example"}}
	e.config.Binding = cfg.metadataBinding()
	personal := &PersonalServicesStore{}
	personal.mu.Lock() // Construction and both NPM operations must not touch this lock.
	defer personal.mu.Unlock()
	old := e.view.Load()
	e.mu.Lock() // Construction neither locks the editor nor validates its view.
	e.view.Store(nil)
	o := newSharedIconOperations(cfg, e, cache, personal)
	fixedNow := cache.Get().UpdatedAt
	o.now = func() time.Time { return fixedNow }
	e.view.Store(old)
	e.mu.Unlock()
	cfg.PortalEditMode, cfg.ControlPlane, cfg.StateNamespace, cfg.PollInterval = serviceEditOff, controlPlaneTailscale, "source-two", 0
	cfg.TrustedProxyCIDR.IP[0] = 1
	cfg.TrustedProxyCIDR.Mask[0] = 0
	cfg.MCP.Origin, cfg.MCP.Host = "http://changed.example", "changed.example"
	if o.mode != serviceEditShared || o.binding != e.config.Binding || o.interval != time.Second ||
		!o.trusted.Contains(net.ParseIP("127.0.0.1")) || o.trusted.Contains(net.ParseIP("192.0.2.1")) ||
		o.mcp.Origin != "http://mcp.example" || o.mcp.Host != "mcp.example" || o.editor != e || o.cache != cache || o.personal != personal {
		t.Fatal("constructor did not capture detached immutable values")
	}
	r := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
	form, err := o.GetBrowserNPM(r, 3)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := o.browserNPMIdentity(r, 3)
	if err != nil {
		t.Fatal(err)
	}
	identity.Login = "changed@example.com"
	if IdentityFromContext(r.Context()).Login != "alice@example.com" {
		t.Fatal("identity not detached")
	}
	form.Icons[0] = "not-a-registry-icon"
	if serviceIconIDs[0] == form.Icons[0] {
		t.Fatal("icons not detached")
	}
	req := serviceMetadataEditRequest{Action: "save", Revision: form.Revision, TargetRevision: form.TargetRevision, CSRF: form.CSRF, Icon: "generic"}
	if err := o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, req); err != nil {
		t.Fatal(err)
	}
	if e.view.Load().metadata.document.Version != 3 {
		t.Fatal("ordinary save activated v4")
	}
}

func TestSharedNPMOperationsEntryCannotBypassAdapter(t *testing.T) {
	type fixture struct {
		o   *SharedIconOperations
		r   *http.Request
		req serviceMetadataEditRequest
		id  int
	}
	cases := []struct {
		name   string
		change func(*fixture)
		want   error
	}{
		{"nil-service", func(f *fixture) { f.o = nil }, errMetadataStale},
		{"nil-config-constructor", func(f *fixture) { f.o = newSharedIconOperations(nil, f.o.editor, f.o.cache, nil) }, errMetadataStale},
		{"nil-editor", func(f *fixture) { f.o.editor = nil }, errMetadataStale},
		{"nil-editor-config", func(f *fixture) { f.o.editor.config = nil }, errMetadataStale},
		{"nil-cache", func(f *fixture) { f.o.cache = nil }, errMetadataStale},
		{"nil-clock", func(f *fixture) { f.o.now = nil }, errMetadataStale},
		{"nil-trusted", func(f *fixture) { f.o.trusted = nil }, errMetadataStale},
		{"nil-request", func(f *fixture) { f.r = nil }, errMetadataForbidden},
		{"nil-url", func(f *fixture) { f.r.URL = nil }, errMetadataForbidden},
		{"absent-context-identity", func(f *fixture) { f.r = f.r.WithContext(context.Background()) }, errMetadataForbidden},
		{"canceled", func(f *fixture) {
			ctx, cancel := context.WithCancel(f.r.Context())
			cancel()
			f.r = f.r.WithContext(ctx)
		}, errMetadataStale},
		{"off", func(f *fixture) { f.o.mode = serviceEditOff }, errMetadataForbidden},
		{"browser", func(f *fixture) { f.o.mode = serviceEditBrowser }, errMetadataForbidden},
		{"unresolved-mode", func(f *fixture) { f.o.mode = "" }, errMetadataForbidden},
		{"zero-id", func(f *fixture) { f.id = 0 }, errMetadataForbidden},
		{"negative-id", func(f *fixture) { f.id = -3 }, errMetadataForbidden},
		{"untrusted", func(f *fixture) { f.r.RemoteAddr = "192.0.2.1:1" }, errMetadataForbidden},
		{"invalid-remote", func(f *fixture) { f.r.RemoteAddr = "not-an-ip" }, errMetadataForbidden},
		{"raw-login-mismatch", func(f *fixture) { f.r.Header.Set("Tailscale-User-Login", "bob@example.com") }, errMetadataForbidden},
		{"missing-raw-login", func(f *fixture) { f.r.Header.Del("Tailscale-User-Login") }, errMetadataForbidden},
		{"duplicate-login", func(f *fixture) { f.r.Header.Add("Tailscale-User-Login", "alice@example.com") }, errMetadataForbidden},
		{"case-alias-login", func(f *fixture) { f.r.Header["tailscale-user-login"] = []string{"alice@example.com"} }, errMetadataForbidden},
		{"duplicate-name", func(f *fixture) { f.r.Header["Tailscale-User-Name"] = []string{"Alice", "Alice"} }, errMetadataForbidden},
		{"case-alias-profile", func(f *fixture) {
			f.r.Header["Tailscale-User-Profile-Pic"] = []string{"pic"}
			f.r.Header["tailscale-user-profile-pic"] = []string{"pic"}
		}, errMetadataForbidden},
		{"wrong-method", func(f *fixture) { f.r.Method = http.MethodPut }, errMetadataForbidden},
		{"wrong-path", func(f *fixture) { f.r.URL.Path = serviceMetadataEditorRoute + "1" }, errMetadataForbidden},
		{"raw-path", func(f *fixture) { f.r.URL.RawPath = "/api/service-metadata/%33" }, errMetadataForbidden},
		{"query", func(f *fixture) { f.r.URL.RawQuery = "x=1" }, errMetadataForbidden},
		{"force-query", func(f *fixture) { f.r.URL.ForceQuery = true }, errMetadataForbidden},
		{"wrong-host", func(f *fixture) { f.r.Host = "other.example" }, errMetadataForbidden},
		{"non-editor", func(f *fixture) { delete(f.o.editor.config.Logins, "alice@example.com") }, errMetadataForbidden},
		{"changed-origin", func(f *fixture) { f.o.editor.config.Origin = "http://other.example" }, errMetadataForbidden},
		{"changed-config-host", func(f *fixture) { f.o.editor.config.Host = "other.example" }, errMetadataForbidden},
		{"noncanonical-origin", func(f *fixture) { f.o.origin += "/"; f.o.editor.config.Origin = f.o.origin }, errMetadataForbidden},
	}
	for _, save := range []bool{false, true} {
		method := http.MethodGet
		if save {
			method = http.MethodPost
		}
		for _, test := range cases {
			t.Run(method+"/"+test.name, func(t *testing.T) {
				o, req, _ := sharedNPMFixture(t)
				e := o.editor
				old := e.view.Load()
				f := fixture{o, sharedNPMRequest(t, o, method, "alice@example.com"), req, 3}
				test.change(&f)
				sharedNPMResult(t, f.o, f.r, f.id, f.req, save, test.want)
				sharedNPMUnchanged(t, e, old, old.bytes)
			})
		}
		for _, login := range []string{" alice@example.com", "alice@", "alice", "alice​@example.com", "Alice@example.com"} {
			t.Run(method+"/exact-login/"+login, func(t *testing.T) {
				o, req, _ := sharedNPMFixture(t)
				r := sharedNPMRequest(t, o, method, login)
				// Even an accidental allowlist entry cannot canonicalize a bad login.
				if login != "Alice@example.com" {
					o.editor.config.Logins[login] = true
				}
				old := o.editor.view.Load()
				sharedNPMResult(t, o, r, 3, req, save, errMetadataForbidden)
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			})
		}
	}
}

func TestSharedNPMOperationsOriginCSRFAndFieldParity(t *testing.T) {
	for _, origin := range []string{"absent", "exact", "wrong", "duplicate", "alias", "empty"} {
		t.Run("GET/"+origin, func(t *testing.T) {
			o, req, _ := sharedNPMFixture(t)
			r := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
			want := errMetadataForbidden
			switch origin {
			case "absent":
				want = nil
			case "exact":
				r.Header.Set("Origin", o.origin)
				want = nil
			case "wrong":
				r.Header.Set("Origin", "https://other.example")
			case "duplicate":
				r.Header["Origin"] = []string{o.origin, o.origin}
			case "alias":
				r.Header["Origin"] = []string{o.origin}
				r.Header["origin"] = []string{o.origin}
			case "empty":
				r.Header.Set("Origin", "")
			}
			sharedNPMResult(t, o, r, 3, req, false, want)
		})
	}
	for _, test := range []struct {
		name   string
		change func(*SharedIconOperations, *http.Request, *serviceMetadataEditRequest, time.Time)
		want   error
	}{
		{"exact", func(*SharedIconOperations, *http.Request, *serviceMetadataEditRequest, time.Time) {}, nil},
		{"missing-origin", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Del("Origin")
		}, errMetadataForbidden},
		{"duplicate-origin", func(o *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Add("Origin", o.origin)
		}, errMetadataForbidden},
		{"wrong-origin", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Origin", "null")
		}, errMetadataForbidden},
		{"site-absent", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Del("Sec-Fetch-Site")
		}, nil},
		{"site-none", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Sec-Fetch-Site", "none")
		}, nil},
		{"site-same-site", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
		}, errMetadataForbidden},
		{"site-cross-site", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}, errMetadataForbidden},
		{"site-duplicate", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Add("Sec-Fetch-Site", "same-origin")
		}, errMetadataForbidden},
		{"type-parameters", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Content-Type", "application/json; charset=utf-8")
		}, errMetadataForbidden},
		{"type-missing", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Del("Content-Type")
		}, errMetadataForbidden},
		{"type-duplicate", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Add("Content-Type", "application/json")
		}, errMetadataForbidden},
		{"csrf-inclusive", func(o *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, now time.Time) {
			req.CSRF = o.editor.csrf("alice@example.com", now.Add(-serviceMetadataCSRFDuration))
		}, nil},
		{"csrf-expired", func(o *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, now time.Time) {
			req.CSRF = o.editor.csrf("alice@example.com", now.Add(-serviceMetadataCSRFDuration-time.Second))
		}, errMetadataCSRF},
		{"csrf-future", func(o *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, now time.Time) {
			req.CSRF = o.editor.csrf("alice@example.com", now.Add(time.Second))
		}, errMetadataCSRF},
		{"csrf-other-login", func(o *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, now time.Time) {
			req.CSRF = o.editor.csrf("bob@example.com", now)
		}, errMetadataCSRF},
		{"csrf-forged", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.CSRF = "forged"
		}, errMetadataCSRF},
		{"action", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Action = "set-icon"
		}, errMetadataInvalidFields},
		{"reset-fields", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Action = "reset"
		}, errMetadataInvalidFields},
		{"missing-revision", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Revision = ""
		}, errMetadataInvalidFields},
		{"missing-target", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.TargetRevision = ""
		}, errMetadataInvalidFields},
		{"missing-csrf", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.CSRF = ""
		}, errMetadataInvalidFields},
		{"long-revision", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Revision = strings.Repeat("x", 257)
		}, errMetadataInvalidFields},
		{"long-target", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.TargetRevision = strings.Repeat("x", 257)
		}, errMetadataInvalidFields},
		{"long-csrf", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.CSRF = strings.Repeat("x", 257)
		}, errMetadataInvalidFields},
		{"invalid-name", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Name = " padded"
		}, errMetadataInvalidFields},
		{"invalid-url", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.URL = "javascript:alert(1)"
		}, errMetadataInvalidFields},
		{"invalid-icon", func(_ *SharedIconOperations, _ *http.Request, req *serviceMetadataEditRequest, _ time.Time) {
			req.Icon = "../icon"
		}, errMetadataInvalidFields},
		// MCP-only cookie/session/Fetch-Mode/Fetch-Dest rules are not browser rules.
		{"incumbent-browser-headers", func(_ *SharedIconOperations, r *http.Request, _ *serviceMetadataEditRequest, _ time.Time) {
			r.Header.Set("Cookie", "unused=1")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
		}, nil},
	} {
		t.Run("POST/"+test.name, func(t *testing.T) {
			o, req, now := sharedNPMFixture(t)
			r := sharedNPMRequest(t, o, http.MethodPost, "alice@example.com")
			old := o.editor.view.Load()
			test.change(o, r, &req, now)
			sharedNPMResult(t, o, r, 3, req, true, test.want)
			if test.want != nil {
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			}
		})
	}
}

func TestSharedNPMOperationsLateRequestAuthority(t *testing.T) {
	for _, stage := range []string{"recheck", "rename"} {
		for _, change := range []string{"remote", "host", "origin", "raw-login", "optional-name", "optional-profile", "type", "site", "method", "path", "raw-path", "query", "force-query", "cancel", "allowlist", "both-identities", "config-origin", "config-host", "binding", "provider", "policy", "target", "disabled", "nil-view", "csrf-expired"} {
			t.Run(stage+"/"+change, func(t *testing.T) {
				o, req, now := sharedNPMFixture(t)
				r := sharedNPMRequest(t, o, http.MethodPost, "alice@example.com")
				bob := sharedNPMRequest(t, o, http.MethodPost, "bob@example.com")
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				r = r.WithContext(ctx)
				old := o.editor.view.Load()
				want := errMetadataForbidden
				hit := false
				o.editor.failpoint = func(current string) error {
					if current != stage {
						return nil
					}
					hit = true
					switch change {
					case "remote":
						r.RemoteAddr = "192.0.2.1:1"
					case "host":
						r.Host = "other.example"
					case "origin":
						r.Header.Set("Origin", "https://other.example")
					case "raw-login":
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					case "optional-name":
						r.Header["Tailscale-User-Name"] = []string{"Alice", "Alice"}
					case "optional-profile":
						r.Header["Tailscale-User-Profile-Pic"] = []string{"pic", "pic"}
					case "type":
						r.Header.Set("Content-Type", "text/plain")
					case "site":
						r.Header.Set("Sec-Fetch-Site", "cross-site")
					case "method":
						r.Method = http.MethodGet
						r.Header.Del("Origin")
						req.CSRF = "not-reusable"
					case "path":
						r.URL.Path = serviceMetadataEditorRoute + "1"
					case "raw-path":
						r.URL.RawPath = "/api/service-metadata/%33"
					case "query":
						r.URL.RawQuery = "x=1"
					case "force-query":
						r.URL.ForceQuery = true
					case "cancel":
						cancel()
						want = errMetadataStale
					case "allowlist":
						delete(o.editor.config.Logins, "alice@example.com")
					case "both-identities":
						*r = *r.WithContext(bob.Context())
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					case "config-origin":
						o.editor.config.Origin = "http://other.example"
					case "config-host":
						o.editor.config.Host = "other.example"
					case "binding":
						o.editor.config.Binding.Namespace = "changed"
						want = errMetadataStale
					case "provider":
						data := *o.cache.Get()
						data.ControlPlane.Provider = controlPlaneTailscale
						o.cache.data.Store(&data)
						want = errMetadataStale
					case "policy":
						data := *o.cache.Get()
						data.Policy = &Policy{}
						o.cache.data.Store(&data)
						want = errMetadataNotVisible
					case "target":
						data := *o.cache.Get()
						data.ProxyHosts = append([]ProxyHost(nil), data.ProxyHosts...)
						data.ProxyHosts[2].ForwardPort++
						o.cache.data.Store(&data)
						want = errMetadataConflict
					case "disabled":
						o.editor.disabled = true
						want = errMetadataUncertain
					case "nil-view":
						o.editor.view.Store(nil)
						want = errMetadataStale
					case "csrf-expired":
						o.now = func() time.Time { return now.Add(serviceMetadataCSRFDuration + time.Second) }
						want = errMetadataCSRF
					}
					return nil
				}
				err := o.SaveBrowserNPM(r, 3, req)
				if !hit || !errors.Is(err, want) {
					t.Fatalf("hit=%v err=%v want=%v", hit, err, want)
				}
				if change == "nil-view" {
					if o.editor.view.Load() != nil {
						t.Fatal("writer published after nil view")
					}
					o.editor.view.Store(old) // Restore only this deliberate fixture mutation.
				}
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			})
		}
	}
}

func TestSharedNPMOperationsLockWait(t *testing.T) {
	for _, test := range []struct {
		name       string
		save, csrf bool
	}{{"GET-stale", false, false}, {"POST-stale", true, false}, {"POST-expired-CSRF", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			o, req, now := sharedNPMFixture(t)
			method := http.MethodGet
			if test.save {
				method = http.MethodPost
			}
			r := sharedNPMRequest(t, o, method, "alice@example.com")
			old := o.editor.view.Load()
			if test.csrf {
				req.CSRF = o.editor.csrf("alice@example.com", now.Add(-serviceMetadataCSRFDuration))
			}
			var mu sync.Mutex
			clock := now
			entered := make(chan struct{})
			var once sync.Once
			o.now = func() time.Time {
				mu.Lock()
				value := clock
				mu.Unlock()
				once.Do(func() { close(entered) })
				return value
			}
			o.editor.mu.Lock()
			done := make(chan error, 1)
			go func() {
				if test.save {
					done <- o.SaveBrowserNPM(r, 3, req)
					return
				}
				form, err := o.GetBrowserNPM(r, 3)
				if !reflect.DeepEqual(form, serviceMetadataEditForm{}) {
					done <- errors.New("stale form returned")
					return
				}
				done <- err
			}()
			<-entered // Initial clock captured while the editor mutex is still held.
			mu.Lock()
			if test.csrf {
				clock = now.Add(time.Second)
			} else {
				clock = now.Add(91 * time.Second)
			}
			mu.Unlock()
			o.editor.mu.Unlock()
			want := errMetadataStale
			if test.csrf {
				want = errMetadataCSRF
			}
			if err := <-done; !errors.Is(err, want) {
				t.Fatalf("after lock wait got %v want %v", err, want)
			}
			sharedNPMUnchanged(t, o.editor, old, old.bytes)
		})
	}
}

func TestSharedNPMOperationsSnapshotAndWriterPrerequisites(t *testing.T) {
	for _, save := range []bool{false, true} {
		method := http.MethodGet
		if save {
			method = http.MethodPost
		}
		for _, change := range []string{"nil-snapshot", "zero-time", "future-time", "stale", "nil-policy", "zero-interval", "negative-interval", "overflow-interval", "blank-provider", "unknown-provider", "mismatched-provider", "invalid-binding-provider", "invalid-namespace", "changed-writer-binding", "nil-view", "nil-metadata", "invalid-model", "disabled", "v4-namespace-mismatch"} {
			t.Run(method+"/"+change, func(t *testing.T) {
				o, req, now := sharedNPMFixture(t)
				r := sharedNPMRequest(t, o, method, "alice@example.com")
				data := *o.cache.Get()
				want := errMetadataStale
				switch change {
				case "zero-time":
					data.UpdatedAt = time.Time{}
				case "future-time":
					data.UpdatedAt = now.Add(time.Nanosecond)
				case "stale":
					data.UpdatedAt = now.Add(-90*time.Second - time.Nanosecond)
				case "nil-policy":
					data.Policy = nil
				case "zero-interval":
					o.interval = 0
				case "negative-interval":
					o.interval = -time.Second
				case "overflow-interval":
					o.interval = time.Duration(1<<63-1)/3 + 1
				case "blank-provider":
					data.ControlPlane.Provider = ""
				case "unknown-provider":
					data.ControlPlane.Provider = "unknown"
				case "mismatched-provider":
					data.ControlPlane.Provider = controlPlaneTailscale
				case "invalid-binding-provider":
					o.binding.ControlPlane = "unknown"
					o.editor.config.Binding = o.binding
				case "invalid-namespace":
					o.binding.Namespace = "Invalid"
					o.editor.config.Binding = o.binding
				case "changed-writer-binding":
					o.editor.config.Binding.Namespace = "changed"
				case "nil-view":
					o.editor.view.Store(nil)
				case "nil-metadata":
					view := *o.editor.view.Load()
					view.metadata = nil
					o.editor.view.Store(&view)
				case "invalid-model":
					view := *o.editor.view.Load()
					m := *view.metadata
					m.document.Version = 0
					view.metadata = &m
					o.editor.view.Store(&view)
				case "disabled":
					o.editor.disabled = true
					want = errMetadataUncertain
				case "v4-namespace-mismatch":
					// Syntax-seeded view below startup, never a production v4 loader.
					view := *o.editor.view.Load()
					view.metadata = metadataV4(t)
					o.editor.view.Store(&view)
					o.binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale, Namespace: "different"}
					o.editor.config.Binding = o.binding
					data.ControlPlane.Provider = controlPlaneTailscale
				}
				o.cache.data.Store(&data)
				if change == "nil-snapshot" {
					o.cache.data.Store(nil)
				}
				view := o.editor.view.Load()
				disk, err := os.ReadFile(o.editor.path)
				if err != nil {
					t.Fatal(err)
				}
				sharedNPMResult(t, o, r, 3, req, save, want)
				sharedNPMUnchanged(t, o.editor, view, disk)
			})
		}
	}
	for _, provider := range []controlPlaneProvider{controlPlaneHeadscale, controlPlaneTailscale} {
		for _, namespace := range []string{"", "legacy-binding"} {
			t.Run(string(provider)+"/"+namespace, func(t *testing.T) {
				e, cache, _ := editorFixture(t)
				e.config.Binding = serviceMetadataBinding{ControlPlane: provider, Namespace: namespace}
				data := *cache.Get()
				data.ControlPlane.Provider = provider
				cache.data.Store(&data)
				o := editorTestOperations(e, cache)
				form, err := o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3)
				if err != nil {
					t.Fatal(err)
				}
				req := serviceMetadataEditRequest{Action: "reset", Revision: form.Revision, TargetRevision: form.TargetRevision, CSRF: form.CSRF}
				if err := o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, req); err != nil {
					t.Fatal(err)
				}
				if e.view.Load().metadata.document.Version != 3 {
					t.Fatal("legacy namespace promoted v4")
				}
			})
		}
	}
}

func TestSharedNPMOperationsReadRechecksAfterDisk(t *testing.T) {
	for _, change := range []string{"stale", "provider", "target", "visibility", "request", "binding", "disabled", "nil-view"} {
		t.Run(change, func(t *testing.T) {
			o, req, now := sharedNPMFixture(t)
			r := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
			old := o.editor.view.Load()
			calls := 0
			want := errMetadataStale
			o.now = func() time.Time {
				calls++
				// Entry, post-lock, selection, then the post-disk read guard.
				if calls == 4 {
					data := *o.cache.Get()
					switch change {
					case "stale":
						data.UpdatedAt = now.Add(-91 * time.Second)
					case "provider":
						data.ControlPlane.Provider = controlPlaneTailscale
					case "target":
						data.ProxyHosts = append([]ProxyHost(nil), data.ProxyHosts...)
						data.ProxyHosts[2].DomainNames = []string{"changed.example"}
						want = errMetadataConflict
					case "visibility":
						data.Policy = &Policy{}
						want = errMetadataNotVisible
					case "request":
						r.RemoteAddr = "192.0.2.1:1"
						want = errMetadataForbidden
					case "binding":
						o.editor.config.Binding.Namespace = "changed"
					case "disabled":
						o.editor.disabled = true
						want = errMetadataUncertain
					case "nil-view":
						o.editor.view.Store(nil)
					}
					o.cache.data.Store(&data)
				}
				return now
			}
			form, err := o.GetBrowserNPM(r, 3)
			if calls < 4 || !errors.Is(err, want) || !reflect.DeepEqual(form, serviceMetadataEditForm{}) {
				t.Fatalf("post-disk check calls=%d err=%v want=%v form=%v", calls, err, want, form)
			}
			if change == "nil-view" {
				o.editor.view.Store(old)
			}
			sharedNPMUnchanged(t, o.editor, old, old.bytes)
			_ = req
		})
	}
	// The clock, not just cache data, must be sampled again after disk inspection.
	t.Run("clock", func(t *testing.T) {
		o, req, now := sharedNPMFixture(t)
		r := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
		calls := 0
		o.now = func() time.Time {
			calls++
			if calls >= 4 {
				return now.Add(91 * time.Second)
			}
			return now
		}
		sharedNPMResult(t, o, r, 3, req, false, errMetadataStale)
	})
}

func TestSharedNPMOperationsVisibilityBeforeCAS(t *testing.T) {
	for _, change := range []string{"missing", "invisible", "domainless", "unrenderable", "disabled", "duplicate", "backend", "domain", "identical-poll", "route-state", "unrelated"} {
		t.Run(change, func(t *testing.T) {
			o, req, now := sharedNPMFixture(t)
			r := sharedNPMRequest(t, o, http.MethodPost, "alice@example.com")
			old := o.editor.view.Load()
			data := *o.cache.Get()
			data.ProxyHosts = append([]ProxyHost(nil), data.ProxyHosts...)
			want := errMetadataNotVisible
			switch change {
			case "missing":
				data.ProxyHosts = data.ProxyHosts[:2]
			case "invisible":
				data.Policy = &Policy{}
			case "domainless":
				data.ProxyHosts[2].DomainNames = nil
			case "unrenderable":
				data.ProxyHosts[2].DomainNames = []string{""}
			case "disabled":
				data.ProxyHosts[2].Enabled = false
			case "duplicate":
				data.ProxyHosts = append(data.ProxyHosts, data.ProxyHosts[2])
			case "backend":
				data.ProxyHosts[2].ForwardPort++
				want = errMetadataConflict
			case "domain":
				data.ProxyHosts[2].DomainNames = []string{"changed.example"}
				want = errMetadataConflict
			case "identical-poll":
				data.UpdatedAt = now
				want = nil
			case "route-state":
				data.ProxyHosts[2].Meta.NginxOnline = !data.ProxyHosts[2].Meta.NginxOnline
				want = nil
			case "unrelated":
				data.ProxyHosts[0].ForwardPort++
				want = nil
			}
			o.cache.data.Store(&data)
			if want == errMetadataNotVisible {
				req.Revision, req.TargetRevision = "incorrect-document-CAS", "incorrect-target-CAS"
			}
			sharedNPMResult(t, o, r, 3, req, true, want)
			if want != nil {
				sharedNPMUnchanged(t, o.editor, old, old.bytes)
			}
		})
	}
}

func TestSharedNPMOperationsLegacyFullDialogAndDiskConflict(t *testing.T) {
	for _, source := range []string{
		`{"version":1,"services":[{"proxy_host_id":3,"name":"Old Wiki"},{"proxy_host_id":99,"name":null,"url":"https://unseen.example"}]}`,
		editorTestDocument,
		`{"version":3,"services":[{"proxy_host_id":3,"name":"Old Wiki","url":null,"icon":"grafana","category":"Tools","order":0},{"proxy_host_id":99,"name":null,"url":"https://unseen.example"}]}`,
	} {
		for _, action := range []string{"save", "blank", "reset"} {
			t.Run(source[:12]+"/"+action, func(t *testing.T) {
				e, err := newServiceMetadataEditor(editorTestFile(t, source), editorTestConfig())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = e.Close() })
				data := standardTestData()
				data.ControlPlane.Provider = controlPlaneHeadscale
				o := editorTestOperations(e, newTestCache(data))
				old := e.view.Load()
				form, err := o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3)
				if err != nil {
					t.Fatal(err)
				}
				req := serviceMetadataEditRequest{Action: "save", Revision: form.Revision, TargetRevision: form.TargetRevision, CSRF: form.CSRF}
				if action == "save" {
					req.Name, req.URL, req.Icon = "New Wiki", "https://new.example/path?q=1#part", "generic"
				}
				if action == "reset" {
					req.Action = "reset"
				}
				if err := o.SaveBrowserNPM(sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, req); err != nil {
					t.Fatal(err)
				}
				want, err := mutateServiceMetadata(old.metadata.document, 3, req.Name, req.URL, req.Icon, action == "reset")
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := serializeServiceMetadataDocument(want)
				if err != nil {
					t.Fatal(err)
				}
				current := e.view.Load()
				disk, err := os.ReadFile(e.path)
				if err != nil || current.metadata.document.Version != 3 || !bytes.Equal(current.bytes, encoded) || !bytes.Equal(disk, encoded) || !reflect.DeepEqual(current.metadata.Overrides[99], old.metadata.Overrides[99]) {
					t.Fatal("legacy full-dialog semantics changed", err)
				}
				// Disk mismatch remains conflict for both selected GET and save.
				form, err = o.GetBrowserNPM(sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3)
				if err != nil {
					t.Fatal(err)
				}
				req.Revision, req.TargetRevision, req.CSRF = form.Revision, form.TargetRevision, form.CSRF
				drift := append(append([]byte(nil), disk...), '\n')
				if err := os.WriteFile(e.path, drift, 0600); err != nil {
					t.Fatal(err)
				}
				sharedNPMResult(t, o, sharedNPMRequest(t, o, http.MethodGet, "alice@example.com"), 3, req, false, errMetadataConflict)
				sharedNPMResult(t, o, sharedNPMRequest(t, o, http.MethodPost, "alice@example.com"), 3, req, true, errMetadataConflict)
				sharedNPMUnchanged(t, e, current, drift)
			})
		}
	}
}

func TestSharedNPMOperationsFreshnessBoundaryAndRemoteForms(t *testing.T) {
	for _, remote := range []string{"127.0.0.1", "127.0.0.1:1234", "[::1%fixture]:1234", "::1%fixture"} {
		t.Run(remote, func(t *testing.T) {
			o, req, now := sharedNPMFixture(t)
			r := sharedNPMRequest(t, o, http.MethodGet, "alice@example.com")
			if strings.Contains(remote, "::1") {
				_, o.trusted, _ = net.ParseCIDR("::1/128")
			}
			r.RemoteAddr = remote
			data := *o.cache.Get()
			data.UpdatedAt = now.Add(-3 * o.interval)
			o.cache.data.Store(&data)
			sharedNPMResult(t, o, r, 3, req, false, nil)
			if _, err := o.checkBrowserNPMRead(r, 3, "", now); !errors.Is(err, errMetadataForbidden) {
				t.Fatal("empty pinned login accepted")
			}
		})
	}
}

func TestSharedNPMOperationsNoCapabilitiesAdded(t *testing.T) {
	e, cache, _ := editorFixture(t)
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser} {
		o := editorTestOperations(e, cache)
		o.mode = mode
		mux := http.NewServeMux()
		mountServiceMetadataEditor(mux, o)
		for _, path := range []string{serviceMetadataEditorRoute + "3", "/api/shared-targets", "/api/shared-icons"} {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s gained route %s", mode, path)
			}
		}
	}
	if !serviceMetadataV4RuntimeEnabled {
		t.Fatal("S5 source activation missing")
	}
	// Real enabled MCP discovery/dispatch, not a synthetic capabilities list.
	cfg := &Config{ControlPlane: controlPlaneHeadscale, PortalEditMode: serviceEditShared, TrustedProxyCIDR: mcpTrusted(), PollInterval: 30 * time.Second, MCP: mcpTestConfig()}
	mux, err := newServerMux(cfg, cache, nil, e, nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, true))
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Result.Tools) != 1 || response.Result.Tools[0].Name != "icons_list" {
		t.Fatal("new capabilities advertised", w.Body.String())
	}
	for _, name := range []string{"shared_targets_list", "shared_target_get", "shared_icon_set", "shared_icon_reset", "personal_read", "personal_create", "personal_update", "personal_delete"} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{}}})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, mcpTestRequest(string(body), true))
		if name == "personal_read" {
			if !strings.Contains(w.Body.String(), `"isError":true`) || !strings.Contains(w.Body.String(), `"error":"unavailable"`) {
				t.Fatal("unconfigured personal accepted", w.Body.String())
			}
		} else if !strings.Contains(w.Body.String(), `"code":-32602`) {
			t.Fatalf("invalid tool arguments %s accepted: %s", name, w.Body.String())
		}
	}
}
