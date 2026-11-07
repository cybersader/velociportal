package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func personalHTTPFixture(t *testing.T) (*PersonalServicesStore, http.Handler, *Cache) {
	t.Helper()
	s := personalTestStore(t)
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	cache := &Cache{}
	_, data, _, _ := personalProjectionFixture(t)
	data.UpdatedAt = time.Now()
	cache.data.Store(data)
	return s, newPersonalServicesHandler(s, cache, trusted, 30*time.Second), cache
}

func personalHTTPRequest(method string, body []byte) *http.Request {
	r := httptest.NewRequest(method, "http://portal.example:8081"+personalServicesRoute, bytes.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Tailscale-User-Login", "alice@example.com")
	r.Header.Set("Origin", "http://portal.example:8081")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("Sec-Fetch-Mode", "cors")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	return r
}

func personalHTTPRead(t *testing.T, h http.Handler) personalServicesResponse {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("GET", nil))
	if w.Code != 200 {
		t.Fatal("GET", w.Code, w.Body.String())
	}
	var result personalServicesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func personalHTTPPost(t *testing.T, h http.Handler, request PersonalServicesMutation) (*httptest.ResponseRecorder, personalServicesResponse) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("POST", data))
	var result personalServicesResponse
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return w, result
}

func TestPersonalHTTPAuthoritativeCopiesAndRecovery(t *testing.T) {
	s, h, cache := personalHTTPFixture(t)
	first := personalHTTPRead(t, h)
	if first.Initialized || first.Revision != "0" || len(s.collections) != 0 || first.CSRF == "" || len(first.IdentityScope) != 64 || len(first.Icons) == 0 {
		t.Fatal("GET contract")
	}
	create := personalTestMutation(first.PersonalServicesState, "create")
	create.CSRF = first.CSRF
	w, saved := personalHTTPPost(t, h, create)
	if w.Code != 200 || saved.Revision != "1" || !saved.Initialized || saved.Projection[0].Status != "active" {
		t.Fatal("create", w.Code, w.Body.String())
	}
	second := personalHTTPRead(t, h)
	if second.Revision != saved.Revision || second.Document.Entries[0] != saved.Document.Entries[0] {
		t.Fatal("second client did not see acknowledged write")
	}
	// An uncertain save is reconciled by GET, never by reissuing the mutation.
	w, _ = personalHTTPPost(t, h, create)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "conflict") {
		t.Fatal("retry silently overwrote")
	}
	cache.data.Store(nil)
	unresolved := personalHTTPRead(t, h)
	if len(unresolved.Document.Entries) != 1 || unresolved.Projection[0].Status != "unavailable" || strings.Contains(w.Body.String(), "100.64") {
		t.Fatal("cold state lost authored records/leaked topology")
	}
	remove := PersonalServicesMutation{Action: "delete", Generation: second.Generation, Revision: second.Revision, CSRF: second.CSRF, ID: second.Document.Entries[0].ID}
	w, empty := personalHTTPPost(t, h, remove)
	if w.Code != 200 || !empty.Initialized || empty.Revision != "2" || len(empty.Document.Entries) != 0 {
		t.Fatal("empty state", w.Code, w.Body.String())
	}
	restore := PersonalServicesMutation{Action: "restore", Generation: empty.Generation, Revision: empty.Revision, CSRF: empty.CSRF, Document: &saved.Document}
	w, _ = personalHTTPPost(t, h, restore)
	if w.Code != 409 {
		t.Fatal("empty collection resurrected")
	}
	for key, value := range map[string]string{"Cache-Control": "no-store, max-age=0", "X-Frame-Options": "DENY", "Content-Security-Policy": "frame-ancestors 'none'", "X-Content-Type-Options": "nosniff"} {
		if w.Header().Get(key) != value {
			t.Fatalf("missing header %s", key)
		}
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "" || !strings.Contains(w.Header().Get("Vary"), "Tailscale-User-Login") {
		t.Fatal("cache/CORS boundary")
	}
}

func TestPersonalHTTPSecurityGuards(t *testing.T) {
	_, h, _ := personalHTTPFixture(t)
	state := personalHTTPRead(t, h)
	mutation := personalTestMutation(state.PersonalServicesState, "create")
	mutation.CSRF = state.CSRF
	body, _ := json.Marshal(mutation)
	for _, test := range []struct {
		name   string
		change func(*http.Request)
		code   int
	}{
		{"untrusted", func(r *http.Request) { r.RemoteAddr = "192.0.2.1:1234" }, 403},
		{"missing identity", func(r *http.Request) { r.Header.Del("Tailscale-User-Login") }, 401},
		{"duplicate identity", func(r *http.Request) { r.Header.Add("Tailscale-User-Login", "bob@example.com") }, 403},
		{"case duplicate identity", func(r *http.Request) { r.Header["tailscale-user-login"] = []string{"bob@example.com"} }, 403},
		{"duplicate profile", func(r *http.Request) { r.Header["Tailscale-User-Name"] = []string{"one", "two"} }, 403},
		{"padded identity", func(r *http.Request) { r.Header.Set("Tailscale-User-Login", " alice@example.com ") }, 403},
		{"machine identity", func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "tag:server") }, 403},
		{"bare identity", func(r *http.Request) { r.Header.Set("Tailscale-User-Login", "alice") }, 403},
		{"wrong host", func(r *http.Request) { r.Host = "evil.example" }, 403},
		{"wrong origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, 403},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"duplicate origin", func(r *http.Request) { r.Header.Add("Origin", "http://portal.example:8081") }, 403},
		{"cross-site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		{"same-site not origin", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }, 403},
		{"navigate", func(r *http.Request) { r.Header.Set("Sec-Fetch-Mode", "navigate") }, 403},
		{"frame", func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "iframe") }, 403},
		{"duplicate fetch", func(r *http.Request) { r.Header.Add("Sec-Fetch-Site", "same-origin") }, 403},
		{"content type params", func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }, 415},
		{"duplicate content type", func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, 415},
		{"query", func(r *http.Request) { r.URL.RawQuery = "ignored=1" }, 404},
		{"escaped path", func(r *http.Request) { r.URL.RawPath = "/api/%70ersonal-services" }, 404},
		{"method", func(r *http.Request) { r.Method = "PUT" }, 405},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := personalHTTPRequest("POST", body)
			test.change(r)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != test.code {
				t.Fatalf("code=%d want=%d body=%s", w.Code, test.code, w.Body.String())
			}
		})
	}
	for _, key := range []string{"Origin", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		r := personalHTTPRequest("GET", nil)
		r.Header.Del(key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("GET without optional %s", key)
		}
	}
}

func TestPersonalHTTPStrictMutationSchema(t *testing.T) {
	_, h, _ := personalHTTPFixture(t)
	state := personalHTTPRead(t, h)
	mutation := personalTestMutation(state.PersonalServicesState, "create")
	mutation.CSRF = state.CSRF
	body, _ := json.Marshal(mutation)
	valid := string(body)
	for _, bad := range []string{
		`null`, `[]`, valid + ` {}`, strings.Replace(valid, `"action":"create"`, `"action":"create","action":"delete"`, 1), strings.Replace(valid, `"action":"create"`, `"Action":"create"`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":null`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":"\ud800"`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":"\udc00"`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":"\ud800A"`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":"\u0000"`, 1), strings.Replace(valid, `"revision":"0"`, `"revision":0`, 1), strings.Replace(valid, `"revision":"0"`, `"revision":"00"`, 1), strings.Replace(valid, `"revision":"0"`, `"revision":"18446744073709551616"`, 1), strings.Replace(valid, `"url":`, `"extra":1,"url":`, 1), strings.Replace(valid, `"url":`, `"icon":null,"url":`, 1), strings.Replace(valid, `"url":`, `"id":"ps_00000000000000000000000000000001","url":`, 1), strings.Replace(valid, `"url":`, `"document":null,"url":`, 1), strings.Replace(valid, `"name":"Tool"`, `"name":"`+string([]byte{0xff})+`"`, 1),
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, personalHTTPRequest("POST", []byte(bad)))
		if w.Code != 400 {
			t.Errorf("strict request code=%d body=%s input=%q", w.Code, w.Body.String(), bad)
		}
	}
	base := `{"action":"restore","generation":"` + state.Generation + `","revision":"0","csrf":"` + state.CSRF + `","document":`
	for _, doc := range []string{`null`, `{"version":1,"entries":null}`, `{"version":1,"entries":[],"extra":1}`, `{"version":1.0,"entries":[]}`, `{"Version":1,"entries":[]}`, `{"version":1,"entries":[{"id":"ps_00000000000000000000000000000001","name":"Tool","url":"https://server.tailnet.ts.net","icon":null}]}`, `{"version":1,"entries":[{"id":"ps_00000000000000000000000000000001","name":"Tool","url":"https://server.tailnet.ts.net","url":"https://evil.example"}]}`} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, personalHTTPRequest("POST", []byte(base+doc+`}`)))
		if w.Code != 400 {
			t.Errorf("strict restore code=%d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("POST", bytes.Repeat([]byte(" "), maxPersonalRequestBytes+1)))
	if w.Code != 413 {
		t.Fatal("request bound", w.Code)
	}
	// Well-formed UTF16 surrogate pairs represent valid Unicode names.
	good := strings.Replace(valid, `"name":"Tool"`, `"name":"🚀"`, 1)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("POST", []byte(good)))
	if w.Code != 200 {
		t.Fatal("valid surrogate pair rejected", w.Code, w.Body.String())
	}
}

func TestPersonalHTTPCSRFAndConcurrency(t *testing.T) {
	s, h, _ := personalHTTPFixture(t)
	state := personalHTTPRead(t, h)
	now := time.Now().Truncate(time.Second)
	for _, test := range []struct {
		name, token, login string
		at                 time.Time
		valid              bool
	}{
		{"current", s.csrf("alice@example.com", now), "alice@example.com", now, true},
		{"TTL boundary", s.csrf("alice@example.com", now), "alice@example.com", now.Add(personalCSRFDuration), true},
		{"expired", s.csrf("alice@example.com", now), "alice@example.com", now.Add(personalCSRFDuration + time.Second), false},
		{"future", s.csrf("alice@example.com", now), "alice@example.com", now.Add(-time.Second), false},
		{"different identity", s.csrf("alice@example.com", now), "bob@example.com", now, false},
		{"invalid", "bogus", "alice@example.com", now, false},
	} {
		if s.validCSRF(test.login, test.token, test.at) != test.valid {
			t.Error(test.name)
		}
	}
	other := personalTestStore(t)
	if other.validCSRF("alice@example.com", state.CSRF, time.Now()) {
		t.Fatal("CSRF survived process generation")
	}
	oldOrigin := s.config.Origin
	s.config.Origin = "https://other.example"
	if s.validCSRF("alice@example.com", state.CSRF, time.Now()) {
		t.Fatal("CSRF not origin bound")
	}
	s.config.Origin = oldOrigin
	mutation := personalTestMutation(state.PersonalServicesState, "create")
	mutation.CSRF = "bogus"
	w, _ := personalHTTPPost(t, h, mutation)
	if w.Code != 403 {
		t.Fatal("invalid csrf accepted")
	}
	mutation.CSRF = state.CSRF
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, _ := json.Marshal(mutation)
			r := personalHTTPRequest("POST", data)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for code := range codes {
		if code == 200 {
			ok++
		} else if code == 409 {
			conflict++
		} else {
			t.Fatal("unexpected concurrent status", code)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal("concurrent create CAS")
	}
	for i := 0; i < cap(s.requests); i++ {
		s.requests <- struct{}{}
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, personalHTTPRequest("GET", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "busy") {
		t.Fatal("active allocation cap")
	}
	for i := 0; i < cap(s.requests); i++ {
		<-s.requests
	}
	mux := http.NewServeMux()
	mountPersonalServices(mux, nil, nil, nil, time.Second)
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", personalServicesRoute, nil))
	if w.Code != 404 {
		t.Fatal("disabled route mounted")
	}
	// Read copies need no snapshot at all and cannot initialize a collection.
	r := personalHTTPRequest("GET", nil)
	r.Header.Set("Tailscale-User-Login", "bob@example.com")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	data, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 || !bytes.Contains(data, []byte(`"initialized":false`)) {
		t.Fatal("identity-scoped absence")
	}
}
