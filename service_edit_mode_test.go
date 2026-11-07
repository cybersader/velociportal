package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServiceEditModeConfiguration(t *testing.T) {
	for _, test := range []struct {
		name        string
		values      map[string]string
		mode        serviceEditMode
		writer, bad bool
	}{
		{"absent disabled", nil, serviceEditOff, false, false},
		{"absent legacy shared", map[string]string{"PORTAL_EDITORS": `["alice@example.com"]`, "PORTAL_PUBLIC_ORIGIN": "http://portal.example:8081", "SERVICE_METADATA_FILE": "/unused/metadata.json"}, serviceEditShared, true, false},
		{"absent legacy empty", map[string]string{"PORTAL_EDITORS": "", "PORTAL_PUBLIC_ORIGIN": ""}, "", false, true},
		{"browser no file", map[string]string{"PORTAL_EDIT_MODE": "browser"}, serviceEditBrowser, false, false},
		{"browser readonly defaults", map[string]string{"PORTAL_EDIT_MODE": "browser", "SERVICE_METADATA_FILE": "/unused/metadata.json"}, serviceEditBrowser, false, false},
		{"browser editors conflict", map[string]string{"PORTAL_EDIT_MODE": "browser", "PORTAL_EDITORS": ""}, "", false, true},
		{"browser origin conflict", map[string]string{"PORTAL_EDIT_MODE": "browser", "PORTAL_PUBLIC_ORIGIN": ""}, "", false, true},
		{"off ignores bad shared", map[string]string{"PORTAL_EDIT_MODE": "off", "PORTAL_EDITORS": "do-not-leak", "PORTAL_PUBLIC_ORIGIN": "do-not-leak"}, serviceEditOff, false, false},
		{"shared requires settings", map[string]string{"PORTAL_EDIT_MODE": "shared"}, "", false, true},
		{"explicit shared", map[string]string{"PORTAL_EDIT_MODE": "shared", "PORTAL_EDITORS": `["alice@example.com"]`, "PORTAL_PUBLIC_ORIGIN": "http://portal.example:8081", "SERVICE_METADATA_FILE": "/unused/metadata.json"}, serviceEditShared, true, false},
		{"empty explicit", map[string]string{"PORTAL_EDIT_MODE": ""}, "", false, true},
		{"padded explicit", map[string]string{"PORTAL_EDIT_MODE": " browser "}, "", false, true},
		{"case explicit", map[string]string{"PORTAL_EDIT_MODE": "Browser"}, "", false, true},
		{"unknown explicit", map[string]string{"PORTAL_EDIT_MODE": "automatic"}, "", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := validConfigValues()
			for k, v := range test.values {
				values[k] = v
			}
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if (err != nil) != test.bad {
				t.Fatalf("error=%v bad=%t", err, test.bad)
			}
			if err != nil {
				if strings.Contains(err.Error(), "do-not-leak") {
					t.Fatal("setting value leaked")
				}
				return
			}
			if cfg.PortalEditMode != test.mode || (cfg.ServiceMetadataEditor != nil) != test.writer {
				t.Fatalf("resolved mode/writer mismatch: %q", cfg.PortalEditMode)
			}
		})
	}
}

func TestServiceEditModeOffDoesNotReadInactiveSettings(t *testing.T) {
	lookup := func(key string) (string, bool, error) {
		if key != "PORTAL_EDIT_MODE" {
			t.Fatal("off interpreted inactive shared settings")
		}
		return "off", true, nil
	}
	mode, shared, err := resolveServiceEditMode(lookup, "unused")
	if err != nil || mode != serviceEditOff || shared != nil {
		t.Fatalf("inactive settings resolution: %v", err)
	}
}

// These cases are mirrored in the JS harness. Both validators reject unsafe
// fields; the browser intentionally accepts a narrower URL syntax because Go
// url.Parse and WHATWG have different normalization rules.
func TestServiceEditModeValidatorSafetyReference(t *testing.T) {
	for _, raw := range []string{"http://example.test", "https://example.test/path?q=ok#fragment", "https://[fd7a:115c:a1e0::1]:8443/path", "https://example.test/%E2%98%83", "https://EVIL.test", "https://127.1", "https://example.test:443", "https://example.test/a/../b", "https://example.test:99999"} {
		if _, err := validateServiceMetadataURL(raw); err != nil {
			t.Errorf("Go-supported reference URL rejected: %q", raw)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "//evil.test", "https://user:pass@evil.test", " https://evil.test", "https://evil.test\n/a", "https://*.evil.test", "https://evil.test/" + strings.Repeat("a", 2048)} {
		if _, err := validateServiceMetadataURL(raw); err == nil {
			t.Errorf("unsafe shared/browser reference accepted: %q", raw)
		}
	}
	if _, err := validateServiceMetadataName(strings.Repeat("😀", 120)); err != nil {
		t.Fatal("120 Unicode code point name rejected")
	}
	for _, raw := range []string{" padded", "x\u0085y", strings.Repeat("😀", 121), string([]byte{0xed, 0xa0, 0x80})} {
		if _, err := validateServiceMetadataName(raw); err == nil {
			t.Fatal("unsafe Unicode reference accepted")
		}
	}
}

func TestServiceEditModeEncodedProcessEnvironment(t *testing.T) {
	for k, v := range validConfigValues() {
		t.Setenv(k, strconv.Quote(v))
	}
	t.Setenv(processEnvEncodingKey, goQuotedEnvEncoding)
	t.Setenv("PORTAL_EDIT_MODE", strconv.Quote("browser"))
	cfg, err := loadConfig()
	if err != nil || cfg.PortalEditMode != serviceEditBrowser || cfg.ServiceMetadataEditor != nil {
		t.Fatalf("encoded mode error: %v", err)
	}
	t.Setenv("PORTAL_EDIT_MODE", strconv.Quote(""))
	if _, err := loadConfig(); err == nil {
		t.Fatal("explicit encoded empty silently inferred")
	}
}

func TestServiceEditModeNoWriterOrRoute(t *testing.T) {
	for _, mode := range []serviceEditMode{serviceEditBrowser, serviceEditOff} {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "not-created.json")
			_, configured, err := resolveServiceEditMode(mapConfigLookup(map[string]string{"PORTAL_EDIT_MODE": string(mode)}), path)
			if err != nil {
				t.Fatal(err)
			}
			e, err := newServiceMetadataEditor(path, configured)
			if err != nil || e != nil {
				t.Fatalf("inactive mode initialized writer: %v", err)
			}
			mux := http.NewServeMux()
			mountServiceMetadataEditor(mux, e, nil, nil, time.Second)
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, httptest.NewRequest(method, "/api/service-metadata/3", nil))
				if w.Code != http.StatusNotFound {
					t.Fatalf("edit route mounted: %d", w.Code)
				}
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("inactive mode created file or lock")
			}
		})
	}
}

func TestServiceEditModeBrowserDoctorNoStorageProbe(t *testing.T) {
	fixture := newDoctorHTTPFixture(t)
	values := doctorFixtureConfig(fixture)
	values["PORTAL_EDIT_MODE"] = "browser"
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.env")
	if err := writeEnvFile(path, values); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runDoctorForTest([]string{"--env-file", path}, fixture)
	if code != 0 || !strings.Contains(stdout, "PASS service editor: browser-local mode (no shared writer or storage checks)") {
		t.Fatalf("browser Doctor code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "fixture.env" {
		t.Fatal("browser Doctor created storage or lock")
	}
}

func TestServiceEditModeProviderExamplesSupportBrowserWithoutFile(t *testing.T) {
	for _, path := range []string{"deploy/velociportal.env.example", "deploy/velociportal.tailscale.env.example"} {
		t.Run(path, func(t *testing.T) {
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			values, err := parseEnvFile(file)
			if err != nil {
				t.Fatal(err)
			}
			values["TRUSTED_PROXY_CIDR"] = "172.31.255.1/32"
			values["PORTAL_EDIT_MODE"] = "browser"
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if err != nil || cfg.PortalEditMode != serviceEditBrowser || cfg.ServiceMetadataEditor != nil || cfg.ServiceMetadataFile != "" {
				t.Fatalf("browser requires no shared storage: %v", err)
			}
		})
	}
}

func TestServiceEditModeBrowserAuthorizedDefaults(t *testing.T) {
	metadata, err := parseServiceMetadata([]byte(`{"version":3,"services":[{"proxy_host_id":3,"name":"Metadata <name>","url":"https://custom.example.test/path?a=1&b=2","icon":"grafana","category":"Tools","order":5}]}`))
	if err != nil {
		t.Fatal(err)
	}
	data := standardTestData()
	data.ServiceMetadata = metadata
	portal := NewPortalHandler(newTestCache(data))
	portal.editMode = serviceEditBrowser
	_, trusted, _ := net.ParseCIDR("127.0.0.1/32")
	handler := IdentityMiddleware(trusted, portal)
	for _, login := range []string{"alice@example.com", "bob@example.com"} {
		r := httptest.NewRequest(http.MethodGet, "http://unconfigured-origin.example/portal", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Tailscale-User-Login", login)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `data-service-edit-mode="browser"`) || !strings.Contains(body, `Saved in this browser.`) {
			t.Fatal("trusted viewer browser editor unavailable")
		}
		if strings.Contains(body, "Save for everyone") || strings.Contains(body, "service-metadata-saved from:body") {
			t.Fatal("shared copy/refresh or unauthorized service exposed")
		}
		if strings.Index(body, "/static/service-editor-browser.js") > strings.Index(body, "/static/service-editor.js") {
			t.Fatal("browser adapter must precede controller")
		}
		matched := map[int]bool{}
		for _, card := range MatchServices(&Identity{Login: login}, data) {
			matched[card.ID] = true
		}
		for _, host := range data.ProxyHosts {
			if strings.Contains(body, `data-service-id="`+strconv.Itoa(host.ID)+`"`) != matched[host.ID] {
				t.Fatal("browser mode changed authorized service set")
			}
		}
		if login == "alice@example.com" && (!strings.Contains(body, `data-default-name="Metadata &lt;name&gt;"`) || !strings.Contains(body, `data-default-url="https://custom.example.test/path?a=1&amp;b=2"`) || !strings.Contains(body, `data-default-icon="grafana"`)) {
			t.Fatal("current resolved defaults missing or unescaped")
		}
		if (login == "alice@example.com" && !strings.Contains(body, `href="https://custom.example.test/path?a=1&amp;b=2"`)) || !strings.Contains(body, `maxlength="240"`) {
			t.Fatal("no-JS link/Unicode bound lost")
		}
		if !strings.Contains(body, `data-identity-scope="`+logoPreferenceScope(login)+`"`) {
			t.Fatal("exact opaque scope missing")
		}
	}
	var shared bytes.Buffer
	if err := renderPortalWithOptions(&shared, &Identity{Login: "alice@example.com"}, MatchServices(&Identity{Login: "alice@example.com"}, data), portalRenderOptions{ServiceEditing: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(shared.String(), "service-editor-browser.js") || strings.Contains(shared.String(), "data-default-name=") {
		t.Fatal("browser adapter/defaults leaked into shared mode")
	}
}
