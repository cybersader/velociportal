package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStateNamespaceGrammar(t *testing.T) {
	for _, value := range []string{"a", "0", "a_b-c9", strings.Repeat("a", 64)} {
		if !validStateNamespace(value) || !validPersonalNamespace(value) || !validMetadataNamespace(value) {
			t.Fatal("incumbent valid namespace rejected")
		}
	}
	for _, value := range []string{"", "_a", "-a", "Mixed", " padded", "padded ", "a/b", "a.b", "a\n", "a\x00", "é", strings.Repeat("a", 65)} {
		if validStateNamespace(value) || validPersonalNamespace(value) || validMetadataNamespace(value) {
			t.Fatal("noncanonical namespace accepted")
		}
	}
	// Exhaust the single-byte grammar and permitted subsequent characters.
	for c := 0; c < 256; c++ {
		first := string([]byte{byte(c)})
		want := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if validStateNamespace(first) != want || validStateNamespace("a"+first) != (want || c == '_' || c == '-') {
			t.Fatal("namespace grammar drift")
		}
	}
}

func namespaceConfigValues(provider, mode string) map[string]string {
	values := validConfigValues()
	values["CONTROL_PLANE"] = provider
	values["TAILSCALE_OAUTH_CLIENT_ID"] = "client-canary"
	values["TAILSCALE_OAUTH_CLIENT_SECRET"] = "secret-canary"
	if mode != "absent" {
		values["PORTAL_EDIT_MODE"] = mode
	}
	if mode == "shared" || mode == "inferred" {
		delete(values, "PORTAL_EDIT_MODE")
		if mode == "shared" {
			values["PORTAL_EDIT_MODE"] = mode
		}
		values["PORTAL_EDITORS"] = `["alice@example.com"]`
		values["PORTAL_PUBLIC_ORIGIN"] = "http://portal.example:8081"
	}
	return values
}

func TestStateNamespaceConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, provider, mode, personal, namespace string
		metadata, namespaceSet, bad               bool
	}{
		{"headscale legacy absent", "headscale", "absent", "off", "", false, false, false},
		{"tailscale legacy browser", "tailscale", "browser", "off", "", false, false, false},
		{"headscale metadata absent mode", "headscale", "absent", "off", "namespace-canary", true, true, false},
		{"headscale metadata off", "headscale", "off", "off", "namespace-canary", true, true, false},
		{"headscale metadata browser", "headscale", "browser", "off", "namespace-canary", true, true, false},
		{"headscale metadata shared", "headscale", "shared", "off", "namespace-canary", true, true, false},
		{"tailscale metadata absent mode", "tailscale", "absent", "off", "namespace-canary", true, true, false},
		{"tailscale metadata off", "tailscale", "off", "off", "namespace-canary", true, true, false},
		{"tailscale metadata browser", "tailscale", "browser", "off", "namespace-canary", true, true, false},
		{"tailscale metadata shared", "tailscale", "shared", "off", "namespace-canary", true, true, false},
		{"tailscale metadata inferred", "tailscale", "inferred", "off", "namespace-canary", true, true, false},
		{"volatile shared namespace off", "tailscale", "off", "volatile", "namespace-canary", true, true, false},
		{"volatile shared namespace browser", "tailscale", "browser", "volatile", "namespace-canary", true, true, false},
		{"volatile shared namespace shared", "tailscale", "shared", "volatile", "namespace-canary", true, true, false},
		{"volatile legacy", "tailscale", "off", "volatile", "", false, false, false},
		{"durable off", "tailscale", "off", "durable", "namespace-canary", false, true, false},
		{"durable browser", "tailscale", "browser", "durable", "namespace-canary", true, true, false},
		{"durable shared", "tailscale", "shared", "durable", "namespace-canary", true, true, false},
		{"durable missing namespace", "tailscale", "off", "durable", "", true, false, true},
		{"durable empty namespace", "tailscale", "off", "durable", "", true, true, true},
		{"headscale volatile", "headscale", "off", "volatile", "namespace-canary", true, true, true},
		{"headscale durable", "headscale", "shared", "durable", "namespace-canary", true, true, true},
		{"no consumer headscale", "headscale", "off", "off", "namespace-canary", false, true, true},
		{"no consumer tailscale", "tailscale", "browser", "off", "namespace-canary", false, true, true},
		{"volatile is not namespace consumer", "tailscale", "off", "volatile", "namespace-canary", false, true, true},
		{"empty namespace off", "headscale", "off", "off", "", true, true, true},
		{"noncanonical namespace browser", "tailscale", "browser", "off", "Namespace-Canary", true, true, true},
		{"padded namespace shared", "headscale", "shared", "off", " namespace-canary ", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := namespaceConfigValues(tc.provider, tc.mode)
			dir := t.TempDir()
			if tc.metadata {
				values["SERVICE_METADATA_FILE"] = filepath.Join(dir, "unopened-shared-canary.json")
			}
			if tc.namespaceSet {
				values["PORTAL_STATE_NAMESPACE"] = tc.namespace
			}
			if tc.personal != "off" {
				values["PORTAL_PERSONAL_SERVICES_ORIGIN"] = "http://portal.example:8081"
			}
			if tc.personal == "durable" {
				values["PORTAL_PERSONAL_SERVICES_FILE"] = filepath.Join(dir, "unopened-private-canary.json")
			}
			reads := 0
			cfg, err := loadConfigFrom(func(key string) (string, bool, error) {
				if key == "PORTAL_STATE_NAMESPACE" {
					reads++
					if reads != 1 {
						t.Fatal("namespace read more than once")
					}
				}
				v, ok := values[key]
				return v, ok, nil
			})
			if (err != nil) != tc.bad || reads != 1 {
				t.Fatalf("configuration: %v; namespace reads %d", err, reads)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatal("config performed storage work")
			}
			if err != nil {
				for _, canary := range []string{"namespace-canary", "Namespace-Canary", "client-canary", "secret-canary", dir, "unopened-private-canary", "unopened-shared-canary"} {
					if strings.Contains(err.Error(), canary) {
						t.Fatal("config leaked value")
					}
				}
				return
			}
			if cfg.StateNamespace != tc.namespace {
				t.Fatal("global namespace not retained")
			}
			binding := cfg.metadataBinding()
			if binding.Namespace != cfg.StateNamespace || binding.ControlPlane != cfg.ControlPlane {
				t.Fatal("binding differs from common config")
			}
			if cfg.ServiceMetadataEditor != nil && cfg.ServiceMetadataEditor.Binding != binding {
				t.Fatal("editor has different binding")
			}
			if (cfg.ServiceMetadataEditor != nil) != (tc.mode == "shared" || tc.mode == "inferred") {
				t.Fatal("namespace changed writer enablement")
			}
			if (cfg.PersonalServices != nil) != (tc.personal != "off") {
				t.Fatal("namespace changed personal enablement")
			}
			if cfg.PersonalServices != nil {
				want := ""
				if tc.personal == "durable" {
					want = tc.namespace
				}
				if cfg.PersonalServices.Namespace != want {
					t.Fatal("private namespace not isolated")
				}
				if tc.personal == "volatile" {
					s, err := NewPersonalServicesStore(cfg.PersonalServices)
					if err != nil {
						t.Fatal("volatile rejected global namespace", err)
					}
					_ = s.Close()
				}
			}
		})
	}
}

func TestStateNamespaceLookupFailureAndInactiveKeys(t *testing.T) {
	values := namespaceConfigValues("headscale", "off")
	values["SERVICE_METADATA_FILE"] = "/unopened/metadata.json"
	values["PORTAL_EDITORS"] = "invalid-inactive"
	values["PORTAL_PUBLIC_ORIGIN"] = "invalid-inactive"
	_, err := loadConfigFrom(func(key string) (string, bool, error) {
		if key == "PORTAL_STATE_NAMESPACE" {
			return "secret-namespace", true, errors.New("secret-lookup-canary")
		}
		v, ok := values[key]
		return v, ok, nil
	})
	if err == nil || strings.Contains(err.Error(), "secret-") {
		t.Fatal("namespace lookup error ignored or disclosed", err)
	}
	values["PORTAL_STATE_NAMESPACE"] = "valid-ns"
	if cfg, err := loadConfigFrom(mapConfigLookup(values)); err != nil || cfg.ServiceMetadataEditor != nil {
		t.Fatal("off interpreted inactive shared keys", err)
	}
	values["PORTAL_EDIT_MODE"] = "browser"
	for _, key := range []string{"PORTAL_EDITORS", "PORTAL_PUBLIC_ORIGIN"} {
		delete(values, "PORTAL_EDITORS")
		delete(values, "PORTAL_PUBLIC_ORIGIN")
		values[key] = ""
		if _, err := loadConfigFrom(mapConfigLookup(values)); err == nil {
			t.Fatal("browser accepted supplied shared key")
		}
	}
}

func TestStateNamespaceNoWriterOrDisabledRoutes(t *testing.T) {
	for _, mode := range []string{"off", "browser"} {
		t.Run(mode, func(t *testing.T) {
			path := editorTestFile(t, editorTestDocument)
			before, _ := os.ReadFile(path)
			values := namespaceConfigValues("tailscale", mode)
			values["PORTAL_STATE_NAMESPACE"] = "shared-ns"
			values["SERVICE_METADATA_FILE"] = path
			values["PORTAL_MCP_ORIGIN"] = "http://portal.example:8081"
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if err != nil {
				t.Fatal(err)
			}
			e, p, err := newPresentationStores(cfg)
			if err != nil || e != nil || p != nil {
				t.Fatal("namespace/MCP enabled writer or personal store", err)
			}
			mux, err := newServerMux(cfg, newTestCache(standardTestData()), nil, e, p)
			if err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{"/api/service-metadata/3", "/api/personal-services", "/unknown"} {
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, httptest.NewRequest(method, route, nil))
					if w.Code != http.StatusNotFound {
						t.Fatalf("disabled route %s: %d", route, w.Code)
					}
				}
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("startup rewrote legacy metadata")
			}
			if _, err := os.Stat(path + ".lock"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("readonly mode created lock")
			}
		})
	}
	values := namespaceConfigValues("tailscale", "off")
	values["PORTAL_MCP_ORIGIN"] = "http://portal.example:8081"
	cfg, err := loadConfigFrom(mapConfigLookup(values))
	if err != nil || cfg.StateNamespace != "" || cfg.PersonalServices != nil || cfg.ServiceMetadataEditor != nil {
		t.Fatal("MCP implied state", err)
	}
	values["PORTAL_STATE_NAMESPACE"] = "no-consumer"
	if _, err := loadConfigFrom(mapConfigLookup(values)); err == nil {
		t.Fatal("MCP consumed namespace")
	}
}

func TestStateNamespaceDurableBindingAndPreflightUnwind(t *testing.T) {
	for _, tc := range []string{"matching", "mismatched-private", "missing-namespace", "shared-v4-failure"} {
		t.Run(tc, func(t *testing.T) {
			private := durableTestConfig(t, []personalStorageCollection{})
			values := namespaceConfigValues("tailscale", "off")
			values["PORTAL_PERSONAL_SERVICES_ORIGIN"] = private.Origin
			values["PORTAL_PERSONAL_SERVICES_FILE"] = private.File
			values["PORTAL_STATE_NAMESPACE"] = private.Namespace
			if tc == "mismatched-private" {
				values["PORTAL_STATE_NAMESPACE"] = "wrong-ns"
			}
			if tc == "missing-namespace" {
				delete(values, "PORTAL_STATE_NAMESPACE")
			}
			if tc == "shared-v4-failure" {
				values["SERVICE_METADATA_FILE"] = editorTestFile(t, metadataV4Fixture)
			}
			before, _ := os.ReadFile(private.File)
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if tc == "missing-namespace" {
				if err == nil {
					t.Fatal("missing durable binding accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			e, p, err := newPresentationStores(cfg)
			if tc == "matching" {
				if err != nil || p == nil || e != nil {
					t.Fatal("durable common namespace failed", err)
				}
				_ = p.Close()
			} else {
				if err == nil || e != nil || p != nil {
					t.Fatal("mismatch/preflight accepted")
				}
				if strings.Contains(err.Error(), private.File) || strings.Contains(err.Error(), "wrong-ns") {
					t.Fatal("startup leaked private binding")
				}
			}
			after, _ := os.ReadFile(private.File)
			if string(before) != string(after) {
				t.Fatal("startup rewrote private bytes")
			}
			// A shared preflight failure must release the already-acquired private lock.
			if tc == "shared-v4-failure" {
				reopened := durableTestStore(t, private)
				_ = reopened.Close()
			}
		})
	}
}
