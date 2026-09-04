package main

import (
	"reflect"
	"testing"
	"time"
)

func mustGrantCapability(t *testing.T, value string) grantIPCapability {
	t.Helper()
	capability, err := parseGrantIPCapability(value)
	if err != nil {
		t.Fatalf("parseGrantIPCapability(%q) error = %v", value, err)
	}
	return capability
}

func machineMatcherFixture(t *testing.T) *CacheData {
	t.Helper()
	return &CacheData{
		ControlPlane: ControlPlaneMetadata{Provider: controlPlaneTailscale},
		Policy: &Policy{
			SSH: SSHPolicy{State: sshPolicySupported, Rules: []SSHRule{{
				Action: "accept",
				Src:    []string{"alice@example.com"},
				Dst:    []string{"tag:server"},
				Users:  []string{"deploy", "root"},
			}}},
			Grants: []GrantRule{{
				Src:            []string{"alice@example.com"},
				BrowserSrc:     []string{"alice@example.com"},
				Dst:            []string{"tag:server"},
				IPCapabilities: []grantIPCapability{mustGrantCapability(t, "tcp:22")},
			}},
		},
		Nodes: []Node{{
			ID: "node-1", Name: " Server.Tailnet.TS.Net. ", OwnerLogin: "service@example.com",
			Tags: []string{"tag:server"}, Addresses: []string{"100.64.0.10"},
		}},
		GrantRoleSelectorsByLogin: map[string][]string{
			"alice@example.com": {"autogroup:member"},
		},
	}
}

func TestEvaluateMachinesRequiresSeparateSSHandGrantTCP22Evidence(t *testing.T) {
	data := machineMatcherFixture(t)
	data.ProxyHosts = []ProxyHost{{ID: 99, Enabled: false}}
	data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{99: {Name: "must not matter"}}}

	matches := evaluateMachines(&Identity{Login: "alice@example.com"}, data)
	if len(matches) != 1 {
		t.Fatalf("evaluateMachines() = %#v", matches)
	}
	match := matches[0]
	if got, want := match.Card, (MachineCard{
		ID: "node-1", Name: "server.tailnet.ts.net", Target: "server.tailnet.ts.net",
		Access: []MachineAccess{{User: "deploy", Action: "accept"}, {User: "root", Action: "accept"}},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("card = %#v, want %#v", got, want)
	}
	if match.GrantRuleIndex != 0 || match.GrantSourceToken != "alice@example.com" || match.GrantDestination.Kind != destinationMatchTag || match.GrantDestination.ResolvedValue != "100.64.0.10" {
		t.Fatalf("Grant evidence = %#v", match)
	}
	if len(match.SSHAccess) != 2 || match.SSHAccess[0].RuleIndex != 0 || match.SSHAccess[0].SourceToken != "alice@example.com" || match.SSHAccess[0].DestinationToken != "tag:server" {
		t.Fatalf("SSH evidence = %#v", match.SSHAccess)
	}
	if got := MatchMachines(&Identity{Login: "alice@example.com"}, data); !reflect.DeepEqual(got, []MachineCard{match.Card}) {
		t.Fatalf("MatchMachines() = %#v", got)
	}

	tests := map[string]func(*CacheData){
		"SSH absent": func(snapshot *CacheData) {
			snapshot.Policy.SSH = SSHPolicy{State: sshPolicyAbsent}
		},
		"SSH unsupported": func(snapshot *CacheData) {
			snapshot.Policy.SSH = SSHPolicy{State: sshPolicyUnsupported}
		},
		"Grant absent even with legacy ACL TCP22 shape": func(snapshot *CacheData) {
			snapshot.Policy.Grants = nil
			snapshot.Policy.ACLs = []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"tag:server:22"}}}
		},
		"Grant wrong port": func(snapshot *CacheData) {
			snapshot.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "tcp:443")}
		},
		"Grant non-TCP": func(snapshot *CacheData) {
			snapshot.Policy.Grants[0].IPCapabilities = []grantIPCapability{mustGrantCapability(t, "udp:22")}
		},
		"SSH destination misses node": func(snapshot *CacheData) {
			snapshot.Policy.SSH.Rules[0].Dst = []string{"tag:other"}
		},
		"Grant destination misses node": func(snapshot *CacheData) {
			snapshot.Policy.Grants[0].Dst = []string{"100.64.0.20"}
		},
		"node has no validated tailnet address": func(snapshot *CacheData) {
			snapshot.Nodes[0].Addresses = []string{"10.0.0.10", "fd00::10"}
		},
		"Headscale provider": func(snapshot *CacheData) {
			snapshot.ControlPlane.Provider = controlPlaneHeadscale
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := machineMatcherFixture(t)
			mutate(snapshot)
			if cards := MatchMachines(&Identity{Login: "alice@example.com"}, snapshot); len(cards) != 0 {
				t.Fatalf("MatchMachines() = %#v, want none", cards)
			}
		})
	}
}

func TestEvaluateMachinesPreservesExactIdentityRolesSelfAndMachineSourceNegatives(t *testing.T) {
	tcp22 := mustGrantCapability(t, "tcp:22")
	data := &CacheData{
		ControlPlane: ControlPlaneMetadata{Provider: controlPlaneTailscale},
		Policy: &Policy{
			Groups: map[string][]string{"group:ops": {"member@example.com"}},
			SSH: SSHPolicy{State: sshPolicySupported, Rules: []SSHRule{
				{Action: "accept", Src: []string{"group:ops"}, Dst: []string{"tag:group"}, Users: []string{"group-user"}},
				{Action: "accept", Src: []string{"autogroup:admin"}, Dst: []string{"tag:admin"}, Users: []string{"admin-user"}},
				{Action: "accept", Src: []string{"autogroup:owner"}, Dst: []string{"tag:owner"}, Users: []string{"owner-user"}},
				{Action: "accept", Src: []string{"autogroup:it-admin"}, Dst: []string{"tag:it"}, Users: []string{"it-user"}},
				{Action: "accept", Src: []string{"member@example.com"}, Dst: []string{"autogroup:self"}, Users: []string{"self-user"}},
				{Action: "accept", Src: []string{"member@example.com"}, Dst: []string{"tag:machine-source"}, Users: []string{"machine-user"}},
			}},
			Grants: []GrantRule{
				{Src: []string{"group:ops"}, BrowserSrc: []string{"group:ops"}, Dst: []string{"tag:group"}, IPCapabilities: []grantIPCapability{tcp22}},
				{Src: []string{"autogroup:admin"}, BrowserSrc: []string{"autogroup:admin"}, Dst: []string{"tag:admin"}, IPCapabilities: []grantIPCapability{tcp22}},
				{Src: []string{"autogroup:owner"}, BrowserSrc: []string{"autogroup:owner"}, Dst: []string{"tag:owner"}, IPCapabilities: []grantIPCapability{tcp22}},
				{Src: []string{"autogroup:it-admin"}, BrowserSrc: []string{"autogroup:it-admin"}, Dst: []string{"tag:it"}, IPCapabilities: []grantIPCapability{tcp22}},
				{Src: []string{"member@example.com"}, BrowserSrc: []string{"member@example.com"}, Dst: []string{"autogroup:self"}, IPCapabilities: []grantIPCapability{tcp22}},
				{Src: []string{"tag:client"}, BrowserSrc: nil, Dst: []string{"tag:machine-source"}, IPCapabilities: []grantIPCapability{tcp22}},
			},
		},
		Nodes: []Node{
			{ID: "1", Name: "group.tailnet.ts.net", Tags: []string{"tag:group"}, Addresses: []string{"100.64.0.1"}},
			{ID: "2", Name: "admin.tailnet.ts.net", Tags: []string{"tag:admin"}, Addresses: []string{"100.64.0.2"}},
			{ID: "3", Name: "owner.tailnet.ts.net", Tags: []string{"tag:owner"}, Addresses: []string{"100.64.0.3"}},
			{ID: "4", Name: "it.tailnet.ts.net", Tags: []string{"tag:it"}, Addresses: []string{"100.64.0.4"}},
			{ID: "5", Name: "self.tailnet.ts.net", OwnerLogin: "member@example.com", Addresses: []string{"100.64.0.5"}},
			{ID: "6", Name: "other-self.tailnet.ts.net", OwnerLogin: "member@other.example", Addresses: []string{"100.64.0.6"}},
			{ID: "7", Name: "short-self.tailnet.ts.net", OwnerLogin: "member@", Addresses: []string{"100.64.0.7"}},
			{ID: "8", Name: "machine-source.tailnet.ts.net", Tags: []string{"tag:machine-source"}, Addresses: []string{"100.64.0.8"}},
		},
		GrantRoleSelectorsByLogin: map[string][]string{
			"owner@example.com":  {"autogroup:admin", "autogroup:member", "autogroup:owner"},
			"admin@example.com":  {"autogroup:admin", "autogroup:member"},
			"member@example.com": {"autogroup:member"},
			"it@example.com":     {"autogroup:it-admin", "autogroup:member"},
		},
	}

	tests := map[string]struct {
		login string
		want  []string
	}{
		"owner gets only explicit owner and automatic admin roles": {login: "owner@example.com", want: []string{"admin.tailnet.ts.net", "owner.tailnet.ts.net"}},
		"admin does not inherit owner or specialized roles":        {login: "admin@example.com", want: []string{"admin.tailnet.ts.net"}},
		"specialized role stays isolated":                          {login: "it@example.com", want: []string{"it.tailnet.ts.net"}},
		"member gets exact group and exact self only":              {login: "member@example.com", want: []string{"group.tailnet.ts.net", "self.tailnet.ts.net"}},
		"shared user gets no machines":                             {login: "shared@example.com", want: []string{}},
		"case variant gets no machines":                            {login: "Member@example.com", want: []string{}},
		"short identity gets no machines":                          {login: "member@", want: []string{}},
		"bare identity gets no machines":                           {login: "member", want: []string{}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cards := MatchMachines(&Identity{Login: test.login}, data)
			got := make([]string, len(cards))
			for index, card := range cards {
				got[index] = card.Name
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("machine names = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEvaluateMachineSSHAccessUsesRestrictiveCheckPrecedence(t *testing.T) {
	ids := map[string]bool{"alice@example.com": true}
	node := Node{OwnerLogin: "alice@example.com"}

	t.Run("check replaces accept for the same literal user", func(t *testing.T) {
		rules := []SSHRule{
			{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"autogroup:self"}, Users: []string{"autogroup:nonroot", "deploy", "root"}},
			{Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"autogroup:self"}, Users: []string{"deploy"}, CheckPeriod: 30 * time.Minute},
		}
		access := evaluateMachineSSHAccess(node, rules, ids, nil, "alice@example.com")
		if len(access) != 3 {
			t.Fatalf("access = %#v", access)
		}
		byUser := make(map[string]machineSSHAccessEvidence)
		for _, evidence := range access {
			byUser[evidence.Access.User] = evidence
		}
		if deploy := byUser["deploy"]; deploy.Access.Action != "check" || deploy.Access.CheckPeriod != 30*time.Minute || deploy.RuleIndex != 1 {
			t.Fatalf("deploy evidence = %#v", deploy)
		}
		if byUser[machineNonrootSelector].Access.Action != "accept" || byUser["root"].Access.Action != "accept" {
			t.Fatalf("remaining access = %#v", byUser)
		}
	})

	t.Run("nonroot check dominates literal nonroot accepts but not explicit root", func(t *testing.T) {
		rules := []SSHRule{
			{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"autogroup:self"}, Users: []string{"deploy", "root"}},
			{Action: "check", Src: []string{"alice@example.com"}, Dst: []string{"autogroup:self"}, Users: []string{"autogroup:nonroot"}, CheckPeriod: time.Hour},
		}
		access := evaluateMachineSSHAccess(node, rules, ids, nil, "alice@example.com")
		if len(access) != 2 {
			t.Fatalf("access = %#v", access)
		}
		if access[0].Access.User != machineNonrootSelector || access[0].Access.Action != "check" || access[1].Access.User != "root" || access[1].Access.Action != "accept" {
			t.Fatalf("access = %#v", access)
		}
	})
}

func TestMachineConsoleEligibleRequiresTailscaleDirectMemberWithConsoleRole(t *testing.T) {
	baseData := func() *CacheData {
		return &CacheData{
			ControlPlane: ControlPlaneMetadata{Provider: controlPlaneTailscale},
			Policy:       &Policy{SSH: SSHPolicy{State: sshPolicySupported}},
			GrantRoleSelectorsByLogin: map[string][]string{
				"owner@example.com":   {"autogroup:admin", "autogroup:member", "autogroup:owner"},
				"admin@example.com":   {"autogroup:admin", "autogroup:member"},
				"it@example.com":      {"autogroup:it-admin", "autogroup:member"},
				"network@example.com": {"autogroup:network-admin", "autogroup:member"},
				"billing@example.com": {"autogroup:billing-admin", "autogroup:member"},
				"auditor@example.com": {"autogroup:auditor", "autogroup:member"},
				"member@example.com":  {"autogroup:member"},
			},
		}
	}

	tests := map[string]struct {
		login string
		want  bool
	}{
		"owner is eligible via automatic admin role": {login: "owner@example.com", want: true},
		"admin is eligible":                          {login: "admin@example.com", want: true},
		"it-admin is eligible":                       {login: "it@example.com", want: true},
		"network-admin is eligible":                  {login: "network@example.com", want: true},
		"billing-admin is not eligible":              {login: "billing@example.com", want: false},
		"auditor is not eligible":                    {login: "auditor@example.com", want: false},
		"plain member is not eligible":               {login: "member@example.com", want: false},
		"shared user is not eligible":                {login: "shared@example.com", want: false},
		"case variant is not eligible":               {login: "Owner@example.com", want: false},
		"blank login is not eligible":                {login: "", want: false},
		"whitespace-padded login is not eligible":    {login: "  owner@example.com  ", want: false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := machineConsoleEligible(test.login, baseData()); got != test.want {
				t.Fatalf("machineConsoleEligible(%q) = %t, want %t", test.login, got, test.want)
			}
		})
	}

	t.Run("Headscale provider is never eligible regardless of role", func(t *testing.T) {
		data := baseData()
		data.ControlPlane.Provider = controlPlaneHeadscale
		if machineConsoleEligible("owner@example.com", data) {
			t.Fatal("Headscale must never be console-eligible")
		}
	})

	t.Run("unavailable SSH projection is never eligible", func(t *testing.T) {
		data := baseData()
		data.Policy = &Policy{SSH: SSHPolicy{State: sshPolicyUnsupported}}
		if machineConsoleEligible("owner@example.com", data) {
			t.Fatal("unsupported SSH projection must never be console-eligible")
		}
	})

	t.Run("nil data is never eligible", func(t *testing.T) {
		if machineConsoleEligible("owner@example.com", nil) {
			t.Fatal("nil snapshot must never be console-eligible")
		}
	})
}

func TestMachineConsoleURLBuildsFixedFilteredMachinesLinkFromValidatedTargetsOnly(t *testing.T) {
	tests := map[string]struct {
		target string
		want   string
		ok     bool
	}{
		"canonical ts.net name": {
			target: "server.tailnet.ts.net",
			want:   "https://console.tailscale.com/admin/machines?q=server.tailnet.ts.net",
			ok:     true,
		},
		"Tailscale CGNAT IPv4": {
			target: "100.64.0.10",
			want:   "https://console.tailscale.com/admin/machines?q=100.64.0.10",
			ok:     true,
		},
		"Tailscale ULA IPv6": {
			target: "fd7a:115c:a1e0::13",
			want:   "https://console.tailscale.com/admin/machines?q=fd7a%3A115c%3Aa1e0%3A%3A13",
			ok:     true,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := machineConsoleURL(test.target)
			if got != test.want || ok != test.ok {
				t.Fatalf("machineConsoleURL(%q) = %q, %t; want %q, %t", test.target, got, ok, test.want, test.ok)
			}
		})
	}

	for name, target := range map[string]string{
		"single-label host is not a validated target":          "server",
		"public non-MagicDNS domain is not a validated target": "public.example.com",
		"ordinary private IPv4 is not a validated target":      "192.168.1.10",
		"whitespace changes the byte-identical target":         " server.tailnet.ts.net",
		"query metacharacters do not smuggle a new target":     "server.tailnet.ts.net&q=evil",
		"empty target is never valid":                          "",
	} {
		t.Run(name, func(t *testing.T) {
			if got, ok := machineConsoleURL(target); ok || got != "" {
				t.Fatalf("machineConsoleURL(%q) = %q, %t; want rejection", target, got, ok)
			}
		})
	}
}

func TestMachineTargetUsesCanonicalNameOrNarrowTailnetAddressFallback(t *testing.T) {
	tests := map[string]struct {
		node Node
		want string
		ok   bool
	}{
		"canonical FQDN preferred": {
			node: Node{Name: " Host.Tailnet.TS.Net. ", Addresses: []string{"100.64.0.10"}},
			want: "host.tailnet.ts.net", ok: true,
		},
		"unsafe name falls back to Tailscale IPv4": {
			node: Node{Name: "-oProxyCommand=bad", Addresses: []string{"192.168.1.5", "100.64.0.11"}},
			want: "100.64.0.11", ok: true,
		},
		"non-MagicDNS FQDN falls back to Tailscale IPv4": {
			node: Node{Name: "public.example.com", Addresses: []string{"100.64.0.14"}},
			want: "100.64.0.14", ok: true,
		},
		"IPv4 fallback is preferred over Tailscale IPv6": {
			node: Node{Addresses: []string{"fd7a:115c:a1e0::12", "100.64.0.12"}},
			want: "100.64.0.12", ok: true,
		},
		"Tailscale IPv6 is a supported final fallback": {
			node: Node{Name: "single-label", Addresses: []string{"fd7a:115c:a1e0::13"}},
			want: "fd7a:115c:a1e0::13", ok: true,
		},
		"ordinary private address is not a target fallback": {
			node: Node{Name: "unsafe target", Addresses: []string{"10.0.0.10", "fd00::1"}},
			ok:   false,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, ok := machineTarget(test.node)
			if got != test.want || ok != test.ok {
				t.Fatalf("machineTarget() = %q, %v; want %q, %v", got, ok, test.want, test.ok)
			}
		})
	}
}
