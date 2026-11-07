package main

import (
	"errors"
	"strings"
	"testing"
)

func TestPersonalConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, provider, mode, origin string
		present, bad                 bool
	}{
		{"absent headscale", "headscale", "off", "", false, false},
		{"absent tailscale", "tailscale", "browser", "", false, false},
		{"enabled off", "tailscale", "off", "http://portal.example:8081", true, false},
		{"enabled browser", "tailscale", "browser", "https://portal.tailnet.ts.net", true, false},
		{"explicit empty", "tailscale", "off", "", true, true},
		{"wrong provider", "headscale", "off", "http://portal.example:8081", true, true},
		{"padded", "tailscale", "off", " https://portal.example", true, true},
		{"path", "tailscale", "off", "https://portal.example/", true, true},
		{"userinfo", "tailscale", "off", "https://user@portal.example", true, true},
		{"query", "tailscale", "off", "https://portal.example?secret=do-not-leak", true, true},
		{"case", "tailscale", "off", "https://PORTAL.example", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := validConfigValues()
			values["CONTROL_PLANE"] = test.provider
			values["PORTAL_EDIT_MODE"] = test.mode
			values["TAILSCALE_OAUTH_CLIENT_ID"] = "fixture-client"
			values["TAILSCALE_OAUTH_CLIENT_SECRET"] = "fixture-secret"
			if test.present {
				values["PORTAL_PERSONAL_SERVICES_ORIGIN"] = test.origin
			}
			cfg, err := loadConfigFrom(func(key string) (string, bool, error) { value, ok := values[key]; return value, ok, nil })
			if (err != nil) != test.bad {
				t.Fatalf("configuration err=%v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "do-not-leak") {
					t.Fatal("config error leaked URL")
				}
				return
			}
			if (cfg.PersonalServices != nil) != test.present || cfg.ServiceMetadataEditor != nil || cfg.ServiceMetadataFile != "" {
				t.Fatal("feature coupled to shared writer/storage")
			}
			store, err := NewPersonalServicesStore(cfg.PersonalServices)
			if err != nil || (store != nil) != test.present {
				t.Fatal("store enablement", err)
			}
		})
	}
	_, err := loadPersonalServicesConfig(func(string) (string, bool, error) { return "", false, errors.New("lookup unavailable") }, controlPlaneTailscale)
	if err == nil {
		t.Fatal("lookup error ignored")
	}
}
