package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type controlPlaneProvider string

const (
	controlPlaneHeadscale controlPlaneProvider = "headscale"
	controlPlaneTailscale controlPlaneProvider = "tailscale"
)

const legacyACLVisibilityV1 = "legacy_acl_visibility_v1"

type controlPlaneSupportLevel string

const (
	controlPlaneSupported controlPlaneSupportLevel = "supported"
	controlPlanePreview   controlPlaneSupportLevel = "preview"
)

type ControlPlaneMetadata struct {
	Provider     controlPlaneProvider
	PolicyMode   string
	SupportLevel controlPlaneSupportLevel
	SSHPresent   bool
}

type ControlPlaneResult struct {
	Policy   *Policy
	Nodes    []Node
	Metadata ControlPlaneMetadata
}

type controlPlaneLoadStage string

const (
	controlPlaneStageAuth    controlPlaneLoadStage = "auth"
	controlPlaneStagePolicy  controlPlaneLoadStage = "policy"
	controlPlaneStageUsers   controlPlaneLoadStage = "users"
	controlPlaneStageDevices controlPlaneLoadStage = "devices"
	controlPlaneStageNodes   controlPlaneLoadStage = "nodes"
)

type controlPlaneProgress func(stage controlPlaneLoadStage, count int)

type ControlPlane interface {
	Provider() controlPlaneProvider
	Load(context.Context, controlPlaneProgress) (*ControlPlaneResult, error)
}

type controlPlaneLoadError struct {
	Provider controlPlaneProvider
	Stage    controlPlaneLoadStage
	Err      error
}

func (e *controlPlaneLoadError) Error() string {
	return fmt.Sprintf("%s %s: %v", e.Provider, e.Stage, e.Err)
}

func (e *controlPlaneLoadError) Unwrap() error { return e.Err }

type unsupportedPolicyError struct {
	Section string
	Reason  string
}

func (e *unsupportedPolicyError) Error() string {
	if e.Section == "" {
		return "unsupported policy: " + e.Reason
	}
	return fmt.Sprintf("unsupported policy section %q: %s", e.Section, e.Reason)
}

type Policy struct {
	Groups    map[string][]string
	TagOwners map[string][]string
	ACLs      []ACLRule
	Hosts     map[string]string
}

type ACLRule struct {
	Action string
	Src    []string
	Dst    []string
}

type Node struct {
	ID         string
	Name       string
	OwnerLogin string
	Tags       []string
	Addresses  []string
}

type validatedPolicy struct {
	Policy     *Policy
	SSHPresent bool
}

var benignPolicySections = map[string]bool{
	"autoApprovers":       true,
	"tests":               true,
	"sshTests":            true,
	"derpMap":             true,
	"disableIPv4":         true,
	"oneCGNATRoute":       true,
	"randomizeClientPort": true,
}

func validatePolicyDocument(raw []byte) (*validatedPolicy, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &validatedPolicy{Policy: &Policy{}}, nil
	}

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	if sections == nil {
		return nil, fmt.Errorf("decode policy: expected a JSON object")
	}

	policy := &Policy{}
	result := &validatedPolicy{Policy: policy}
	for section, value := range sections {
		switch section {
		case "groups":
			if err := json.Unmarshal(value, &policy.Groups); err != nil {
				return nil, fmt.Errorf("decode policy groups: %w", err)
			}
		case "tagOwners":
			if err := json.Unmarshal(value, &policy.TagOwners); err != nil {
				return nil, fmt.Errorf("decode policy tagOwners: %w", err)
			}
		case "hosts":
			if err := json.Unmarshal(value, &policy.Hosts); err != nil {
				return nil, fmt.Errorf("decode policy hosts: %w", err)
			}
		case "acls":
			rules, err := validateACLRules(value)
			if err != nil {
				return nil, err
			}
			policy.ACLs = rules
		case "grants", "postures", "ipsets", "nodeAttrs":
			if !jsonValueEmpty(value) {
				return nil, &unsupportedPolicyError{Section: section, Reason: "non-empty section is outside legacy ACL visibility"}
			}
		case "ssh":
			result.SSHPresent = !jsonValueEmpty(value)
		default:
			if !benignPolicySections[section] {
				return nil, &unsupportedPolicyError{Section: section, Reason: "unknown policy section"}
			}
		}
	}

	return result, nil
}

func validateACLRules(raw json.RawMessage) ([]ACLRule, error) {
	if jsonValueEmpty(raw) {
		return nil, nil
	}
	var wireRules []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wireRules); err != nil {
		return nil, fmt.Errorf("decode policy acls: %w", err)
	}

	rules := make([]ACLRule, 0, len(wireRules))
	for index, wire := range wireRules {
		for field := range wire {
			switch field {
			case "action", "src", "dst", "proto", "srcPosture":
			default:
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d contains unknown field %q", index, field)}
			}
		}
		if value, ok := wire["srcPosture"]; ok && !jsonValueEmpty(value) {
			return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d uses source posture", index)}
		}
		if value, ok := wire["proto"]; ok {
			if err := validateIgnoredProtocol(value); err != nil {
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d has invalid proto: %v", index, err)}
			}
		}

		var rule ACLRule
		if err := unmarshalRequiredACLField(wire, "action", &rule.Action); err != nil {
			return nil, fmt.Errorf("decode policy acls rule %d: %w", index, err)
		}
		if rule.Action != "accept" {
			return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d action %q is not supported", index, rule.Action)}
		}
		if err := unmarshalRequiredACLField(wire, "src", &rule.Src); err != nil {
			return nil, fmt.Errorf("decode policy acls rule %d: %w", index, err)
		}
		if err := unmarshalRequiredACLField(wire, "dst", &rule.Dst); err != nil {
			return nil, fmt.Errorf("decode policy acls rule %d: %w", index, err)
		}
		for _, source := range rule.Src {
			normalized := strings.TrimSpace(source)
			if unsupportedPolicySelector(normalized) {
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d uses unsupported source selector %q", index, normalized)}
			}
		}
		for _, destination := range rule.Dst {
			normalized := stripPort(strings.TrimSpace(destination))
			if unsupportedPolicySelector(normalized) {
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d uses unsupported destination selector %q", index, normalized)}
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func unsupportedPolicySelector(selector string) bool {
	return strings.HasPrefix(selector, "ipset:") ||
		strings.HasPrefix(selector, "svc:") ||
		strings.HasPrefix(selector, "posture:")
}

func unmarshalRequiredACLField(fields map[string]json.RawMessage, name string, out any) error {
	raw, ok := fields[name]
	if !ok {
		return fmt.Errorf("missing required field %q", name)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("field %q: %w", name, err)
	}
	return nil
}

func validateIgnoredProtocol(raw json.RawMessage) error {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return errors.New("must not be blank")
		}
		return nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil {
		return nil
	}
	return errors.New("must be a string or number")
}

func jsonValueEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return false
	}
	switch typed := value.(type) {
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	case string:
		return strings.TrimSpace(typed) == ""
	case bool:
		return !typed
	default:
		return false
	}
}

func normalizeStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func reportControlPlaneProgress(progress controlPlaneProgress, stage controlPlaneLoadStage, count int) {
	if progress != nil {
		progress(stage, count)
	}
}
