package main

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePolicyDocumentSupportsLegacyACLSubset(t *testing.T) {
	raw := []byte(`{
		"groups":{"group:admin":["alice@example.com"]},
		"tagOwners":{"tag:app":["group:admin"]},
		"hosts":{"app":"10.0.0.10"},
		"acls":[{"action":"accept","src":["group:admin"],"dst":["app:443"],"proto":"tcp"}],
		"ssh":[{"action":"check","src":["group:admin"],"dst":["autogroup:self"],"users":["autogroup:nonroot"]}],
		"autoApprovers":{},"tests":[],"sshTests":[],"derpMap":{},"disableIPv4":false,"oneCGNATRoute":true
	}`)

	validated, err := validatePolicyDocument(raw)
	if err != nil {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
	if !validated.SSHPresent {
		t.Fatal("SSHPresent = false, want true")
	}
	if len(validated.Policy.ACLs) != 1 || validated.Policy.ACLs[0].Dst[0] != "app:443" {
		t.Fatalf("policy ACLs = %#v", validated.Policy.ACLs)
	}
	if validated.Policy.Hosts["app"] != "10.0.0.10" {
		t.Fatalf("policy hosts = %#v", validated.Policy.Hosts)
	}
}

func TestValidatePolicyDocumentSupportsSafeGrantsAndNodeAttrs(t *testing.T) {
	raw := []byte(`{
		"groups":{"group:admin":["alice@example.com"]},
		"hosts":{"wiki":"10.0.0.20"},
		"acls":[{"action":"accept","src":["group:admin"],"dst":["10.0.0.10:443"]}],
		"grants":[
			{"src":["group:admin"],"dst":["tag:app"],"ip":["tcp:443","udp:53"],"app":{},"srcPosture":[],"via":[]},
			{"src":["tag:client"],"dst":["wiki"],"ip":["*"]},
			{"src":["autogroup:member"],"dst":["10.0.0.0/24"],"ip":["80-90"]},
			{"src":["autogroup:member"],"dst":["autogroup:self"],"ip":["tcp:443"]}
		],
		"nodeAttrs":[{"target":["autogroup:member"],"attr":["funnel"],"app":{}}]
	}`)

	validated, err := validatePolicyDocument(raw)
	if err != nil {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
	if validated.PolicyMode != networkAccessVisibilityV1 || !validated.NodeAttrsPresent {
		t.Fatalf("validated metadata = %#v", validated)
	}
	if len(validated.Policy.ACLs) != 1 || len(validated.Policy.Grants) != 4 {
		t.Fatalf("policy rules = %#v", validated.Policy)
	}
	first := validated.Policy.Grants[0]
	if len(first.BrowserSrc) != 1 || first.BrowserSrc[0] != "group:admin" ||
		len(validated.Policy.Grants[1].BrowserSrc) != 0 ||
		len(validated.Policy.Grants[2].BrowserSrc) != 1 || validated.Policy.Grants[2].BrowserSrc[0] != "autogroup:member" ||
		len(validated.Policy.Grants[3].BrowserSrc) != 1 || validated.Policy.Grants[3].BrowserSrc[0] != "autogroup:member" {
		t.Fatalf("grant browser sources = %#v", validated.Policy.Grants)
	}
	if len(first.IPCapabilities) != 2 || !first.IPCapabilities[0].permitsTCP(443) || first.IPCapabilities[0].permitsTCP(80) || first.IPCapabilities[1].permitsTCP(53) {
		t.Fatalf("first grant capabilities = %#v", first.IPCapabilities)
	}
	if !validated.Policy.Grants[1].IPCapabilities[0].permitsTCP(65535) {
		t.Fatalf("wildcard grant = %#v", validated.Policy.Grants[1])
	}
	if !validated.Policy.Grants[2].IPCapabilities[0].permitsTCP(85) || validated.Policy.Grants[2].IPCapabilities[0].permitsTCP(443) {
		t.Fatalf("range grant = %#v", validated.Policy.Grants[2])
	}
}

func TestParseGrantIPCapability(t *testing.T) {
	tests := []struct {
		value       string
		port        int
		permitsTCP  bool
		shouldError bool
	}{
		{value: "*", port: 443, permitsTCP: true},
		{value: "443", port: 443, permitsTCP: true},
		{value: "400-500", port: 443, permitsTCP: true},
		{value: "tcp:*", port: 443, permitsTCP: true},
		{value: "6:443", port: 443, permitsTCP: true},
		{value: "udp:443", port: 443, permitsTCP: false},
		{value: "17:*", port: 443, permitsTCP: false},
		{value: "tcp:500-400", shouldError: true},
		{value: "tcp:70000", shouldError: true},
		{value: "future:443", shouldError: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			capability, err := parseGrantIPCapability(test.value)
			if test.shouldError {
				if err == nil {
					t.Fatalf("parseGrantIPCapability(%q) error = nil", test.value)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGrantIPCapability(%q) error = %v", test.value, err)
			}
			if got := capability.permitsTCP(test.port); got != test.permitsTCP {
				t.Fatalf("permitsTCP(%d) = %v, want %v", test.port, got, test.permitsTCP)
			}
		})
	}

	wildcard, err := parseGrantIPCapability("*")
	if err != nil {
		t.Fatal(err)
	}
	rule := accessRule{Kind: accessRuleGrant, IPCapabilities: []grantIPCapability{wildcard}}
	if rule.permitsTCP(0) || rule.permitsTCP(65536) {
		t.Fatal("Grant rule permits an invalid NPM backend port")
	}
}

func TestValidatePolicyDocumentRejectsMalformedGrant(t *testing.T) {
	for _, raw := range []string{
		`{"grants":[{"src":["*"],"dst":["*"]}]}`,
		`{"grants":[{"src":["*"],"dst":["*"],"ip":["tcp:70000"]}]}`,
		`{"grants":[{"src":[],"dst":["*"],"ip":["*"]}]}`,
	} {
		if _, err := validatePolicyDocument([]byte(raw)); err == nil {
			t.Fatalf("validatePolicyDocument(%s) error = nil", raw)
		}
	}
}

func TestPolicyValidationErrorIsDeterministic(t *testing.T) {
	raw := []byte(`{"futureZ":{},"futureA":{},"nodeAttrs":[{"target":["*"],"futureB":true,"futureA":true}]}`)
	var first string
	for index := 0; index < 100; index++ {
		_, err := validatePolicyDocument(raw)
		if err == nil {
			t.Fatal("validatePolicyDocument() error = nil")
		}
		if index == 0 {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("error changed from %q to %q", first, err)
		}
	}
	if !strings.Contains(first, `unknown field "futureA"`) {
		t.Fatalf("deterministic error = %q", first)
	}
}

func TestValidatePolicyDocumentAllowsEmptyPolicy(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{}`), []byte(`{"acls":[],"grants":[],"postures":{},"ipsets":{},"nodeAttrs":[]}`)} {
		validated, err := validatePolicyDocument(raw)
		if err != nil {
			t.Fatalf("validatePolicyDocument(%q) error = %v", raw, err)
		}
		if validated.Policy == nil || len(validated.Policy.ACLs) != 0 {
			t.Fatalf("validatePolicyDocument(%q) = %#v", raw, validated)
		}
	}
}

func TestValidatePolicyDocumentRejectsMalformedSectionTypes(t *testing.T) {
	tests := map[string]string{
		"groups null":      `{"groups":null}`,
		"tagOwners null":   `{"tagOwners":null}`,
		"hosts null":       `{"hosts":null}`,
		"acls null":        `{"acls":null}`,
		"grants null":      `{"grants":null}`,
		"nodeAttrs null":   `{"nodeAttrs":null}`,
		"postures null":    `{"postures":null}`,
		"ipsets null":      `{"ipsets":null}`,
		"ssh null":         `{"ssh":null}`,
		"acls object":      `{"acls":{}}`,
		"grants object":    `{"grants":{}}`,
		"grants false":     `{"grants":false}`,
		"grants string":    `{"grants":""}`,
		"nodeAttrs object": `{"nodeAttrs":{}}`,
		"nodeAttrs string": `{"nodeAttrs":""}`,
		"postures array":   `{"postures":[]}`,
		"ipsets false":     `{"ipsets":false}`,
		"ssh object":       `{"ssh":{}}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := validatePolicyDocument([]byte(raw)); err == nil {
				t.Fatal("validatePolicyDocument() error = nil")
			}
		})
	}
}

func TestValidatePolicyDocumentRejectsNull(t *testing.T) {
	_, err := validatePolicyDocument([]byte(`null`))
	if err == nil || !strings.Contains(err.Error(), "expected a JSON object") {
		t.Fatalf("validatePolicyDocument(null) error = %v", err)
	}
}

func TestValidatePolicyDocumentRejectsUnsupportedPolicy(t *testing.T) {
	tests := map[string]string{
		"grant app capability":   `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"app":{"example/cap":[{}]}}]}`,
		"grant source posture":   `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"srcPosture":["posture:trusted"]}]}`,
		"grant routing via":      `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"via":["tag:router"]}]}`,
		"grant app wrong shape":  `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"app":[]}]}`,
		"grant posture shape":    `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"srcPosture":{}}]}`,
		"grant via wrong shape":  `{"grants":[{"src":["*"],"dst":["*"],"ip":["*"],"via":{}}]}`,
		"posture section":        `{"postures":{"posture:trusted":["node:os == 'linux'"]}}`,
		"source posture":         `{"acls":[{"action":"accept","src":["*"],"srcPosture":["posture:trusted"],"dst":["*:*"]}]}`,
		"ip sets":                `{"ipsets":{"ipset:lan":["10.0.0.0/8"]}}`,
		"ipset selector":         `{"acls":[{"action":"accept","src":["*"],"dst":["ipset:lan:*"]}]}`,
		"service selector":       `{"acls":[{"action":"accept","src":["*"],"dst":["svc:web:*"]}]}`,
		"node app capabilities":  `{"nodeAttrs":[{"target":["*"],"app":{"example/cap":[{}]}}]}`,
		"node app wrong shape":   `{"nodeAttrs":[{"target":["*"],"attr":["funnel"],"app":[]}]}`,
		"ACL posture shape":      `{"acls":[{"action":"accept","src":["*"],"srcPosture":{},"dst":["*:*"]}]}`,
		"non-accept action":      `{"acls":[{"action":"deny","src":["*"],"dst":["*:*"]}]}`,
		"legacy users alias":     `{"acls":[{"action":"accept","users":["*"],"dst":["*:*"]}]}`,
		"legacy ports alias":     `{"acls":[{"action":"accept","src":["*"],"ports":["*:*"]}]}`,
		"unknown rule field":     `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"] ,"future":true}]}`,
		"unknown policy section": `{"futureAccessRules":[]}`,
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := validatePolicyDocument([]byte(raw))
			if err == nil {
				t.Fatal("validatePolicyDocument() error = nil")
			}
			var unsupported *unsupportedPolicyError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error type = %T, want *unsupportedPolicyError: %v", err, err)
			}
		})
	}
}

func TestValidatePolicyDocumentRejectsUnsupportedGrantSelectors(t *testing.T) {
	tests := map[string]string{
		"undefined group":          `{"grants":[{"src":["group:missing"],"dst":["*"],"ip":["*"]}]}`,
		"source posture":           `{"grants":[{"src":["posture:trusted"],"dst":["*"],"ip":["*"]}]}`,
		"source posture at":        `{"grants":[{"src":["posture:user@example.com"],"dst":["*"],"ip":["*"]}]}`,
		"source service":           `{"grants":[{"src":["svc:client"],"dst":["*"],"ip":["*"]}]}`,
		"undefined group at":       `{"grants":[{"src":["group:user@example.com"],"dst":["*"],"ip":["*"]}]}`,
		"unknown source at":        `{"grants":[{"src":["future:user@example.com"],"dst":["*"],"ip":["*"]}]}`,
		"destination ipset":        `{"grants":[{"src":["*"],"dst":["ipset:lan"],"ip":["*"]}]}`,
		"destination service":      `{"grants":[{"src":["*"],"dst":["svc:web"],"ip":["*"]}]}`,
		"destination internet":     `{"grants":[{"src":["*"],"dst":["autogroup:internet"],"ip":["*"]}]}`,
		"internet alias collision": `{"hosts":{"autogroup:internet":"10.0.0.10"},"grants":[{"src":["*"],"dst":["autogroup:internet"],"ip":["*"]}]}`,
		"service alias collision":  `{"hosts":{"svc:web":"10.0.0.10"},"grants":[{"src":["*"],"dst":["svc:web"],"ip":["*"]}]}`,
		"ipset alias collision":    `{"hosts":{"ipset:lan":"10.0.0.10"},"grants":[{"src":["*"],"dst":["ipset:lan"],"ip":["*"]}]}`,
		"posture alias collision":  `{"hosts":{"posture:trusted":"10.0.0.10"},"grants":[{"src":["*"],"dst":["posture:trusted"],"ip":["*"]}]}`,
		"identity alias collision": `{"hosts":{"alice@example.com":"10.0.0.10"},"grants":[{"src":["*"],"dst":["alice@example.com"],"ip":["*"]}]}`,
		"undefined destination":    `{"grants":[{"src":["*"],"dst":["missing-host"],"ip":["*"]}]}`,
		"mixed self sources":       `{"groups":{"group:admin":["alice@example.com"]},"grants":[{"src":["group:admin","tag:client"],"dst":["autogroup:self"],"ip":["*"]}]}`,
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := validatePolicyDocument([]byte(raw))
			if err == nil {
				t.Fatal("validatePolicyDocument() error = nil")
			}
			var unsupported *unsupportedPolicyError
			if !errors.As(err, &unsupported) {
				t.Fatalf("error type = %T, want *unsupportedPolicyError: %v", err, err)
			}
		})
	}
}

func TestValidatePolicyDocumentAcceptsMachineGrantSourcesWithoutHumanInference(t *testing.T) {
	raw := []byte(`{
		"hosts":{"client":"100.64.0.10"},
		"grants":[
			{"src":["tag:client"],"dst":["tag:app"],"ip":["tcp:443"]},
			{"src":["100.64.0.10","100.64.0.0/24","client","autogroup:tagged"],"dst":["10.0.0.10"],"ip":["tcp:443"]}
		]
	}`)

	validated, err := validatePolicyDocument(raw)
	if err != nil {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
	if len(validated.Policy.Grants) != 2 {
		t.Fatalf("grants = %#v", validated.Policy.Grants)
	}
	for index, grant := range validated.Policy.Grants {
		if len(grant.BrowserSrc) != 0 {
			t.Fatalf("grant %d browser sources = %#v, want none", index, grant.BrowserSrc)
		}
	}

	data := &CacheData{
		Policy: validated.Policy,
		Nodes:  []Node{{Tags: []string{"tag:app"}, Addresses: []string{"10.0.0.20"}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"tag.example.com"}, ForwardHost: "10.0.0.20", ForwardPort: 443, Enabled: true},
			{ID: 2, DomainNames: []string{"machine.example.com"}, ForwardHost: "10.0.0.10", ForwardPort: 443, Enabled: true},
		},
	}
	for _, login := range []string{"tag:client", "100.64.0.10", "100.64.0.0/24", "client", "autogroup:tagged"} {
		if cards := MatchServices(&Identity{Login: login}, data); len(cards) != 0 {
			t.Fatalf("machine-selector login %q received cards: %#v", login, cards)
		}
	}
}

func TestGrantSourceClassification(t *testing.T) {
	policy := &Policy{
		Groups: map[string][]string{"group:users": {"alice@example.com"}},
		Hosts:  map[string]string{"client": "100.64.0.10"},
	}
	tests := []struct {
		selector string
		kind     grantSourceKind
		browser  bool
	}{
		{selector: "*", kind: grantSourceWildcard, browser: true},
		{selector: "alice@example.com", kind: grantSourceHuman, browser: true},
		{selector: "group:users", kind: grantSourceGroup, browser: true},
		{selector: "autogroup:owner", kind: grantSourceRole, browser: true},
		{selector: "autogroup:admin", kind: grantSourceRole, browser: true},
		{selector: "autogroup:member", kind: grantSourceRole, browser: true},
		{selector: "autogroup:it-admin", kind: grantSourceRole, browser: true},
		{selector: "autogroup:network-admin", kind: grantSourceRole, browser: true},
		{selector: "autogroup:billing-admin", kind: grantSourceRole, browser: true},
		{selector: "autogroup:auditor", kind: grantSourceRole, browser: true},
		{selector: "autogroup:tagged", kind: grantSourceMachine},
		{selector: "autogroup:shared", kind: grantSourceMachine},
		{selector: "tag:client", kind: grantSourceMachine},
		{selector: "100.64.0.10", kind: grantSourceMachine},
		{selector: "100.64.0.0/24", kind: grantSourceMachine},
		{selector: "client", kind: grantSourceMachine},
	}
	for _, test := range tests {
		t.Run(test.selector, func(t *testing.T) {
			kind, err := validateGrantSourceSelector(test.selector, policy)
			if err != nil {
				t.Fatalf("validateGrantSourceSelector() error = %v", err)
			}
			if kind != test.kind || kind.browserEligible() != test.browser {
				t.Fatalf("classification = %q browser=%v, want %q browser=%v", kind, kind.browserEligible(), test.kind, test.browser)
			}
		})
	}
}

func TestValidatePolicyDocumentValidatesNodeAttrsStructure(t *testing.T) {
	for _, raw := range []string{
		`{"nodeAttrs":[{"target":[],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:member"],"attr":[]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:member"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:member"],"attr":[""]}]}`,
		`{"nodeAttrs":[{"target":["svc:future"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:future"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["group:missing"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["100.64.0.10"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["100.64.0.0/24"],"attr":["funnel"]}]}`,
		`{"hosts":{"client":"100.64.0.10"},"nodeAttrs":[{"target":["client"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:tagged"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:shared"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:admin"],"attr":["funnel"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:member"],"attr":["future-authorization-mode"]}]}`,
	} {
		if _, err := validatePolicyDocument([]byte(raw)); err == nil {
			t.Fatalf("validatePolicyDocument(%s) error = nil", raw)
		}
	}
}

func TestValidatePolicyDocumentAcceptsKnownFunnelTargets(t *testing.T) {
	raw := []byte(`{
		"groups":{"group:users":["alice@example.com"]},
		"nodeAttrs":[{
			"target":["*","alice@example.com","group:users","tag:server","autogroup:member"],
			"attr":["funnel"]
		}]
	}`)
	validated, err := validatePolicyDocument(raw)
	if err != nil {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
	if !validated.NodeAttrsPresent {
		t.Fatal("NodeAttrsPresent = false")
	}
}

func TestValidateLegacyPolicyDocumentRejectsModernSections(t *testing.T) {
	for _, raw := range []string{
		`{"grants":[{"src":["*"],"dst":["*"],"ip":["*"]}]}`,
		`{"nodeAttrs":[{"target":["autogroup:member"],"attr":["funnel"]}]}`,
	} {
		_, err := validateLegacyPolicyDocument([]byte(raw))
		if err == nil {
			t.Fatalf("validateLegacyPolicyDocument(%s) error = nil", raw)
		}
		var unsupported *unsupportedPolicyError
		if !errors.As(err, &unsupported) {
			t.Fatalf("error type = %T, want *unsupportedPolicyError: %v", err, err)
		}
	}
}

func TestValidatePolicyDocumentRejectsMalformedProtocol(t *testing.T) {
	_, err := validatePolicyDocument([]byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"] ,"proto":{"name":"tcp"}}]}`))
	if err == nil || !strings.Contains(err.Error(), "invalid proto") {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
}
