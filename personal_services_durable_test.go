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
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func durableTestConfig(t *testing.T, records []personalStorageCollection) *PersonalServicesConfig {
	t.Helper()
	path := storageTestPath(t)
	snapshot := personalStorageSnapshot{Version: 1, Namespace: "fixture-1", StoreID: strings.Repeat("a", 32), Collections: records}
	data, err := serializePersonalSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return &PersonalServicesConfig{Origin: "http://portal.example:8081", Host: "portal.example:8081", File: path, Namespace: "fixture-1"}
}
func durableTestStore(t *testing.T, cfg *PersonalServicesConfig) *PersonalServicesStore {
	t.Helper()
	s, err := NewPersonalServicesStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.collections != nil {
		t.Fatal("durable mode allocated a second RAM catalog")
	}
	return s
}
func durableTestCache(t *testing.T) *Cache {
	t.Helper()
	_, data, _, _ := personalProjectionFixture(t)
	data.UpdatedAt = time.Now().Truncate(time.Second)
	data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: true}, "bob@example.com": {ID: "bob-id", DirectMember: true}}
	cache := &Cache{}
	cache.data.Store(data)
	return cache
}
func durableTestHandler(s *PersonalServicesStore, cache *Cache, now func() time.Time) http.Handler {
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	if now == nil {
		return newPersonalServicesHandler(s, cache, trusted, 30*time.Second)
	}
	return IdentityMiddleware(trusted, &personalServicesHandler{store: s, cache: cache, interval: 30 * time.Second, now: now, trusted: trusted})
}
func durableMutation(state personalServicesResponse, action string) PersonalServicesMutation {
	r := personalTestMutation(state.PersonalServicesState, action)
	r.StoreID, r.CSRF = state.StoreID, state.CSRF
	return r
}
func durableRecord(login, id, revision string, entries []PersonalService) personalStorageCollection {
	return personalStorageCollection{Login: login, UserID: id, Revision: revision, Document: PersonalServicesDocument{Version: 1, Entries: entries}}
}
func durableRender(s *PersonalServicesStore, cache *Cache, login string) *httptest.ResponseRecorder {
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	portal := NewPortalHandler(cache)
	portal.personal, portal.pollInterval = s, 30*time.Second
	r := personalHTTPRequestAt("GET", "/portal")
	r.Header.Set("Tailscale-User-Login", login)
	w := httptest.NewRecorder()
	IdentityMiddleware(trusted, portal).ServeHTTP(w, r)
	return w
}

func TestPersonalDurableRestartCASAndEmpty(t *testing.T) {
	cfg := durableTestConfig(t, []personalStorageCollection{})
	s := durableTestStore(t, cfg)
	cache := durableTestCache(t)
	h := durableTestHandler(s, cache, nil)
	first := personalHTTPRead(t, h)
	if !first.Durable || first.StoreID != strings.Repeat("a", 32) || first.Initialized || first.Revision != "0" {
		t.Fatal("absent durable wire")
	}
	create := durableMutation(first, "create")
	w, saved := personalHTTPPost(t, h, create)
	if w.Code != 200 || saved.Revision != "1" || saved.Projection[0].Status != "active" {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "alice-id") || strings.Contains(w.Body.String(), "fixture-1") || strings.Contains(w.Body.String(), "user_id") {
		t.Fatal("ownership binding disclosed")
	}
	if _, err := s.Read("alice@example.com"); !errors.Is(err, errPersonalUnavailable) {
		t.Fatal("unguarded durable read")
	}
	if _, err := s.Mutate("alice@example.com", create); !errors.Is(err, errPersonalUnavailable) {
		t.Fatal("unguarded durable mutation")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := durableTestStore(t, cfg)
	h = durableTestHandler(restarted, cache, nil)
	loaded := personalHTTPRead(t, h)
	if loaded.Generation == saved.Generation || loaded.StoreID != saved.StoreID || loaded.Revision != saved.Revision || !reflect.DeepEqual(loaded.Document, saved.Document) {
		t.Fatal("restart did not automatically load disk")
	}
	for _, change := range []func(*PersonalServicesMutation){
		func(r *PersonalServicesMutation) { r.Generation = saved.Generation },
		func(r *PersonalServicesMutation) { r.StoreID = "" },
		func(r *PersonalServicesMutation) { r.StoreID = strings.Repeat("b", 32) },
		func(r *PersonalServicesMutation) { r.Revision = "0" },
	} {
		r := durableMutation(loaded, "create")
		change(&r)
		w, _ := personalHTTPPost(t, h, r)
		if w.Code != 409 {
			t.Fatal("CAS", w.Code, w.Body.String())
		}
	}
	remove := PersonalServicesMutation{Action: "delete", StoreID: loaded.StoreID, Generation: loaded.Generation, Revision: loaded.Revision, CSRF: loaded.CSRF, ID: loaded.Document.Entries[0].ID}
	w, empty := personalHTTPPost(t, h, remove)
	if w.Code != 200 || !empty.Initialized || len(empty.Document.Entries) != 0 {
		t.Fatal("delete", w.Code)
	}
	_ = restarted.Close()
	final := durableTestStore(t, cfg)
	h = durableTestHandler(final, cache, nil)
	empty = personalHTTPRead(t, h)
	if !empty.Initialized || empty.Revision != "2" || len(empty.Document.Entries) != 0 {
		t.Fatal("initialized empty lost at restart")
	}
	restore := PersonalServicesMutation{Action: "restore", StoreID: empty.StoreID, Generation: empty.Generation, Revision: empty.Revision, CSRF: empty.CSRF, Document: &saved.Document}
	w, _ = personalHTTPPost(t, h, restore)
	if w.Code != 409 {
		t.Fatal("initialized-empty resurrected")
	}
	_ = final.Close()
	if _, err := final.ReadForSnapshot("alice@example.com", cache.Get(), 30*time.Second, time.Now); err == nil {
		t.Fatal("closed store readable")
	}
}

func TestPersonalDurableOwnershipEveryUse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*CacheData)
		login  string
	}{
		{"removed", func(d *CacheData) { delete(d.TailscaleUsersByLogin, "alice@example.com") }, ""},
		{"shared", func(d *CacheData) {
			d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
		}, ""},
		{"reassigned", func(d *CacheData) {
			d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "new-id", DirectMember: true}
		}, ""},
		{"renamed", func(d *CacheData) {
			delete(d.TailscaleUsersByLogin, "alice@example.com")
			d.TailscaleUsersByLogin["new@example.com"] = TailscaleUserIdentity{ID: "alice-id", DirectMember: true}
		}, "new@example.com"},
		{"invalid ID", func(d *CacheData) {
			d.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: " padded", DirectMember: true}
		}, ""},
		{"no map", func(d *CacheData) { d.TailscaleUsersByLogin = nil }, ""},
		{"stale", func(d *CacheData) { d.UpdatedAt = time.Now().Add(-91 * time.Second) }, ""},
		{"future", func(d *CacheData) { d.UpdatedAt = time.Now().Add(time.Minute) }, ""},
		{"zero time", func(d *CacheData) { d.UpdatedAt = time.Time{} }, ""},
		{"no policy", func(d *CacheData) { d.Policy = nil }, ""},
		{"wrong provider", func(d *CacheData) { d.ControlPlane.Provider = controlPlaneHeadscale }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", []PersonalService{personalTestEntry(0)})}))
			cache := durableTestCache(t)
			h := durableTestHandler(s, cache, nil)
			state := personalHTTPRead(t, h)
			data := cache.Get()
			tc.change(data)
			cache.data.Store(data)
			login := tc.login
			if login == "" {
				login = "alice@example.com"
			}
			if result, err := s.ReadForSnapshot(login, data, 30*time.Second, time.Now); err == nil || result.Document.Entries != nil {
				t.Fatal("private read escaped owner guard")
			}
			for _, action := range []string{"GET", "create", "update", "delete", "restore"} {
				var body []byte
				method := "GET"
				if action != "GET" {
					method = "POST"
					r := durableMutation(state, action)
					if action == "update" {
						r.ID = state.Document.Entries[0].ID
					}
					if action == "delete" {
						r.ID = state.Document.Entries[0].ID
						r.Name = ""
						r.URL = ""
					}
					if action == "restore" {
						r.Name = ""
						r.URL = ""
						r.Document = &state.Document
					}
					r.CSRF = s.csrf(login, time.Now())
					body, _ = json.Marshal(r)
				}
				req := personalHTTPRequest(method, body)
				req.Header.Set("Tailscale-User-Login", login)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if w.Code != 503 || strings.Contains(w.Body.String(), "document") || strings.Contains(w.Body.String(), "alice-id") {
					t.Fatal(action, w.Code, w.Body.String())
				}
			}
			page := durableRender(s, cache, login).Body.String()
			if strings.Contains(page, `data-personal-enabled="true"`) || strings.Contains(page, `data-personal-service=`) || strings.Contains(page, "Tool 0") {
				t.Fatal("renderer escaped owner guard")
			}
		})
	}
}

func TestPersonalDurableFreshnessAndInactiveManagement(t *testing.T) {
	s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{}))
	cache := durableTestCache(t)
	data := cache.Get()
	at := data.UpdatedAt
	for _, tc := range []struct {
		name     string
		data     *CacheData
		interval time.Duration
		now      time.Time
		ok       bool
	}{
		{"boundary", data, 30 * time.Second, at.Add(90 * time.Second), true},
		{"beyond", data, 30 * time.Second, at.Add(90*time.Second + time.Nanosecond), false},
		{"future", data, 30 * time.Second, at.Add(-time.Nanosecond), false},
		{"cold", nil, 30 * time.Second, at, false},
		{"zero interval", data, 0, at, false},
		{"negative interval", data, -time.Second, at, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := s.ReadForSnapshot("alice@example.com", tc.data, tc.interval, func() time.Time { return tc.now })
			if (err == nil) != tc.ok || err != nil && result.Document.Entries != nil {
				t.Fatal(err)
			}
		})
	}
	data.Nodes = nil
	data.Policy.Grants = nil
	data.GrantRoleSelectorsByLogin = nil
	h := durableTestHandler(s, cache, nil)
	first := personalHTTPRead(t, h)
	w, saved := personalHTTPPost(t, h, durableMutation(first, "create"))
	if w.Code != 200 || saved.Projection[0].Status != "unavailable" {
		t.Fatal("inactive management", w.Code, w.Body.String())
	}
	page := durableRender(s, cache, "alice@example.com").Body.String()
	if !strings.Contains(page, `data-personal-durable="true"`) || !strings.Contains(page, "loaded automatically after restart") || strings.Contains(page, `href="https://server.tailnet.ts.net/app"`) {
		t.Fatal("durable UI truth/eligibility")
	}
}

func TestPersonalDurablePrecommitGuardsAndUncertainty(t *testing.T) {
	for _, tc := range []string{"owner removed", "owner changed", "stale after sync", "expired csrf", "host", "origin", "identity", "proxy", "cancelled", "post-sync stale", "rename ambiguous", "directory sync"} {
		t.Run(tc, func(t *testing.T) {
			s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{}))
			cache := durableTestCache(t)
			at := cache.Get().UpdatedAt
			now := func() time.Time { return at }
			h := durableTestHandler(s, cache, now)
			state := personalHTTPRead(t, h)
			body, _ := json.Marshal(durableMutation(state, "create"))
			r := personalHTTPRequest("POST", body)
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			r = r.WithContext(ctx)
			before, _ := os.ReadFile(s.config.File)
			s.storage.failpoint = func(stage string) error {
				if stage == "filesync" {
					data := *cache.Get()
					switch tc {
					case "owner removed":
						data.TailscaleUsersByLogin = nil
					case "owner changed":
						data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "changed", DirectMember: true}}
					case "stale after sync":
						at = at.Add(91 * time.Second)
					case "expired csrf":
						at = at.Add(31 * time.Minute)
						data.UpdatedAt = at
					case "host":
						r.Host = "evil.example" // WithContext below copies Host: use origin case for mutable header; tested directly separately.
					case "origin":
						r.Header.Set("Origin", "http://evil.example")
					case "identity":
						r.Header.Set("Tailscale-User-Login", "bob@example.com")
					case "cancelled":
						cancel()
					}
					cache.data.Store(&data)
				}
				if stage == "dirsync" && tc == "directory sync" {
					return errors.New("fixture")
				}
				if stage == "dirsync" && tc == "post-sync stale" {
					at = at.Add(91 * time.Second)
				}
				return nil
			}
			if tc == "rename ambiguous" {
				s.storage.rename = func(int, string, int, string) error { return syscall.EIO }
			}
			w := httptest.NewRecorder()
			// Use an explicit context in final-guard fixtures so the same request is
			// inspected at admission and precommit (no middleware request struct copy).
			_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
			r = r.WithContext(context.WithValue(r.Context(), identityKey, &Identity{Login: "alice@example.com"}))
			if tc == "proxy" {
				s.storage.failpoint = func(stage string) error {
					if stage == "filesync" {
						r.RemoteAddr = "192.0.2.1:1234"
					}
					return nil
				}
			}
			(&personalServicesHandler{store: s, cache: cache, trusted: trusted, interval: 30 * time.Second, now: now}).ServeHTTP(w, r)
			if w.Code == 200 || strings.Contains(w.Body.String(), "document") {
				t.Fatal("final guard/uncertainty accepted", w.Code, w.Body.String())
			}
			uncertain := tc == "rename ambiguous" || tc == "directory sync"
			if uncertain {
				if !strings.Contains(w.Body.String(), "uncertain") {
					t.Fatal(w.Body.String())
				}
				if result, err := s.ReadForSnapshot("alice@example.com", cache.Get(), 30*time.Second, now); !errors.Is(err, errPersonalStorageUncertain) || result.Document.Entries != nil {
					t.Fatal("old authoritative data exposed", err)
				}
				if page := durableRender(s, cache, "alice@example.com").Body.String(); !strings.Contains(page, `data-personal-unavailable="true"`) || strings.Contains(page, `data-personal-service=`) || strings.Contains(page, `data-personal-revision=`) {
					t.Fatal("uncertain render must expose only draft-continuity markers")
				}
				w = httptest.NewRecorder()
				h.ServeHTTP(w, personalHTTPRequest("GET", nil))
				if w.Code != 503 || strings.Contains(w.Body.String(), "document") {
					t.Fatal("uncertain HTTP read")
				}
			} else if tc != "post-sync stale" {
				after, _ := os.ReadFile(s.config.File)
				if !bytes.Equal(before, after) {
					t.Fatal("precommit denial changed disk")
				}
			} else {
				after, _ := os.ReadFile(s.config.File)
				if bytes.Equal(before, after) {
					t.Fatal("post-sync test did not commit")
				}
			}
		})
	}
}

// The optional artifact feeds the actual API response and production portal
// fragments into the shipped JS controller regression, not a hand-made marker.
func TestPersonalDurableUncertainPollFixture(t *testing.T) {
	s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{}))
	cache := durableTestCache(t)
	h := durableTestHandler(s, cache, nil)
	initial := personalHTTPRead(t, h)
	w, saved := personalHTTPPost(t, h, durableMutation(initial, "create"))
	if w.Code != http.StatusOK {
		t.Fatal("fixture create failed", w.Code)
	}
	healthy := durableRender(s, cache, "alice@example.com").Body.String()
	scope := personalDraftScope(s.config.Namespace, "alice@example.com", cache.Get(), time.Now(), 30*time.Second)
	if len(scope) != 64 || !strings.Contains(healthy, `data-personal-owner-scope="`+scope+`"`) || !strings.Contains(healthy, `data-personal-service=`) {
		t.Fatal("healthy fragment missing owner continuity or active fixture card")
	}
	s.storage.rename = func(int, string, int, string) error { return syscall.EIO }
	request := durableMutation(saved, "update")
	request.ID = saved.Document.Entries[0].ID
	request.Name = "Uncertain local draft"
	w, _ = personalHTTPPost(t, h, request)
	if w.Code != http.StatusServiceUnavailable || strings.TrimSpace(w.Body.String()) != `{"error":"uncertain"}` {
		t.Fatal("fixture did not reach real uncertainty", w.Code, w.Body.String())
	}
	uncertain := durableRender(s, cache, "alice@example.com").Body.String()
	if !strings.Contains(uncertain, `data-personal-unavailable="true"`) || !strings.Contains(uncertain, `data-personal-owner-scope="`+scope+`"`) {
		t.Fatal("uncertainty lost same-owner continuity")
	}
	for _, forbidden := range []string{`data-personal-service=`, `data-personal-generation=`, `data-personal-revision=`, `data-personal-store=`, "Uncertain local draft", "alice-id"} {
		if strings.Contains(uncertain, forbidden) {
			t.Fatal("uncertain fragment exposes private state", forbidden)
		}
	}
	fragments := map[string]string{}
	original := cache.Get()
	for _, failure := range []string{"removed", "shared", "stale", "future", "reassigned", "renamed"} {
		data := *original
		data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"alice@example.com": {ID: "alice-id", DirectMember: true}}
		switch failure {
		case "removed":
			data.TailscaleUsersByLogin = nil
		case "shared":
			data.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "alice-id"}
		case "stale":
			data.UpdatedAt = time.Now().Add(-91 * time.Second)
		case "future":
			data.UpdatedAt = time.Now().Add(time.Hour)
		case "reassigned":
			data.TailscaleUsersByLogin["alice@example.com"] = TailscaleUserIdentity{ID: "replacement-id", DirectMember: true}
		case "renamed":
			data.TailscaleUsersByLogin = map[string]TailscaleUserIdentity{"new@example.com": {ID: "alice-id", DirectMember: true}}
		}
		cache.data.Store(&data)
		page := durableRender(s, cache, "alice@example.com").Body.String()
		if strings.Contains(page, `data-personal-owner-scope="`+scope+`"`) || strings.Contains(page, `data-personal-service=`) {
			t.Fatal("lost/changed owner retained prior continuity or cards", failure)
		}
		if failure != "reassigned" && !strings.Contains(page, `data-personal-enabled="false"`) {
			t.Fatal("ownership loss did not disable personal state", failure)
		}
		fragments[failure] = page
	}
	cache.data.Store(original)
	if scope == personalDraftScope("other-source", "alice@example.com", original, time.Now(), 30*time.Second) {
		t.Fatal("namespace replacement retained continuity")
	}
	if path := os.Getenv("VELOCIPORTAL_PERSONAL_POLL_FIXTURE"); path != "" {
		artifact, err := json.Marshal(map[string]any{"state": saved, "healthy": healthy, "uncertain": uncertain, "response": json.RawMessage(w.Body.Bytes()), "invalid": fragments})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, artifact, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPersonalDurableConcurrentCollectionsAndAbsentImport(t *testing.T) {
	s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{}))
	cache := durableTestCache(t)
	h := durableTestHandler(s, cache, nil)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		r := personalHTTPRequest("GET", nil)
		r.Header.Set("Tailscale-User-Login", login)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var state personalServicesResponse
		if json.Unmarshal(w.Body.Bytes(), &state) != nil || w.Code != 200 {
			t.Fatal("read")
		}
		mutation := PersonalServicesMutation{Action: "restore", StoreID: state.StoreID, Generation: state.Generation, Revision: state.Revision, CSRF: state.CSRF, Document: &PersonalServicesDocument{Version: 1, Entries: []PersonalService{personalTestEntry(0)}}}
		body, _ := json.Marshal(mutation)
		wg.Add(1)
		go func(login string, body []byte) {
			defer wg.Done()
			r := personalHTTPRequest("POST", body)
			r.Header.Set("Tailscale-User-Login", login)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes <- w.Code
		}(login, body)
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal(code)
		}
	}
	snapshot, _, err := s.storage.Read()
	if err != nil || len(snapshot.Collections) != 2 {
		t.Fatal("lost collection", err)
	}
	for _, record := range snapshot.Collections {
		if record.Revision != "1" || len(record.Document.Entries) != 1 {
			t.Fatal("cross-user lost update")
		}
	}
	// Same-user simultaneous CAS still admits exactly one, without whole-file CAS
	// leakage or conflicts caused only by another identity's earlier mutation.
	state := personalHTTPRead(t, h)
	mutation := durableMutation(state, "create")
	body, _ := json.Marshal(mutation)
	codes = make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, personalHTTPRequest("POST", body))
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
}

func TestPersonalDurableCapacityAndOverflow(t *testing.T) {
	records := []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "18446744073709551615", []PersonalService{})}
	for i := 1; i < 128; i++ {
		records = append(records, durableRecord(fmt.Sprintf("user%d@example.com", i), fmt.Sprintf("id%d", i), "1", []PersonalService{}))
	}
	s := durableTestStore(t, durableTestConfig(t, records))
	cache := durableTestCache(t)
	h := durableTestHandler(s, cache, nil)
	state := personalHTTPRead(t, h)
	w, _ := personalHTTPPost(t, h, durableMutation(state, "create"))
	if w.Code != 503 {
		t.Fatal("overflow", w.Code)
	}
	r := personalHTTPRequest("GET", nil)
	r.Header.Set("Tailscale-User-Login", "bob@example.com")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("full collection read")
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Initialized || state.Revision != "0" {
		t.Fatal("absent consumed slot")
	}
	body, _ := json.Marshal(durableMutation(state, "create"))
	r = personalHTTPRequest("POST", body)
	r.Header.Set("Tailscale-User-Login", "bob@example.com")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 507 {
		t.Fatal("collection cap", w.Code, w.Body.String())
	}
}

func TestPersonalDurableConfigurationAndLifecycle(t *testing.T) {
	for _, mode := range []string{"off", "browser", "shared"} {
		for _, tc := range []string{"valid", "no file", "empty file", "relative file", "unclean file", "padded file", "empty namespace", "bad namespace", "no namespace", "no origin", "headscale", "namespace only"} {
			t.Run(mode+"/"+tc, func(t *testing.T) {
				values := validConfigValues()
				values["CONTROL_PLANE"] = "tailscale"
				values["TAILSCALE_OAUTH_CLIENT_ID"] = "fixture"
				values["TAILSCALE_OAUTH_CLIENT_SECRET"] = "fixture"
				values["PORTAL_EDIT_MODE"] = mode
				values["PORTAL_PERSONAL_SERVICES_ORIGIN"] = "http://portal.example:8081"
				values["PORTAL_PERSONAL_SERVICES_FILE"] = "/private/authored.json"
				values["PORTAL_STATE_NAMESPACE"] = "fixture-1"
				if mode == "shared" {
					values["SERVICE_METADATA_FILE"] = "/shared/services.json"
					values["PORTAL_EDITORS"] = `["alice@example.com"]`
					values["PORTAL_PUBLIC_ORIGIN"] = "http://portal.example:8081"
				}
				switch tc {
				case "no file":
					delete(values, "PORTAL_PERSONAL_SERVICES_FILE")
					delete(values, "PORTAL_STATE_NAMESPACE")
				case "empty file":
					values["PORTAL_PERSONAL_SERVICES_FILE"] = ""
				case "relative file":
					values["PORTAL_PERSONAL_SERVICES_FILE"] = "private.json"
				case "unclean file":
					values["PORTAL_PERSONAL_SERVICES_FILE"] = "/private/../authored.json"
				case "padded file":
					values["PORTAL_PERSONAL_SERVICES_FILE"] = " /private/authored.json"
				case "empty namespace":
					values["PORTAL_STATE_NAMESPACE"] = ""
				case "bad namespace":
					values["PORTAL_STATE_NAMESPACE"] = "Wrong"
				case "no namespace":
					delete(values, "PORTAL_STATE_NAMESPACE")
				case "no origin":
					delete(values, "PORTAL_PERSONAL_SERVICES_ORIGIN")
				case "headscale":
					values["CONTROL_PLANE"] = "headscale"
				case "namespace only":
					delete(values, "PORTAL_PERSONAL_SERVICES_FILE")
				}
				cfg, err := loadConfigFrom(func(k string) (string, bool, error) { v, ok := values[k]; return v, ok, nil })
				good := tc == "valid" || tc == "no file" || (tc == "namespace only" && mode == "shared")
				if (err == nil) != good {
					t.Fatal(err)
				}
				if good && cfg.PersonalServices == nil {
					t.Fatal("missing config")
				}
			})
		}
	}
	for _, tc := range []string{"missing", "corrupt", "unsafe", "namespace", "metadata collision", "health collision", "metadata alias", "health alias", "unclean presentation", "lock collision", "shared failure"} {
		t.Run(tc, func(t *testing.T) {
			cfg := &Config{ControlPlane: controlPlaneTailscale, PersonalServices: durableTestConfig(t, []personalStorageCollection{})}
			path := cfg.PersonalServices.File
			switch tc {
			case "missing":
				_ = os.Remove(path)
			case "corrupt":
				_ = os.WriteFile(path, []byte("bad"), 0600)
			case "unsafe":
				_ = os.Chmod(path, 0644)
			case "namespace":
				cfg.PersonalServices.Namespace = "different"
			case "metadata collision":
				cfg.ServiceMetadataFile = path
				cfg.ServiceMetadataEditor = editorTestConfig()
			case "health collision":
				cfg.ServiceHealthFile = path
			case "metadata alias", "health alias":
				alias := filepath.Join(filepath.Dir(path), "alias.json")
				if err := os.Symlink(path, alias); err != nil {
					t.Fatal(err)
				}
				if tc == "metadata alias" {
					cfg.ServiceMetadataFile = alias
				} else {
					cfg.ServiceHealthFile = alias
				}
			case "unclean presentation":
				cfg.ServiceMetadataFile = filepath.Dir(path) + "/unused/../private.json"
			case "lock collision":
				cfg.ServiceMetadataFile = path + ".lock"
			case "shared failure":
				cfg.ServiceMetadataFile = filepath.Join(filepath.Dir(path), "missing-shared.json")
				cfg.ServiceMetadataEditor = editorTestConfig()
			}
			editor, store, err := newPresentationStores(cfg)
			if err == nil || editor != nil || store != nil {
				t.Fatal("invalid startup fell back/succeeded")
			}
			if tc == "shared failure" {
				reopened := durableTestStore(t, cfg.PersonalServices)
				_ = reopened.Close()
			} else if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid startup created lock")
			}
		})
	}
}

func TestPersonalDurableWireAndEntryBounds(t *testing.T) {
	entries := make([]PersonalService, maxPersonalEntries)
	for i := range entries {
		entries[i] = personalTestEntry(i)
	}
	s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{durableRecord("alice@example.com", "alice-id", "1", entries)}))
	cache := durableTestCache(t)
	h := durableTestHandler(s, cache, nil)
	state := personalHTTPRead(t, h)
	w, _ := personalHTTPPost(t, h, durableMutation(state, "create"))
	if w.Code != 507 {
		t.Fatal("entry cap", w.Code)
	}
	request := durableMutation(state, "update")
	request.ID = entries[0].ID
	request.URL = "https://server.tailnet.ts.net/" + strings.Repeat("x", 2040)
	w, _ = personalHTTPPost(t, h, request)
	if w.Code != 400 {
		t.Fatal("authored URL bound", w.Code)
	}
	request.URL = entries[0].URL
	body, _ := json.Marshal(request)
	for _, value := range []string{`null`, `1`, `""`, `"ABC"`, `"` + strings.Repeat("a", 33) + `"`} {
		bad := strings.Replace(string(body), `"store_id":"`+state.StoreID+`"`, `"store_id":`+value, 1)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, personalHTTPRequest("POST", []byte(bad)))
		if w.Code != 400 {
			t.Fatal("invalid stable-store wire accepted", w.Code)
		}
	}
	cache.data.Store(nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("GET", nil))
	if w.Code != 503 || strings.Contains(w.Body.String(), "document") {
		t.Fatal("cold durable read disclosed data")
	}
}

func TestPersonalDurableReadClockAfterLockWait(t *testing.T) {
	s := durableTestStore(t, durableTestConfig(t, []personalStorageCollection{}))
	cache := durableTestCache(t)
	at := cache.Get().UpdatedAt
	started, done := make(chan struct{}), make(chan error, 1)
	s.mu.Lock()
	go func() {
		close(started)
		_, err := s.ReadForSnapshot("alice@example.com", cache.Get(), 30*time.Second, func() time.Time { return at })
		done <- err
	}()
	<-started
	// mu's release/acquire also synchronizes the fixture clock update. An early
	// clock read would be a race and could authorize a now-stale snapshot.
	at = at.Add(91 * time.Second)
	s.mu.Unlock()
	if err := <-done; !errors.Is(err, errPersonalUnavailable) {
		t.Fatal("early clock bypassed freshness", err)
	}
}

func TestPersonalDurableProductionMuxModes(t *testing.T) {
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser, serviceEditShared} {
		t.Run(string(mode), func(t *testing.T) {
			_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
			cfg := &Config{ControlPlane: controlPlaneTailscale, TrustedProxyCIDR: trusted, PollInterval: 30 * time.Second, PortalEditMode: mode, PersonalServices: durableTestConfig(t, []personalStorageCollection{})}
			if mode == serviceEditShared {
				cfg.ServiceMetadataFile = editorTestFile(t, editorTestDocument)
				cfg.ServiceMetadataEditor = editorTestConfig()
			}
			editor, store, err := newPresentationStores(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			defer store.Close()
			cache := durableTestCache(t)
			mux, err := newServerMux(cfg, cache, nil, editor, store)
			if err != nil {
				t.Fatal(err)
			}
			state := personalHTTPRead(t, mux)
			w, _ := personalHTTPPost(t, mux, durableMutation(state, "create"))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, path := range []string{"/", "/portal", "/unknown", "/api/personal-services/extra"} {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, personalHTTPRequestAt("GET", path))
				want := 404
				if path == "/" || path == "/portal" {
					want = 200
				}
				if w.Code != want {
					t.Fatal(path, w.Code)
				}
			}
		})
	}
}
