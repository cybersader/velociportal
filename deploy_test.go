package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestDeploymentPolicyExampleIncludesPortalAccess(t *testing.T) {
	contents, err := os.ReadFile("deploy/policy.hujson.example")
	if err != nil {
		t.Fatalf("read policy example: %v", err)
	}

	var policy Policy
	if err := json.Unmarshal(standardizeHuJSON(contents), &policy); err != nil {
		t.Fatalf("parse policy example: %v", err)
	}

	portalAddress := policy.Hosts["vp-portal-host"]
	if portalAddress == "" {
		t.Fatal("policy example must define vp-portal-host")
	}
	for _, service := range []string{"vp-shared-service", "vp-admin-service"} {
		if policy.Hosts[service] == "" {
			t.Fatalf("policy example must define %s", service)
		}
	}

	for _, rule := range policy.ACLs {
		if rule.Action == "accept" &&
			slices.Contains(rule.Src, "group:vp-shared") &&
			slices.Contains(rule.Dst, "vp-portal-host:8081") {
			return
		}
	}
	t.Fatal("policy example must allow group:vp-shared to reach vp-portal-host:8081")
}

func TestDeploymentProviderEnvironmentExamplesAreExclusiveAndValid(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		provider controlPlaneProvider
		wantKeys []string
	}{
		{
			name:     "headscale",
			path:     "deploy/velociportal.env.example",
			provider: controlPlaneHeadscale,
			wantKeys: []string{"CONTROL_PLANE", "HEADSCALE_API_KEY", "HEADSCALE_URL", "NPM_EMAIL", "NPM_PASSWORD", "NPM_URL", "POLL_INTERVAL", "PORTAL_LOGO_DEFAULT"},
		},
		{
			name:     "tailscale",
			path:     "deploy/velociportal.tailscale.env.example",
			provider: controlPlaneTailscale,
			wantKeys: []string{"CONTROL_PLANE", "NPM_EMAIL", "NPM_PASSWORD", "NPM_URL", "POLL_INTERVAL", "PORTAL_LOGO_DEFAULT", "TAILSCALE_OAUTH_CLIENT_ID", "TAILSCALE_OAUTH_CLIENT_SECRET"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents, err := os.Open(test.path)
			if err != nil {
				t.Fatalf("open environment example: %v", err)
			}
			defer contents.Close()
			values, err := parseEnvFile(contents)
			if err != nil {
				t.Fatalf("parse environment example: %v", err)
			}
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, test.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, test.wantKeys)
			}
			values["TRUSTED_PROXY_CIDR"] = "172.31.255.1/32"
			cfg, err := loadConfigFrom(mapConfigLookup(values))
			if err != nil {
				t.Fatalf("loadConfigFrom() error = %v", err)
			}
			if cfg.ControlPlane != test.provider || !cfg.ControlPlaneExplicit || len(cfg.InactiveControlPlaneKeys) != 0 {
				t.Fatalf("control plane = %q explicit=%t inactive=%v", cfg.ControlPlane, cfg.ControlPlaneExplicit, cfg.InactiveControlPlaneKeys)
			}
			for _, forbidden := range []string{"TAILSCALE_API_URL", "TAILSCALE_API_KEY", "TAILSCALE_ACCESS_TOKEN", "TAILSCALE_TAILNET"} {
				if _, ok := values[forbidden]; ok {
					t.Fatalf("example defines forbidden key %s", forbidden)
				}
			}
		})
	}
}

func TestDeploymentServeExampleTargetsLoopback(t *testing.T) {
	contents, err := os.ReadFile("deploy/tailscale-serve.json.example")
	if err != nil {
		t.Fatalf("read Serve example: %v", err)
	}

	var config struct {
		TCP map[string]struct {
			HTTP bool `json:"HTTP"`
		} `json:"TCP"`
		Web map[string]struct {
			Handlers map[string]struct {
				Proxy string `json:"Proxy"`
			} `json:"Handlers"`
		} `json:"Web"`
	}
	if err := json.Unmarshal(contents, &config); err != nil {
		t.Fatalf("parse Serve example: %v", err)
	}

	if !config.TCP["8081"].HTTP {
		t.Fatal("Serve example must enable HTTP on TCP port 8081")
	}
	portal := config.Web["truenas.tail.home:8081"]
	if target := portal.Handlers["/"].Proxy; target != "http://127.0.0.1:18080" {
		t.Fatalf("Serve example proxy target = %q, want loopback Velociportal port", target)
	}
}
