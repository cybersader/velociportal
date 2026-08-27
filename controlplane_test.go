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

func TestValidatePolicyDocumentRejectsNull(t *testing.T) {
	_, err := validatePolicyDocument([]byte(`null`))
	if err == nil || !strings.Contains(err.Error(), "expected a JSON object") {
		t.Fatalf("validatePolicyDocument(null) error = %v", err)
	}
}

func TestValidatePolicyDocumentRejectsUnsupportedPolicy(t *testing.T) {
	tests := map[string]string{
		"grant":                  `{"grants":[{"src":["*"],"dst":["*"]}]}`,
		"mixed ACL and grant":    `{"acls":[{"action":"accept","src":["*"],"dst":["*:*"]}],"grants":[{"src":["*"],"dst":["*"]}]}`,
		"posture section":        `{"postures":{"posture:trusted":["node:os == 'linux'"]}}`,
		"source posture":         `{"acls":[{"action":"accept","src":["*"],"srcPosture":["posture:trusted"],"dst":["*:*"]}]}`,
		"ip sets":                `{"ipsets":{"ipset:lan":["10.0.0.0/8"]}}`,
		"ipset selector":         `{"acls":[{"action":"accept","src":["*"],"dst":["ipset:lan:*"]}]}`,
		"service selector":       `{"acls":[{"action":"accept","src":["*"],"dst":["svc:web:*"]}]}`,
		"node capabilities":      `{"nodeAttrs":[{"target":["*"],"attr":["funnel"]}]}`,
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

func TestValidatePolicyDocumentRejectsMalformedProtocol(t *testing.T) {
	_, err := validatePolicyDocument([]byte(`{"acls":[{"action":"accept","src":["*"],"dst":["*:*"] ,"proto":{"name":"tcp"}}]}`))
	if err == nil || !strings.Contains(err.Error(), "invalid proto") {
		t.Fatalf("validatePolicyDocument() error = %v", err)
	}
}
