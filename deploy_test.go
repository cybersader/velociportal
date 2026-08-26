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
