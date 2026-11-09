package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serverMuxFixture(t *testing.T, mode serviceEditMode, personalEnabled bool) (*http.ServeMux, *Cache) {
	t.Helper()
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cfg := &Config{TrustedProxyCIDR: trusted, PollInterval: 30 * time.Second, PortalLogoDefaultVisible: true, PortalEditMode: mode}
	cache := newTestCache(standardTestData())
	var editor *ServiceMetadataEditor
	if mode == serviceEditShared {
		var err error
		editor, err = newServiceMetadataEditor(editorTestFile(t, editorTestDocument), editorTestConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = editor.Close() })
	}
	var personal *PersonalServicesStore
	if personalEnabled {
		personal = personalTestStore(t)
	}
	// Use the same constructor as runServer, not a separate test mux.
	mux, err := newServerMux(cfg, cache, nil, editor, personal)
	if err != nil {
		t.Fatal(err)
	}
	return mux, cache
}

func TestServerMuxRouteCombinations(t *testing.T) {
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser, serviceEditShared} {
		for _, enabled := range []bool{false, true} {
			name := string(mode) + "/personal-disabled"
			if enabled {
				name = string(mode) + "/personal-enabled"
			}
			t.Run(name, func(t *testing.T) {
				mux, cache := serverMuxFixture(t, mode, enabled)
				for _, path := range []string{"/", "/portal"} {
					for _, method := range []string{http.MethodGet, http.MethodHead} {
						r := personalHTTPRequest(method, nil)
						r.URL.Path = path
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, r)
						if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
							t.Fatalf("portal %s %s: %d %s", method, path, w.Code, w.Body.String())
						}
						if method == http.MethodGet && !strings.Contains(w.Body.String(), `data-service-id="3"`) {
							t.Fatal("authorized NPM service missing")
						}
					}
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
					if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, HEAD" {
						t.Fatalf("portal POST %s: %d allow=%q", path, w.Code, w.Header().Get("Allow"))
					}
				}
				for _, path := range []string{"/unknown", "/portal/extra", personalServicesRoute + "/extra"} {
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, personalHTTPRequestAt(http.MethodGet, path))
					if w.Code != http.StatusNotFound {
						t.Fatalf("unknown path %s: %d", path, w.Code)
					}
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, path := range []string{"/healthz", "/static/htmx.min.js"} {
						w := httptest.NewRecorder()
						// Health and static routes do not require browser identity.
						mux.ServeHTTP(w, httptest.NewRequest(method, path, nil))
						if w.Code != http.StatusOK {
							t.Fatalf("%s %s: %d", method, path, w.Code)
						}
					}
				}
				if enabled {
					state := personalHTTPRead(t, mux)
					mutation := personalTestMutation(state.PersonalServicesState, "create")
					mutation.CSRF = state.CSRF
					w, saved := personalHTTPPost(t, mux, mutation)
					if w.Code != http.StatusOK || saved.Revision != "1" || !saved.Initialized || len(saved.Document.Entries) != 1 {
						t.Fatalf("personal POST: %d %s", w.Code, w.Body.String())
					}
					if got := personalHTTPRead(t, mux); got.Revision != saved.Revision || got.Document.Entries[0] != saved.Document.Entries[0] {
						t.Fatal("personal GET did not return acknowledged POST")
					}
					for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, personalHTTPRequest(method, nil))
						if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, POST" || !strings.Contains(w.Body.String(), `"error":"method_not_allowed"`) {
							t.Fatalf("personal %s: %d %s", method, w.Code, w.Body.String())
						}
					}
				} else {
					for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead, http.MethodPut} {
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, personalHTTPRequest(method, nil))
						if w.Code != http.StatusNotFound {
							t.Fatalf("disabled personal %s: %d", method, w.Code)
						}
					}
				}
				if mode == serviceEditShared {
					form := editForm(t, mux, "3", "alice@example.com")
					w := editHTTPRequest(mux, http.MethodPost, "3", "alice@example.com", editBody(form, "save"), nil)
					if w.Code != http.StatusOK {
						t.Fatalf("shared POST: %d %s", w.Code, w.Body.String())
					}
					if got := editForm(t, mux, "3", "alice@example.com"); got.Fields.Name != "Shared Wiki" {
						t.Fatal("shared GET did not return acknowledged POST")
					}
					for _, method := range []string{http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions} {
						w := editHTTPRequest(mux, method, "3", "alice@example.com", "", nil)
						if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != "GET, POST" {
							t.Fatalf("shared %s: %d", method, w.Code)
						}
					}
				} else {
					for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
						w := editHTTPRequest(mux, method, "3", "alice@example.com", "", nil)
						if w.Code != http.StatusNotFound {
							t.Fatalf("disabled shared %s: %d", method, w.Code)
						}
					}
				}
				cache.data.Store(nil)
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("cold health: %d", w.Code)
				}
			})
		}
	}
}

func personalHTTPRequestAt(method, path string) *http.Request {
	r := personalHTTPRequest(method, nil)
	r.URL.Path = path
	return r
}

func TestServerMuxPersonalGuards(t *testing.T) {
	mux, _ := serverMuxFixture(t, serviceEditOff, true)
	state := personalHTTPRead(t, mux)
	mutation := personalTestMutation(state.PersonalServicesState, "create")
	mutation.CSRF = state.CSRF
	body, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*http.Request)
		code   int
	}{
		{"untrusted", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1234" }, 403},
		{"missing identity", func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }, 401},
		{"duplicate identity", func(r *http.Request) { r.Header.Add("Tailscale-User-Login", "bob@example.com") }, 403},
		{"wrong host", func(r *http.Request) { r.Host = "evil.example" }, 403},
		{"wrong origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, 403},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"query", func(r *http.Request) { r.URL.RawQuery = "ignored=1" }, 404},
		{"escaped path", func(r *http.Request) { r.URL.RawPath = "/api/%70ersonal-services" }, 404},
		{"content type", func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415},
		{"invalid JSON", func(r *http.Request) { r.Body = http.NoBody }, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := personalHTTPRequest(http.MethodPost, body)
			test.change(r)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != test.code || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
				t.Fatalf("code=%d want=%d body=%s", w.Code, test.code, w.Body.String())
			}
		})
	}
	for _, path := range []string{"/", "/portal"} {
		for _, test := range []struct {
			name   string
			change func(*http.Request)
			code   int
		}{
			{"untrusted", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1234" }, 403},
			{"missing identity", func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }, 401},
		} {
			r := personalHTTPRequestAt(http.MethodGet, path)
			test.change(r)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != test.code {
				t.Fatalf("portal %s %s: %d", path, test.name, w.Code)
			}
		}
	}
}
