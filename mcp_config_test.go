package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const mcpTestOrigin = "http://portal.tailnet.ts.net:8081"

func TestMCPConfig(t *testing.T) {
	for _, test := range []struct {
		name, origin string
		present, bad bool
	}{
		{"absent", "", false, false},
		{"valid", mcpTestOrigin, true, false},
		{"empty", "", true, true},
		{"space", " " + mcpTestOrigin, true, true},
		{"slash", mcpTestOrigin + "/", true, true},
		{"uppercase", "http://Portal.tailnet.ts.net:8081", true, true},
		{"query", mcpTestOrigin + "?", true, true},
		{"default-port", "https://portal.tailnet.ts.net:443", true, true},
		{"userinfo", "http://user@portal.tailnet.ts.net:8081", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := loadMCPConfig(func(string) (string, bool, error) { return test.origin, test.present, nil }, nil, nil)
			if (err != nil) != test.bad || !test.bad && ((config != nil) != test.present) {
				t.Fatalf("config=%v err=%v", config, err)
			}
		})
	}
	lookup := mapConfigLookup(map[string]string{"PORTAL_MCP_ORIGIN": mcpTestOrigin})
	for _, sharedOrigin := range []string{"", mcpTestOrigin, "https://other.example"} {
		for _, personalOrigin := range []string{"", mcpTestOrigin, "https://other.example"} {
			var shared *ServiceMetadataEditorConfig
			var personal *PersonalServicesConfig
			if sharedOrigin != "" {
				shared = &ServiceMetadataEditorConfig{Origin: sharedOrigin}
			}
			if personalOrigin != "" {
				personal = &PersonalServicesConfig{Origin: personalOrigin}
			}
			_, err := loadMCPConfig(lookup, shared, personal)
			bad := sharedOrigin != "" && sharedOrigin != mcpTestOrigin || personalOrigin != "" && personalOrigin != mcpTestOrigin
			if (err != nil) != bad {
				t.Fatalf("shared=%s personal=%s err=%v", sharedOrigin, personalOrigin, err)
			}
		}
	}
	if _, err := loadMCPConfig(func(string) (string, bool, error) { return "", false, errors.New("lookup error") }, nil, nil); err == nil {
		t.Fatal("lookup error lost")
	}
}

func TestMCPLoadConfigIntegration(t *testing.T) {
	for _, provider := range []string{"headscale", "tailscale"} {
		for _, mode := range []string{"off", "browser", "shared"} {
			for _, personal := range []bool{false, true} {
				values := map[string]string{
					"CONTROL_PLANE": provider, "HEADSCALE_URL": "http://headscale.velociportal.internal:8080", "HEADSCALE_API_KEY": "synthetic",
					"TAILSCALE_OAUTH_CLIENT_ID": "synthetic", "TAILSCALE_OAUTH_CLIENT_SECRET": "synthetic",
					"NPM_URL": "http://npm.velociportal.internal:81", "NPM_EMAIL": "test@example.com", "NPM_PASSWORD": "synthetic",
					"TRUSTED_PROXY_CIDR": "127.0.0.1/32", "PORTAL_MCP_ORIGIN": mcpTestOrigin, "PORTAL_EDIT_MODE": mode,
				}
				if mode == "shared" {
					values["PORTAL_EDITORS"] = `["alice@example.com"]`
					values["PORTAL_PUBLIC_ORIGIN"] = mcpTestOrigin
					values["SERVICE_METADATA_FILE"] = "/synthetic/services.json"
				}
				if personal {
					values["PORTAL_PERSONAL_SERVICES_ORIGIN"] = mcpTestOrigin
				}
				cfg, err := loadConfigFrom(mapConfigLookup(values))
				if provider == "headscale" && personal {
					if err == nil {
						t.Fatal("Headscale personal unexpectedly enabled")
					}
					continue
				}
				if err != nil || cfg.MCP == nil || cfg.MCP.Origin != mcpTestOrigin {
					t.Fatalf("provider=%s mode=%s personal=%v cfg=%v err=%v", provider, mode, personal, cfg, err)
				}
				values["PORTAL_MCP_ORIGIN"] = "https://different.example"
				_, err = loadConfigFrom(mapConfigLookup(values))
				if (err != nil) != (mode == "shared" || personal) {
					t.Fatalf("origin cross-check failed: %v", err)
				}
			}
		}
	}
}

func TestMCPProductionMux(t *testing.T) {
	for _, mode := range []serviceEditMode{serviceEditOff, serviceEditBrowser, serviceEditShared} {
		for _, personalEnabled := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				t.Run(string(mode)+"/"+strings.Join([]string{boolText(personalEnabled), boolText(enabled)}, "/"), func(t *testing.T) {
					var editor *ServiceMetadataEditor
					if mode == serviceEditShared {
						var err error
						editorConfig := editorTestConfig()
						editorConfig.Binding = serviceMetadataBinding{ControlPlane: controlPlaneTailscale}
						editor, err = newServiceMetadataEditor(editorTestFile(t, editorTestDocument), editorConfig)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = editor.Close() })
					}
					var personal *PersonalServicesStore
					if personalEnabled {
						personal = personalTestStore(t)
					}
					config := &Config{ControlPlane: controlPlaneTailscale, TrustedProxyCIDR: mcpTrusted(), PollInterval: 30 * time.Second, PortalEditMode: mode}
					if enabled {
						config.MCP = mcpTestConfig()
					}
					data := *standardTestData()
					data.ControlPlane.Provider = controlPlaneTailscale
					mux, err := newServerMux(config, newTestCache(&data), nil, editor, personal)
					if err != nil {
						t.Fatal(err)
					}
					for _, method := range []string{"GET", "HEAD", "OPTIONS", "PUT", "DELETE"} {
						r := mcpTestRequest(``, true)
						r.Method = method
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, r)
						want := http.StatusNotFound
						if enabled {
							want = http.StatusMethodNotAllowed
						}
						if w.Code != want || enabled && w.Header().Get("Allow") != "POST" {
							t.Fatalf("%s got %d", method, w.Code)
						}
					}
					for _, path := range []string{"", "/", "/extra"} {
						r := mcpTestRequest(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, true)
						r.URL.Path = "/mcp" + path
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, r)
						want := http.StatusNotFound
						if enabled && path == "" {
							want = http.StatusOK
						}
						if w.Code != want {
							t.Fatalf("path=%q got=%d %s", path, w.Code, w.Body.String())
						}
					}
					w := httptest.NewRecorder()
					r := mcpTestRequest("", false)
					r.URL.Path = "/"
					r.Method = "GET"
					mux.ServeHTTP(w, r)
					if w.Code != http.StatusOK {
						t.Fatalf("root affected: %d", w.Code)
					}
				})
			}
		}
	}
}

func boolText(value bool) string {
	if value {
		return "on"
	}
	return "off"
}
