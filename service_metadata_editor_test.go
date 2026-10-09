package main

import (
	"bytes"
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
	"testing"
	"time"
)

const editorTestDocument = `{"version":2,"services":[{"proxy_host_id":99,"name":null,"url":"https://unseen.example/path","category":"Unseen","order":0},{"proxy_host_id":3,"name":"Old Wiki","url":null,"category":"Tools","order":8}]}`

func editorTestConfig() *ServiceMetadataEditorConfig {
	return &ServiceMetadataEditorConfig{Logins: map[string]bool{"alice@example.com": true, "bob@example.com": true}, Origin: "http://portal.example:8081", Host: "portal.example:8081"}
}
func editorTestFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "services.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
func editorFixture(t *testing.T) (*ServiceMetadataEditor, *Cache, http.Handler) {
	t.Helper()
	e, err := newServiceMetadataEditor(editorTestFile(t, editorTestDocument), editorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	cache := newTestCache(standardTestData())
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	return e, cache, newServiceMetadataEditorHandler(e, cache, trusted, 30*time.Second)
}
func editHTTPRequest(handler http.Handler, method, id, login, body string, change func(*http.Request)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "http://portal.example:8081"+serviceMetadataEditorRoute+id, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("Tailscale-User-Login", login)
	if method == http.MethodPost {
		request.Header.Set("Origin", "http://portal.example:8081")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if change != nil {
		change(request)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
func editForm(t *testing.T, handler http.Handler, id, login string) serviceMetadataEditForm {
	t.Helper()
	response := editHTTPRequest(handler, http.MethodGet, id, login, "", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", response.Code, response.Body.String())
	}
	var form serviceMetadataEditForm
	if err := json.Unmarshal(response.Body.Bytes(), &form); err != nil {
		t.Fatal(err)
	}
	return form
}
func editBody(form serviceMetadataEditForm, action string) string {
	fields := map[string]string{"action": action, "revision": form.Revision, "target_revision": form.TargetRevision, "csrf": form.CSRF}
	if action == "save" {
		fields["name"] = "Shared Wiki"
		fields["url"] = "https://shared.example/path"
		fields["icon"] = "generic"
	}
	body, _ := json.Marshal(fields)
	return string(body)
}

func TestServiceMetadataV3PreservationAndReset(t *testing.T) {
	for _, source := range []string{
		`{"version":1,"services":[{"proxy_host_id":3,"name":"Before","url":null},{"proxy_host_id":99,"name":null,"url":"https://unseen.example/"}]}`,
		editorTestDocument,
	} {
		metadata, err := parseServiceMetadata([]byte(source))
		if err != nil {
			t.Fatal(err)
		}
		document, err := mutateServiceMetadata(metadata.document, 3, "Shared", "https://shared.example", "generic", false)
		if err != nil {
			t.Fatal(err)
		}
		data, err := serializeServiceMetadataDocumentV3(document)
		if err != nil {
			t.Fatal(err)
		}
		result, err := parseServiceMetadata(data)
		if err != nil {
			t.Fatal(err)
		}
		if result.document.Version != 3 || result.Overrides[3].Icon != "generic" {
			t.Fatalf("bad upgrade: %s", data)
		}
		if !reflect.DeepEqual(metadata.Overrides[99], result.Overrides[99]) || !result.document.Services[1].NameNull {
			t.Fatalf("untouched data lost: %s", data)
		}
		again, err := serializeServiceMetadataDocumentV3(result.document)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatal("nondeterministic roundtrip", err)
		}
		reset, err := mutateServiceMetadata(result.document, 3, "", "", "", true)
		if err != nil {
			t.Fatal(err)
		}
		data, err = serializeServiceMetadataDocumentV3(reset)
		if err != nil {
			t.Fatal(err)
		}
		result, _ = parseServiceMetadata(data)
		override, exists := result.Overrides[3]
		if metadata.document.Version == 1 && exists {
			t.Fatal("empty reset entry retained")
		}
		if metadata.document.Version == 2 && (!exists || override.Category != "Tools" || override.Order == nil || *override.Order != 8 || override.Name != "" || override.URL != "" || override.Icon != "") {
			t.Fatal("organization reset changed", override)
		}
	}
}

func TestServiceMetadataV3StrictAndBounds(t *testing.T) {
	for _, source := range []string{
		`{"version":1,"services":[{"proxy_host_id":1,"icon":"generic"}]}`,
		`{"version":2,"services":[{"proxy_host_id":1,"icon":"generic"}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":"../other"}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":"https://icon.example"}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":null,"name":"A"}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":"generic","Icon":"generic"}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":"generic","extra":1}]}`,
		`{"version":3,"services":[{"proxy_host_id":1,"icon":"generic"}]} {}`,
	} {
		if _, err := parseServiceMetadata([]byte(source)); err == nil {
			t.Fatal("accepted", source)
		}
	}
	name := strings.Repeat("x", maxServiceMetadataName)
	link := "https://example.com/" + strings.Repeat("x", maxServiceMetadataURL-20)
	document := serviceMetadataDocument{Version: 3, Services: make([]serviceMetadataEntry, 200)}
	for i := range document.Services {
		document.Services[i] = serviceMetadataEntry{ProxyHostID: i + 1, Name: &name, URL: &link}
	}
	if _, err := serializeServiceMetadataDocumentV3(document); err == nil {
		t.Fatal("oversized full serialization accepted")
	}
	if _, err := parseServiceMetadata(bytes.Repeat([]byte(" "), maxServiceMetadataBytes+1)); err == nil {
		t.Fatal("oversized parse accepted")
	}
}

func TestServiceMetadataEditorConfig(t *testing.T) {
	base := map[string]string{"PORTAL_EDITORS": `["alice@example.com"]`, "PORTAL_PUBLIC_ORIGIN": "http://portal.example:8081"}
	config, err := loadServiceMetadataEditorConfig(mapConfigLookup(base), "/services.json")
	if err != nil || !config.Logins["alice@example.com"] || config.Logins["Alice@example.com"] {
		t.Fatal(config, err)
	}
	disabled, err := loadServiceMetadataEditorConfig(mapConfigLookup(nil), "")
	if err != nil || disabled != nil {
		t.Fatal("default not disabled")
	}
	for _, editors := range []string{"", "null", `[null]`, `["alice"]`, `["alice@"]`, `[" alice@example.com"]`, `["alice@example.com","alice@example.com"]`, `["alice@example.com"] []`, `["alice@example.com\n"]`, `["` + strings.Repeat("a", 254) + `@example.com"]`} {
		values := map[string]string{"PORTAL_EDITORS": editors, "PORTAL_PUBLIC_ORIGIN": base["PORTAL_PUBLIC_ORIGIN"]}
		if _, err := loadServiceMetadataEditorConfig(mapConfigLookup(values), "/services.json"); err == nil {
			t.Fatal("accepted editors", editors)
		}
	}
	for _, values := range []map[string]string{{"PORTAL_EDITORS": base["PORTAL_EDITORS"]}, {"PORTAL_PUBLIC_ORIGIN": base["PORTAL_PUBLIC_ORIGIN"]}} {
		if _, err := loadServiceMetadataEditorConfig(mapConfigLookup(values), "/services.json"); err == nil {
			t.Fatal("accepted partial")
		}
	}
	if _, err := loadServiceMetadataEditorConfig(mapConfigLookup(base), ""); err == nil {
		t.Fatal("accepted absent metadata")
	}
	for _, origin := range []string{"https://portal.example/", "HTTPS://portal.example", "https://Portal.example", "https://*.example", "https://u@portal.example", "https://portal.example?", "https://portal.example#", "https://portal.example:443", "http://portal.example:08081", "https://portal.example:", "https://portal.example/path", "https://portal.example.", "https://portal.example%20", "https://[portal.example]", "https://[portal.example]:8443", "http://[127.0.0.1]", "http://[127.0.0.1]:8081"} {
		if _, err := validatePortalPublicOrigin(origin); err == nil {
			t.Fatal("accepted origin", origin)
		}
	}
	for _, origin := range []string{"http://portal.example:8081", "https://portal.example", "http://127.0.0.1:8081", "http://[::1]:8081", "https://[2001:db8::1]"} {
		if _, err := validatePortalPublicOrigin(origin); err != nil {
			t.Fatal(origin, err)
		}
	}
}

func TestServiceMetadataEditorDefaultOffAndStartupNoMigration(t *testing.T) {
	path := editorTestFile(t, editorTestDocument)
	before, _ := os.ReadFile(path)
	e, err := newServiceMetadataEditor(path, nil)
	if err != nil || e != nil {
		t.Fatal(e, err)
	}
	if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("disabled editor created lock")
	}
	e, err = newServiceMetadataEditor(path, editorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("startup migrated")
	}
	lockBefore, _ := os.Stat(path + ".lock")
	contender, err := newServiceMetadataEditor(path, editorTestConfig())
	if err == nil {
		contender.Close()
		t.Fatal("second writer acquired lock")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newServiceMetadataEditor(path, editorTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	lockAfter, _ := os.Stat(path + ".lock")
	if !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("lock inode replaced on restart")
	}
}

func TestServiceMetadataEditorUnsafePaths(t *testing.T) {
	cases := []string{"missing", "symlink", "hardlink", "directory", "world-writable-file", "world-writable-directory", "symlink-parent", "symlink-lock", "hardlink-lock", "invalid-document"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			path := editorTestFile(t, editorTestDocument)
			switch name {
			case "missing":
				os.Remove(path)
			case "symlink":
				os.Rename(path, path+".real")
				os.Symlink(path+".real", path)
			case "hardlink":
				os.Link(path, path+".other")
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0o700)
			case "world-writable-file":
				os.Chmod(path, 0o666)
			case "world-writable-directory":
				os.Chmod(filepath.Dir(path), 0o777)
			case "symlink-parent":
				alias := filepath.Join(t.TempDir(), "alias")
				os.Symlink(filepath.Dir(path), alias)
				path = filepath.Join(alias, "services.json")
			case "symlink-lock":
				os.Symlink(path, path+".lock")
			case "hardlink-lock":
				os.Link(path, path+".lock")
			case "invalid-document":
				os.WriteFile(path, []byte(`{}`), 0o600)
			}
			e, err := newServiceMetadataEditor(path, editorTestConfig())
			if err == nil {
				e.Close()
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestServiceMetadataEditorSaveAndPollingCannotResurrect(t *testing.T) {
	e, cache, handler := editorFixture(t)
	beforeAlice := MatchServices(&Identity{Login: "alice@example.com"}, e.requestData(cache.Get()))
	beforeBob := MatchServices(&Identity{Login: "bob@example.com"}, e.requestData(cache.Get()))
	form := editForm(t, handler, "3", "alice@example.com")
	if form.Fields.Name != "Old Wiki" || form.Defaults.Name != "wiki.example.com" || form.Defaults.Icon != "generic" || len(form.Icons) != len(serviceIcons) {
		t.Fatal(form)
	}
	response := editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		updated := editForm(t, handler, "3", login)
		if updated.Fields.Name != "Shared Wiki" || updated.Fields.URL != "https://shared.example/path" || updated.Fields.Icon != "generic" {
			t.Fatal(updated)
		}
	}
	// Publish an old poll's metadata pointer after saving; the request-local view
	// is still committed and the authorization snapshot itself stays unmodified.
	old := cache.Get()
	stalePoll := *old
	stalePoll.ServiceMetadata, _ = parseServiceMetadata([]byte(editorTestDocument))
	stalePoll.UpdatedAt = time.Now()
	cache.data.Store(&stalePoll)
	for _, test := range []struct {
		login  string
		before []ServiceCard
	}{{"alice@example.com", beforeAlice}, {"bob@example.com", beforeBob}} {
		after := MatchServices(&Identity{Login: test.login}, e.requestData(cache.Get()))
		if len(after) != len(test.before) {
			t.Fatal("card set changed")
		}
		beforeIDs, afterIDs := map[int]bool{}, map[int]bool{}
		for _, card := range test.before {
			beforeIDs[card.ID] = true
		}
		for _, card := range after {
			afterIDs[card.ID] = true
			if card.ID == 3 && card.Name != "Shared Wiki" {
				t.Fatal("poll resurrected")
			}
		}
		if !reflect.DeepEqual(beforeIDs, afterIDs) {
			t.Fatal("authorization changed")
		}
	}
	if cache.Get().ServiceMetadata.Overrides[3].Name != "Old Wiki" {
		t.Fatal("authorization cache mutated")
	}
	if info, _ := os.Stat(e.path); info.Mode().Perm() != 0o600 {
		t.Fatal("replacement not restrictive")
	}
	form = editForm(t, handler, "3", "alice@example.com")
	response = editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "reset"), nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	override := e.view.Load().metadata.Overrides[3]
	if override.Name != "" || override.URL != "" || override.Icon != "" || override.Category != "Tools" || override.Order == nil || *override.Order != 8 {
		t.Fatal(override)
	}
}

func TestServiceMetadataEditorWholeFileConflict(t *testing.T) {
	e, _, handler := editorFixture(t)
	first := editForm(t, handler, "3", "alice@example.com")
	unrelated := editForm(t, handler, "1", "alice@example.com")
	response := editHTTPRequest(handler, "POST", "1", "alice@example.com", editBody(unrelated, "save"), nil)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	confirmed := append([]byte(nil), e.view.Load().bytes...)
	response = editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(first, "save"), nil)
	if response.Code != 409 || !bytes.Equal(confirmed, e.view.Load().bytes) {
		t.Fatal("whole-file lost update", response.Code)
	}
	fresh := editForm(t, handler, "3", "alice@example.com")
	external := append(append([]byte(nil), confirmed...), '\n')
	if err := os.WriteFile(e.path, external, 0o600); err != nil {
		t.Fatal(err)
	}
	response = editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(fresh, "save"), nil)
	disk, _ := os.ReadFile(e.path)
	if response.Code != 409 || !bytes.Equal(disk, external) || !bytes.Equal(confirmed, e.view.Load().bytes) {
		t.Fatal("external drift overwritten")
	}
	response = editHTTPRequest(handler, "GET", "3", "alice@example.com", "", nil)
	if response.Code != 409 {
		t.Fatal("GET failed to report external drift")
	}
}

func TestServiceMetadataEditorIOFailures(t *testing.T) {
	for _, stage := range []string{"create", "write", "filesync", "close", "recheck", "rename", "dirsync"} {
		t.Run(stage, func(t *testing.T) {
			e, _, handler := editorFixture(t)
			form := editForm(t, handler, "3", "alice@example.com")
			before := append([]byte(nil), e.view.Load().bytes...)
			e.failpoint = func(current string) error {
				if stage == current {
					return errors.New("synthetic failure")
				}
				return nil
			}
			response := editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
			disk, _ := os.ReadFile(e.path)
			if !bytes.Equal(before, e.view.Load().bytes) {
				t.Fatal("failed write published")
			}
			temporary, _ := filepath.Glob(filepath.Join(filepath.Dir(e.path), ".services.json.tmp-*"))
			if len(temporary) != 0 {
				t.Fatal("temporary leaked")
			}
			if stage != "dirsync" {
				if response.Code != 500 || !bytes.Equal(before, disk) {
					t.Fatal("before-rename failure changed file", response.Code)
				}
			} else {
				if response.Code != 503 || bytes.Equal(before, disk) || !e.disabled || !strings.Contains(response.Body.String(), "uncertain") {
					t.Fatal("uncertain not explicit", response.Code)
				}
				e.failpoint = nil
				response = editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
				if response.Code != 503 {
					t.Fatal("uncertain save not disabled")
				}
				path := e.path
				e.Close()
				restarted, err := newServiceMetadataEditor(path, editorTestConfig())
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				if restarted.disabled || restarted.view.Load().metadata.Overrides[3].Name != "Shared Wiki" {
					t.Fatal("restart not reconciled")
				}
			}
		})
	}
}

func TestServiceMetadataEditorFinalRecheck(t *testing.T) {
	for _, change := range []string{"target", "freshness", "visibility", "external"} {
		t.Run(change, func(t *testing.T) {
			e, cache, handler := editorFixture(t)
			form := editForm(t, handler, "3", "alice@example.com")
			before := append([]byte(nil), e.view.Load().bytes...)
			e.failpoint = func(stage string) error {
				if stage != "recheck" {
					return nil
				}
				data := *cache.Get()
				switch change {
				case "target":
					data.ProxyHosts = append([]ProxyHost(nil), data.ProxyHosts...)
					data.ProxyHosts[2].ForwardPort = 8443
				case "freshness":
					data.UpdatedAt = time.Now().Add(-91 * time.Second)
				case "visibility":
					data.Policy = &Policy{}
				case "external":
					return os.WriteFile(e.path, append(before, '\n'), 0o600)
				}
				cache.data.Store(&data)
				return nil
			}
			response := editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
			expected := map[string]int{"target": 409, "freshness": 503, "visibility": 404, "external": 409}[change]
			if response.Code != expected || !bytes.Equal(before, e.view.Load().bytes) {
				t.Fatal("final eligibility not rechecked", response.Code, response.Body.String())
			}
			disk, _ := os.ReadFile(e.path)
			if change != "external" && !bytes.Equal(before, disk) {
				t.Fatal("denial changed file")
			}
		})
	}
}

func TestServiceMetadataEditorAPINegatives(t *testing.T) {
	e, cache, handler := editorFixture(t)
	form := editForm(t, handler, "3", "alice@example.com")
	valid := editBody(form, "save")
	tests := []struct {
		name, method, id, login, body string
		status                        int
		change                        func(*http.Request)
	}{
		{name: "wrong proxy", method: "GET", id: "3", login: "alice@example.com", status: 403, change: func(r *http.Request) { r.RemoteAddr = "10.1.1.1:1234" }},
		{name: "non editor", method: "GET", id: "3", login: "mallory@example.com", status: 403},
		{name: "padded login", method: "GET", id: "3", login: " alice@example.com", status: 403},
		{name: "case login", method: "GET", id: "3", login: "Alice@example.com", status: 403},
		{name: "short login", method: "GET", id: "3", login: "alice@", status: 403},
		{name: "duplicate login", method: "GET", id: "3", login: "alice@example.com", status: 403, change: func(r *http.Request) { r.Header.Add("Tailscale-User-Login", "alice@example.com") }},
		{name: "case variant duplicate login", method: "GET", id: "3", login: "alice@example.com", status: 403, change: func(r *http.Request) { r.Header["tailscale-user-login"] = []string{"alice@example.com"} }},
		{name: "duplicate optional identity", method: "GET", id: "3", login: "alice@example.com", status: 403, change: func(r *http.Request) { r.Header["Tailscale-User-Name"] = []string{"Alice", "Alice"} }},
		{name: "wrong host", method: "GET", id: "3", login: "alice@example.com", status: 403, change: func(r *http.Request) { r.Host = "other.example" }},
		{name: "invisible", method: "GET", id: "2", login: "alice@example.com", status: 404},
		{name: "missing", method: "GET", id: "999", login: "alice@example.com", status: 404},
		{name: "disabled", method: "GET", id: "4", login: "alice@example.com", status: 404},
		{name: "noncanonical id", method: "GET", id: "03", login: "alice@example.com", status: 404},
		{name: "query", method: "GET", id: "3?leak=1", login: "alice@example.com", status: 404},
		{name: "method", method: "PUT", id: "3", login: "alice@example.com", status: 405},
		{name: "missing origin", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Del("Origin") }},
		{name: "null origin", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Set("Origin", "null") }},
		{name: "wrong origin", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Set("Origin", "https://other.example") }},
		{name: "duplicate origin", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Add("Origin", e.config.Origin) }},
		{name: "cross site", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }},
		{name: "same site", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 403, change: func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") }},
		{name: "content type", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 415, change: func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{name: "duplicate type", method: "POST", id: "3", login: "alice@example.com", body: valid, status: 415, change: func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{name: "oversized", method: "POST", id: "3", login: "alice@example.com", body: strings.Repeat(" ", maxServiceMetadataEditBytes+1), status: 413},
		{name: "duplicate json", method: "POST", id: "3", login: "alice@example.com", body: `{"action":"save","action":"reset"}`, status: 400},
		{name: "noncanonical json", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, `"name"`, `"Name"`, 1), status: 400},
		{name: "unknown json", method: "POST", id: "3", login: "alice@example.com", body: valid[:len(valid)-1] + `,"category":"New"}`, status: 400},
		{name: "trailing json", method: "POST", id: "3", login: "alice@example.com", body: valid + ` {}`, status: 400},
		{name: "null name", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, `"Shared Wiki"`, `null`, 1), status: 400},
		{name: "invalid icon", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, `"generic"`, `"../icon"`, 1), status: 400},
		{name: "wrong token", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, form.CSRF, "forged", 1), status: 403},
		{name: "other identity token", method: "POST", id: "3", login: "bob@example.com", body: valid, status: 403},
		{name: "expired token", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, form.CSRF, e.csrf("alice@example.com", time.Now().Add(-31*time.Minute)), 1), status: 403},
		{name: "future token", method: "POST", id: "3", login: "alice@example.com", body: strings.Replace(valid, form.CSRF, e.csrf("alice@example.com", time.Now().Add(time.Minute)), 1), status: 403},
		{name: "invisible save", method: "POST", id: "2", login: "alice@example.com", body: valid, status: 404},
		{name: "missing save", method: "POST", id: "999", login: "alice@example.com", body: valid, status: 404},
	}
	before := append([]byte(nil), e.view.Load().bytes...)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := editHTTPRequest(handler, test.method, test.id, test.login, test.body, test.change)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Cache-Control"), "no-store") || response.Header().Get("X-Frame-Options") != "DENY" || response.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("privacy headers missing")
			}
			if !bytes.Equal(before, e.view.Load().bytes) {
				t.Fatal("negative changed store")
			}
		})
	}
	for _, data := range []*CacheData{nil, {UpdatedAt: time.Now().Add(-91 * time.Second)}} {
		cache.data.Store(data)
		for _, method := range []string{"GET", "POST"} {
			response := editHTTPRequest(handler, method, "3", "alice@example.com", valid, nil)
			if response.Code != 503 {
				t.Fatal("cold/stale not unavailable", method, response.Code)
			}
		}
	}
}

func TestServiceMetadataEditorAPIPrivacyAndUnchangedPollRevision(t *testing.T) {
	e, cache, handler := editorFixture(t)
	first := editForm(t, handler, "3", "alice@example.com")
	next := *cache.Get()
	next.UpdatedAt = time.Now()
	next.ProxyHosts = append([]ProxyHost(nil), next.ProxyHosts...)
	next.ProxyHosts[2].Meta.NginxOnline = true
	cache.data.Store(&next)
	second := editForm(t, handler, "3", "alice@example.com")
	if first.TargetRevision != second.TargetRevision || first.Revision != second.Revision {
		t.Fatal("unchanged poll conflicted")
	}
	response := editHTTPRequest(handler, "GET", "3", "alice@example.com", "", nil)
	for _, private := range []string{"10.0.0.", "443", "unseen.example", "Tools", "alice@example.com", "proxy_hosts", "forward_host"} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatal("API leaked", private, response.Body.String())
		}
	}
	// Target default domain changed: short-lived form is invalid without persisting
	// a backend fingerprint or generation.
	changed := next
	changed.ProxyHosts = append([]ProxyHost(nil), next.ProxyHosts...)
	changed.ProxyHosts[2].DomainNames = []string{"new.example.com"}
	cache.data.Store(&changed)
	response = editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(first, "save"), nil)
	if response.Code != 409 {
		t.Fatal("changed defaults accepted", response.Code)
	}
	// Origin binding remains in the token independent of request Origin checks.
	e.config.Origin = "http://changed.example:8081"
	if e.validCSRF("alice@example.com", first.CSRF, time.Now()) {
		t.Fatal("origin token not bound")
	}
}

func TestServiceMetadataEditorExactConfigBounds(t *testing.T) {
	logins := make([]string, 32)
	for index := range logins {
		logins[index] = fmt.Sprintf("editor%d@example.com", index)
	}
	logins[0] = strings.Repeat("a", 242) + "@example.com" // 254 bytes.
	for _, count := range []int{32, 33} {
		selected := append([]string(nil), logins...)
		if count == 33 {
			selected = append(selected, "overflow@example.com")
		}
		encoded, _ := json.Marshal(selected)
		config, err := loadServiceMetadataEditorConfig(mapConfigLookup(map[string]string{
			"PORTAL_EDITORS": string(encoded), "PORTAL_PUBLIC_ORIGIN": "http://portal.example:8081",
		}), "/services.json")
		if count == 32 && (err != nil || len(config.Logins) != 32) {
			t.Fatal("exact bounds rejected", err)
		}
		if count == 33 && err == nil {
			t.Fatal("too many editors accepted")
		}
	}
}

func TestServiceMetadataV3BlankMutationPreservesOptionalFields(t *testing.T) {
	metadata, err := parseServiceMetadata([]byte(`{"version":2,"services":[{"proxy_host_id":3,"name":"Old","url":"https://old.example","category":"Tools","order":0},{"proxy_host_id":99,"name":"Unseen","url":null}]}`))
	if err != nil {
		t.Fatal(err)
	}
	document, err := mutateServiceMetadata(metadata.document, 3, "", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := serializeServiceMetadataDocumentV3(document)
	if err != nil {
		t.Fatal(err)
	}
	result, err := parseServiceMetadata(data)
	if err != nil {
		t.Fatal(err)
	}
	override := result.Overrides[3]
	if override.Name != "" || override.URL != "" || override.Icon != "" || override.Category != "Tools" || override.Order == nil || *override.Order != 0 {
		t.Fatal("blank removal changed organization", override)
	}
	if !reflect.DeepEqual(metadata.document.Services[1], result.document.Services[1]) {
		t.Fatal("untouched nullable URL changed")
	}
	name := "Valid"
	document.Services = make([]serviceMetadataEntry, maxServiceMetadataEntries+1)
	for index := range document.Services {
		document.Services[index] = serviceMetadataEntry{ProxyHostID: index + 1, Name: &name}
	}
	if _, err := serializeServiceMetadataDocumentV3(document); err == nil {
		t.Fatal("entry bound not enforced by serializer")
	}
}

func TestServiceMetadataEditorConcurrentWholeFileConflict(t *testing.T) {
	e, _, handler := editorFixture(t)
	alice := editForm(t, handler, "3", "alice@example.com")
	bob := editForm(t, handler, "3", "bob@example.com")
	start := make(chan struct{})
	responses := make(chan int, 2)
	var workers sync.WaitGroup
	for _, candidate := range []struct {
		login string
		form  serviceMetadataEditForm
	}{{"alice@example.com", alice}, {"bob@example.com", bob}} {
		workers.Add(1)
		go func(login string, form serviceMetadataEditForm) {
			defer workers.Done()
			<-start
			responses <- editHTTPRequest(handler, "POST", "3", login, editBody(form, "save"), nil).Code
		}(candidate.login, candidate.form)
	}
	close(start)
	workers.Wait()
	close(responses)
	counts := map[int]int{}
	for response := range responses {
		counts[response]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent forms lost update", counts)
	}
	disk, err := os.ReadFile(e.path)
	if err != nil || !bytes.Equal(disk, e.view.Load().bytes) {
		t.Fatal("confirmed view differs from disk", err)
	}
}

func TestServiceMetadataEditorRejectsReplacedLifetimeLock(t *testing.T) {
	e, _, handler := editorFixture(t)
	form := editForm(t, handler, "3", "alice@example.com")
	before := append([]byte(nil), e.view.Load().bytes...)
	// An uncooperative external actor replaced the name; the writer must not
	// continue through a different lock inode. Only this temp fixture is touched.
	if err := os.Rename(e.path+".lock", e.path+".old-lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.path+".lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	response := editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
	disk, err := os.ReadFile(e.path)
	if response.Code != 500 || err != nil || !bytes.Equal(disk, before) || !bytes.Equal(e.view.Load().bytes, before) {
		t.Fatal("replaced lifetime lock allowed save", response.Code, err)
	}
}

func TestServiceMetadataEditorCSRFExpiresBeforeRename(t *testing.T) {
	e, cache, _ := editorFixture(t)
	clock := time.Now().Truncate(time.Second)
	h := &serviceMetadataEditorHandler{editor: e, cache: cache, staleAfter: 90 * time.Second, now: func() time.Time { return clock }}
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	handler := IdentityMiddleware(trusted, h)
	data := *cache.Get()
	data.UpdatedAt = clock
	cache.data.Store(&data)
	form := editForm(t, handler, "3", "alice@example.com")
	form.CSRF = e.csrf("alice@example.com", clock.Add(-serviceMetadataCSRFDuration))
	before := append([]byte(nil), e.view.Load().bytes...)
	e.failpoint = func(stage string) error {
		if stage == "rename" {
			clock = clock.Add(time.Second)
		}
		return nil
	}
	response := editHTTPRequest(handler, "POST", "3", "alice@example.com", editBody(form, "save"), nil)
	disk, err := os.ReadFile(e.path)
	if response.Code != 403 || err != nil || !bytes.Equal(before, disk) || !bytes.Equal(before, e.view.Load().bytes) {
		t.Fatal("late token expiry misclassified or wrote file", response.Code, err)
	}
}
