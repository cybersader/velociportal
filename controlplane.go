package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

type controlPlaneProvider string

const (
	controlPlaneHeadscale controlPlaneProvider = "headscale"
	controlPlaneTailscale controlPlaneProvider = "tailscale"
)

const (
	legacyACLVisibilityV1     = "legacy_acl_visibility_v1"
	networkAccessVisibilityV1 = "network_access_visibility_v1"
)

type controlPlaneSupportLevel string

const (
	controlPlaneSupported controlPlaneSupportLevel = "supported"
	controlPlanePreview   controlPlaneSupportLevel = "preview"
)

type ControlPlaneMetadata struct {
	Provider     controlPlaneProvider
	PolicyMode   string
	SupportLevel controlPlaneSupportLevel
}

type ControlPlaneResult struct {
	Policy                    *Policy
	Nodes                     []Node
	GrantRoleSelectorsByLogin map[string][]string
	Metadata                  ControlPlaneMetadata
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

type hostnameSuggestionControlPlane interface {
	ControlPlane
	LoadHostnameSuggestions(context.Context, controlPlaneProgress) (*ControlPlaneResult, []string, error)
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

type accessRuleKind string

const (
	accessRuleACL   accessRuleKind = "acl"
	accessRuleGrant accessRuleKind = "grant"
)

type sshPolicyState string

const (
	sshPolicyAbsent      sshPolicyState = "absent"
	sshPolicySupported   sshPolicyState = "supported"
	sshPolicyUnsupported sshPolicyState = "unsupported"
)

type sshUnsupportedReason string

const (
	sshUnsupportedProvider     sshUnsupportedReason = "provider_unsupported"
	sshUnsupportedSectionShape sshUnsupportedReason = "invalid_section_shape"
	sshUnsupportedRuleShape    sshUnsupportedReason = "invalid_rule_shape"
	sshUnsupportedUnknownField sshUnsupportedReason = "unknown_rule_field"
	sshUnsupportedAction       sshUnsupportedReason = "unsupported_action"
	sshUnsupportedSource       sshUnsupportedReason = "unsupported_source"
	sshUnsupportedDestination  sshUnsupportedReason = "unsupported_destination"
	sshUnsupportedUser         sshUnsupportedReason = "unsupported_user"
	sshUnsupportedCheckPeriod  sshUnsupportedReason = "invalid_check_period"
)

type SSHPolicy struct {
	State             sshPolicyState
	Rules             []SSHRule
	RuleCount         int
	UnsupportedReason sshUnsupportedReason
}

type SSHRule struct {
	Action      string
	Src         []string
	Dst         []string
	Users       []string
	CheckPeriod time.Duration
}

type Policy struct {
	Groups    map[string][]string
	TagOwners map[string][]string
	ACLs      []ACLRule
	Grants    []GrantRule
	Hosts     map[string]string
	SSH       SSHPolicy
}

type ACLRule struct {
	Action string
	Src    []string
	Dst    []string
}

type GrantRule struct {
	Src            []string
	BrowserSrc     []string
	Dst            []string
	IPCapabilities []grantIPCapability
}

type accessRule struct {
	Kind           accessRuleKind
	Index          int
	Src            []string
	Dst            []string
	IPCapabilities []grantIPCapability
}

type grantIPCapability struct {
	AllProtocols bool
	Protocol     uint8
	AllPorts     bool
	PortStart    int
	PortEnd      int
}

func (p *Policy) accessRules() []accessRule {
	if p == nil {
		return nil
	}
	rules := make([]accessRule, 0, len(p.ACLs)+len(p.Grants))
	for index, rule := range p.ACLs {
		rules = append(rules, accessRule{
			Kind:  accessRuleACL,
			Index: index,
			Src:   rule.Src,
			Dst:   rule.Dst,
		})
	}
	for index, rule := range p.Grants {
		rules = append(rules, accessRule{
			Kind:           accessRuleGrant,
			Index:          index,
			Src:            rule.BrowserSrc,
			Dst:            rule.Dst,
			IPCapabilities: rule.IPCapabilities,
		})
	}
	return rules
}

func (p *Policy) accessRuleCount() int {
	if p == nil {
		return 0
	}
	return len(p.ACLs) + len(p.Grants)
}

func (r accessRule) permitsTCP(port int) bool {
	if r.Kind == accessRuleACL {
		return true
	}
	if port < 1 || port > 65535 {
		return false
	}
	for _, capability := range r.IPCapabilities {
		if capability.permitsTCP(port) {
			return true
		}
	}
	return false
}

func (c grantIPCapability) permitsTCP(port int) bool {
	if !c.AllProtocols && c.Protocol != 6 {
		return false
	}
	if c.AllPorts {
		return true
	}
	return port >= c.PortStart && port <= c.PortEnd
}

type Node struct {
	ID         string
	Name       string
	OwnerLogin string
	Tags       []string
	Addresses  []string
}

type validatedPolicy struct {
	Policy           *Policy
	PolicyMode       string
	NodeAttrsPresent bool
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

type policyValidationOptions struct {
	AllowGrants       bool
	AllowNodeAttrs    bool
	NormalizeSSHRules bool
}

func validatePolicyDocument(raw []byte) (*validatedPolicy, error) {
	return validatePolicyDocumentWithOptions(raw, policyValidationOptions{
		AllowGrants:       true,
		AllowNodeAttrs:    true,
		NormalizeSSHRules: true,
	})
}

func validateLegacyPolicyDocument(raw []byte) (*validatedPolicy, error) {
	return validatePolicyDocumentWithOptions(raw, policyValidationOptions{})
}

func validatePolicyDocumentWithOptions(raw []byte, options policyValidationOptions) (*validatedPolicy, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return &validatedPolicy{Policy: &Policy{SSH: SSHPolicy{State: sshPolicyAbsent}}, PolicyMode: legacyACLVisibilityV1}, nil
	}

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("decode policy: %w", err)
	}
	if sections == nil {
		return nil, fmt.Errorf("decode policy: expected a JSON object")
	}

	policy := &Policy{SSH: SSHPolicy{State: sshPolicyAbsent}}
	result := &validatedPolicy{Policy: policy, PolicyMode: legacyACLVisibilityV1}

	if value, ok := sections["groups"]; ok {
		if _, err := jsonSectionObjectEmpty(value); err != nil {
			return nil, fmt.Errorf("decode policy groups: %w", err)
		}
		if err := json.Unmarshal(value, &policy.Groups); err != nil {
			return nil, fmt.Errorf("decode policy groups: %w", err)
		}
	}
	if value, ok := sections["tagOwners"]; ok {
		if _, err := jsonSectionObjectEmpty(value); err != nil {
			return nil, fmt.Errorf("decode policy tagOwners: %w", err)
		}
		if err := json.Unmarshal(value, &policy.TagOwners); err != nil {
			return nil, fmt.Errorf("decode policy tagOwners: %w", err)
		}
	}
	if value, ok := sections["hosts"]; ok {
		if _, err := jsonSectionObjectEmpty(value); err != nil {
			return nil, fmt.Errorf("decode policy hosts: %w", err)
		}
		if err := json.Unmarshal(value, &policy.Hosts); err != nil {
			return nil, fmt.Errorf("decode policy hosts: %w", err)
		}
	}
	if value, ok := sections["acls"]; ok {
		if _, err := jsonArrayEmpty(value); err != nil {
			return nil, fmt.Errorf("decode policy acls: %w", err)
		}
		rules, err := validateACLRules(value)
		if err != nil {
			return nil, err
		}
		policy.ACLs = rules
	}
	if value, ok := sections["grants"]; ok {
		empty, err := jsonArrayEmpty(value)
		if err != nil {
			return nil, fmt.Errorf("decode policy grants: %w", err)
		}
		if !options.AllowGrants && !empty {
			return nil, &unsupportedPolicyError{Section: "grants", Reason: "non-empty section is not supported by this control plane"}
		}
		if options.AllowGrants {
			rules, err := validateGrantRules(value, policy)
			if err != nil {
				return nil, err
			}
			policy.Grants = rules
			if len(rules) > 0 {
				result.PolicyMode = networkAccessVisibilityV1
			}
		}
	}
	for _, section := range []string{"postures", "ipsets"} {
		if value, ok := sections[section]; ok {
			empty, err := jsonSectionObjectEmpty(value)
			if err != nil {
				return nil, &unsupportedPolicyError{Section: section, Reason: fmt.Sprintf("invalid section shape: %v", err)}
			}
			if !empty {
				return nil, &unsupportedPolicyError{Section: section, Reason: "non-empty section is outside network access visibility"}
			}
		}
	}
	if value, ok := sections["nodeAttrs"]; ok {
		empty, err := jsonArrayEmpty(value)
		if err != nil {
			return nil, fmt.Errorf("decode policy nodeAttrs: %w", err)
		}
		if !options.AllowNodeAttrs && !empty {
			return nil, &unsupportedPolicyError{Section: "nodeAttrs", Reason: "non-empty section is not supported by this control plane"}
		}
		if options.AllowNodeAttrs {
			present, err := validateNodeAttrs(value, policy)
			if err != nil {
				return nil, err
			}
			result.NodeAttrsPresent = present
		}
	}
	if value, ok := sections["ssh"]; ok {
		policy.SSH = normalizeSSHPolicy(value, policy, options.NormalizeSSHRules)
	}

	known := map[string]bool{
		"groups": true, "tagOwners": true, "hosts": true, "acls": true,
		"grants": true, "postures": true, "ipsets": true, "nodeAttrs": true,
		"ssh": true,
	}
	unknown := make([]string, 0)
	for section := range sections {
		if !known[section] && !benignPolicySections[section] {
			unknown = append(unknown, section)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &unsupportedPolicyError{Section: unknown[0], Reason: "unknown policy section"}
	}

	return result, nil
}

func validateACLRules(raw json.RawMessage) ([]ACLRule, error) {
	var wireRules []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wireRules); err != nil {
		return nil, fmt.Errorf("decode policy acls: %w", err)
	}

	rules := make([]ACLRule, 0, len(wireRules))
	for index, wire := range wireRules {
		if unknown := firstUnknownField(wire, "action", "src", "dst", "proto", "srcPosture"); unknown != "" {
			return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d contains unknown field %q", index, unknown)}
		}
		if value, ok := wire["srcPosture"]; ok {
			empty, err := jsonStringSliceEmpty(value)
			if err != nil {
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d has invalid srcPosture: %v", index, err)}
			}
			if !empty {
				return nil, &unsupportedPolicyError{Section: "acls", Reason: fmt.Sprintf("rule %d uses source posture", index)}
			}
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

func validateGrantRules(raw json.RawMessage, policy *Policy) ([]GrantRule, error) {
	var wireRules []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wireRules); err != nil {
		return nil, fmt.Errorf("decode policy grants: %w", err)
	}

	rules := make([]GrantRule, 0, len(wireRules))
	for index, wire := range wireRules {
		if unknown := firstUnknownField(wire, "src", "dst", "ip", "app", "srcPosture", "via"); unknown != "" {
			return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d contains unknown field %q", index, unknown)}
		}
		if value, ok := wire["app"]; ok {
			empty, err := jsonObjectEmpty(value)
			if err != nil {
				return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d has invalid app: %v", index, err)}
			}
			if !empty {
				return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d uses unsupported app semantics", index)}
			}
		}
		for _, field := range []string{"srcPosture", "via"} {
			if value, ok := wire[field]; ok {
				empty, err := jsonStringSliceEmpty(value)
				if err != nil {
					return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d has invalid %s: %v", index, field, err)}
				}
				if !empty {
					return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d uses unsupported %s semantics", index, field)}
				}
			}
		}

		var src, dst, ip []string
		if err := unmarshalRequiredStringSlice(wire, "src", &src); err != nil {
			return nil, fmt.Errorf("decode policy grants rule %d: %w", index, err)
		}
		if err := unmarshalRequiredStringSlice(wire, "dst", &dst); err != nil {
			return nil, fmt.Errorf("decode policy grants rule %d: %w", index, err)
		}
		if err := unmarshalRequiredStringSlice(wire, "ip", &ip); err != nil {
			return nil, fmt.Errorf("decode policy grants rule %d: %w", index, err)
		}

		sourceKinds := make([]grantSourceKind, 0, len(src))
		browserSources := make([]string, 0, len(src))
		for _, selector := range src {
			kind, err := validateGrantSourceSelector(selector, policy)
			if err != nil {
				return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d uses unsupported source selector %q: %v", index, selector, err)}
			}
			sourceKinds = append(sourceKinds, kind)
			if kind.browserEligible() {
				browserSources = append(browserSources, selector)
			}
		}
		selfDestination := false
		for _, selector := range dst {
			if err := validateGrantDestinationSelector(selector, policy); err != nil {
				return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d uses unsupported destination selector %q: %v", index, selector, err)}
			}
			selfDestination = selfDestination || strings.TrimSpace(selector) == "autogroup:self"
		}
		if selfDestination && !grantSourcesSupportSelf(sourceKinds) {
			return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d uses autogroup:self with a non-human source", index)}
		}

		capabilities := make([]grantIPCapability, 0, len(ip))
		for _, value := range ip {
			capability, err := parseGrantIPCapability(value)
			if err != nil {
				return nil, &unsupportedPolicyError{Section: "grants", Reason: fmt.Sprintf("rule %d has invalid ip capability %q: %v", index, value, err)}
			}
			capabilities = append(capabilities, capability)
		}
		rules = append(rules, GrantRule{
			Src:            normalizeStrings(src),
			BrowserSrc:     normalizeStrings(browserSources),
			Dst:            normalizeStrings(dst),
			IPCapabilities: capabilities,
		})
	}
	return rules, nil
}

func normalizeSSHPolicy(raw json.RawMessage, policy *Policy, normalizeRules bool) SSHPolicy {
	var wireRules []json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &wireRules) != nil {
		return SSHPolicy{State: sshPolicyUnsupported, UnsupportedReason: sshUnsupportedSectionShape}
	}
	if len(wireRules) == 0 {
		return SSHPolicy{State: sshPolicyAbsent}
	}
	if !normalizeRules {
		return SSHPolicy{
			State:             sshPolicyUnsupported,
			RuleCount:         len(wireRules),
			UnsupportedReason: sshUnsupportedProvider,
		}
	}

	rules := make([]SSHRule, 0, len(wireRules))
	for _, rawRule := range wireRules {
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(rawRule, &wire); err != nil || wire == nil {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedRuleShape)
		}
		if firstUnknownField(wire, "action", "src", "dst", "users", "checkPeriod") != "" {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedUnknownField)
		}

		action, ok := canonicalSSHString(wire, "action")
		if !ok || (action != "accept" && action != "check") {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedAction)
		}
		src, ok := canonicalSSHStringSlice(wire, "src")
		if !ok || !allSSHSelectors(src, func(selector string) bool { return supportedSSHSource(selector, policy) }) {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedSource)
		}
		dst, ok := canonicalSSHStringSlice(wire, "dst")
		if !ok || !allSSHSelectors(dst, supportedSSHDestination) {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedDestination)
		}
		users, ok := canonicalSSHStringSlice(wire, "users")
		if !ok || !allSSHSelectors(users, supportedSSHUser) {
			return unsupportedSSHPolicy(len(wireRules), sshUnsupportedUser)
		}

		var checkPeriod time.Duration
		if rawCheckPeriod, present := wire["checkPeriod"]; present {
			if action != "check" {
				return unsupportedSSHPolicy(len(wireRules), sshUnsupportedCheckPeriod)
			}
			parsed, ok := canonicalSSHCheckPeriod(rawCheckPeriod)
			if !ok {
				return unsupportedSSHPolicy(len(wireRules), sshUnsupportedCheckPeriod)
			}
			checkPeriod = parsed
		}

		rules = append(rules, SSHRule{
			Action:      action,
			Src:         normalizeStrings(src),
			Dst:         normalizeStrings(dst),
			Users:       normalizeStrings(users),
			CheckPeriod: checkPeriod,
		})
	}
	return SSHPolicy{State: sshPolicySupported, Rules: rules, RuleCount: len(rules)}
}

func unsupportedSSHPolicy(ruleCount int, reason sshUnsupportedReason) SSHPolicy {
	return SSHPolicy{State: sshPolicyUnsupported, RuleCount: ruleCount, UnsupportedReason: reason}
}

func canonicalSSHString(fields map[string]json.RawMessage, name string) (string, bool) {
	raw, ok := fields[name]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" || strings.TrimSpace(value) != value {
		return "", false
	}
	return value, true
}

func canonicalSSHStringSlice(fields map[string]json.RawMessage, name string) ([]string, bool) {
	raw, ok := fields[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.TrimSpace(value) != value {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			return nil, false
		}
		seen[value] = struct{}{}
	}
	return values, true
}

func allSSHSelectors(values []string, supported func(string) bool) bool {
	for _, value := range values {
		if !supported(value) {
			return false
		}
	}
	return true
}

func supportedSSHSource(selector string, policy *Policy) bool {
	if exactSSHLogin(selector) {
		return true
	}
	if strings.HasPrefix(selector, "group:") {
		members, ok := policy.Groups[selector]
		if !ok {
			return false
		}
		for _, member := range members {
			if !exactSSHLogin(member) {
				return false
			}
		}
		return true
	}
	if strings.HasPrefix(selector, "autogroup:") {
		return tailscaleHumanRoles[strings.TrimPrefix(selector, "autogroup:")]
	}
	return false
}

func exactSSHLogin(selector string) bool {
	if strings.Count(selector, "@") != 1 || strings.ContainsAny(selector, " \t\r\n:") {
		return false
	}
	parts := strings.SplitN(selector, "@", 2)
	return parts[0] != "" && parts[1] != ""
}

func supportedSSHDestination(selector string) bool {
	if selector == "autogroup:self" {
		return true
	}
	if !strings.HasPrefix(selector, "tag:") {
		return false
	}
	name := strings.TrimPrefix(selector, "tag:")
	return name != "" && !strings.ContainsAny(name, " \t\r\n:")
}

func supportedSSHUser(user string) bool {
	if user == "autogroup:nonroot" {
		return true
	}
	if strings.HasPrefix(user, "autogroup:") || strings.HasPrefix(user, "localpart:") {
		return false
	}
	if len(user) > 256 || user == "" || !sshUserInitial(user[0]) {
		return false
	}
	for index := 1; index < len(user); index++ {
		character := user[index]
		if sshUserBody(character) {
			continue
		}
		if character == '$' && index == len(user)-1 {
			continue
		}
		return false
	}
	return true
}

func sshUserInitial(character byte) bool {
	return character == '_' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}

func sshUserBody(character byte) bool {
	return sshUserInitial(character) || character >= '0' && character <= '9' || character == '.' || character == '-'
}

func canonicalSSHCheckPeriod(raw json.RawMessage) (time.Duration, bool) {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) < 2 || strings.TrimSpace(value) != value || value[0] == '0' {
		return 0, false
	}
	unit := value[len(value)-1]
	if unit != 'm' && unit != 'h' {
		return 0, false
	}
	for index := 0; index < len(value)-1; index++ {
		if value[index] < '0' || value[index] > '9' {
			return 0, false
		}
	}
	period, err := time.ParseDuration(value)
	if err != nil || period < time.Minute || period > 168*time.Hour {
		return 0, false
	}
	return period, true
}

func validateNodeAttrs(raw json.RawMessage, policy *Policy) (bool, error) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return false, fmt.Errorf("decode policy nodeAttrs: %w", err)
	}
	for index, entry := range entries {
		if unknown := firstUnknownField(entry, "target", "attr", "app"); unknown != "" {
			return false, &unsupportedPolicyError{Section: "nodeAttrs", Reason: fmt.Sprintf("entry %d contains unknown field %q", index, unknown)}
		}
		var target, attr []string
		if err := unmarshalRequiredStringSlice(entry, "target", &target); err != nil {
			return false, fmt.Errorf("decode policy nodeAttrs entry %d: %w", index, err)
		}
		if value, ok := entry["app"]; ok {
			empty, err := jsonObjectEmpty(value)
			if err != nil {
				return false, &unsupportedPolicyError{Section: "nodeAttrs", Reason: fmt.Sprintf("entry %d has invalid app: %v", index, err)}
			}
			if !empty {
				return false, &unsupportedPolicyError{Section: "nodeAttrs", Reason: fmt.Sprintf("entry %d uses application capabilities", index)}
			}
		}
		if err := unmarshalRequiredStringSlice(entry, "attr", &attr); err != nil {
			return false, fmt.Errorf("decode policy nodeAttrs entry %d: %w", index, err)
		}
		for _, selector := range target {
			if err := validateFunnelNodeAttrTarget(selector, policy); err != nil {
				return false, &unsupportedPolicyError{Section: "nodeAttrs", Reason: fmt.Sprintf("entry %d uses unsupported target selector %q: %v", index, selector, err)}
			}
		}
		for _, attribute := range attr {
			if strings.TrimSpace(attribute) != "funnel" {
				return false, &unsupportedPolicyError{Section: "nodeAttrs", Reason: fmt.Sprintf("entry %d uses unsupported attribute %q", index, attribute)}
			}
		}
	}
	return len(entries) > 0, nil
}

func validateFunnelNodeAttrTarget(selector string, policy *Policy) error {
	selector = strings.TrimSpace(selector)
	switch {
	case selector == "*", selector == "autogroup:member":
		return nil
	case strings.HasPrefix(selector, "group:"):
		if policy != nil {
			if _, ok := policy.Groups[selector]; ok {
				return nil
			}
		}
		return errors.New("group is not defined")
	case strings.HasPrefix(selector, "tag:"):
		if strings.TrimPrefix(selector, "tag:") != "" {
			return nil
		}
		return errors.New("tag is blank")
	case strings.Contains(selector, "@") && !strings.Contains(selector, ":"):
		return nil
	default:
		return errors.New("selector class is not supported for funnel")
	}
}

type grantSourceKind string

const (
	grantSourceWildcard grantSourceKind = "wildcard"
	grantSourceHuman    grantSourceKind = "human"
	grantSourceGroup    grantSourceKind = "group"
	grantSourceRole     grantSourceKind = "role"
	grantSourceMachine  grantSourceKind = "machine"
)

func (kind grantSourceKind) browserEligible() bool {
	return kind == grantSourceWildcard || kind == grantSourceHuman || kind == grantSourceGroup || kind == grantSourceRole
}

func (kind grantSourceKind) supportsSelf() bool {
	return kind == grantSourceHuman || kind == grantSourceGroup || kind == grantSourceRole
}

func validateGrantSourceSelector(selector string, policy *Policy) (grantSourceKind, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", errors.New("selector is blank")
	}
	if selector == "*" {
		return grantSourceWildcard, nil
	}
	if net.ParseIP(selector) != nil {
		return grantSourceMachine, nil
	}
	if _, _, err := net.ParseCIDR(selector); err == nil {
		return grantSourceMachine, nil
	}
	if strings.HasPrefix(selector, "group:") {
		if policy != nil {
			if _, ok := policy.Groups[selector]; ok {
				return grantSourceGroup, nil
			}
		}
		return "", errors.New("group is not defined")
	}
	if strings.HasPrefix(selector, "tag:") {
		if strings.TrimPrefix(selector, "tag:") != "" {
			return grantSourceMachine, nil
		}
		return "", errors.New("tag is blank")
	}
	if strings.HasPrefix(selector, "autogroup:") {
		role := strings.TrimPrefix(selector, "autogroup:")
		if tailscaleHumanRoles[role] {
			return grantSourceRole, nil
		}
		if grantSourceAutogroups[role] {
			return grantSourceMachine, nil
		}
		return "", errors.New("autogroup is not supported")
	}
	if unsupportedPolicySelector(selector) {
		return "", errors.New("selector class is not supported")
	}
	if policy != nil {
		if _, ok := policy.Hosts[selector]; ok {
			return grantSourceMachine, nil
		}
	}
	if strings.Contains(selector, "@") && !strings.Contains(selector, ":") {
		return grantSourceHuman, nil
	}
	return "", errors.New("selector class is not supported")
}

var tailscaleHumanRoles = map[string]bool{
	"admin": true, "member": true, "owner": true, "it-admin": true,
	"network-admin": true, "billing-admin": true, "auditor": true,
}

var grantSourceAutogroups = map[string]bool{
	"admin": true, "member": true, "owner": true, "it-admin": true,
	"network-admin": true, "billing-admin": true, "auditor": true,
	"tagged": true, "shared": true,
}

func validateGrantDestinationSelector(selector string, policy *Policy) error {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return errors.New("selector is blank")
	}
	if selector == "*" || selector == "autogroup:self" || net.ParseIP(selector) != nil {
		return nil
	}
	if _, _, err := net.ParseCIDR(selector); err == nil {
		return nil
	}
	if strings.HasPrefix(selector, "tag:") {
		if strings.TrimPrefix(selector, "tag:") != "" {
			return nil
		}
		return errors.New("tag is blank")
	}
	if reservedPolicySelector(selector) {
		return errors.New("selector class is not supported")
	}
	if policy != nil {
		if _, ok := policy.Hosts[selector]; ok {
			return nil
		}
	}
	return errors.New("selector class is not supported")
}

func grantSourcesSupportSelf(kinds []grantSourceKind) bool {
	if len(kinds) == 0 {
		return false
	}
	for _, kind := range kinds {
		if !kind.supportsSelf() {
			return false
		}
	}
	return true
}

func parseGrantIPCapability(raw string) (grantIPCapability, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return grantIPCapability{}, errors.New("must not be blank")
	}
	if value == "*" {
		return grantIPCapability{AllProtocols: true, AllPorts: true}, nil
	}
	if strings.Count(value, ":") > 1 {
		return grantIPCapability{}, errors.New("must contain at most one protocol separator")
	}
	if !strings.Contains(value, ":") {
		start, end, err := parseGrantPortRange(value)
		if err != nil {
			return grantIPCapability{}, err
		}
		return grantIPCapability{AllProtocols: true, PortStart: start, PortEnd: end}, nil
	}

	parts := strings.SplitN(value, ":", 2)
	protocol, err := parseGrantProtocol(parts[0])
	if err != nil {
		return grantIPCapability{}, err
	}
	if parts[1] == "*" {
		return grantIPCapability{Protocol: protocol, AllPorts: true}, nil
	}
	start, end, err := parseGrantPortRange(parts[1])
	if err != nil {
		return grantIPCapability{}, err
	}
	return grantIPCapability{Protocol: protocol, PortStart: start, PortEnd: end}, nil
}

func parseGrantProtocol(raw string) (uint8, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	aliases := map[string]uint8{
		"icmp": 1, "igmp": 2, "ipv4": 4, "ip-in-ip": 4, "tcp": 6,
		"egp": 8, "igp": 9, "udp": 17, "gre": 47, "esp": 50,
		"ah": 51, "sctp": 132,
	}
	if protocol, ok := aliases[value]; ok {
		return protocol, nil
	}
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > 255 {
		return 0, errors.New("protocol must be a documented alias or a number from 1 through 255")
	}
	return uint8(number), nil
}

func parseGrantPortRange(raw string) (int, int, error) {
	parts := strings.Split(raw, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, errors.New("port must be a number or one inclusive range")
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || start < 1 || start > 65535 {
		return 0, 0, errors.New("port must be between 1 and 65535")
	}
	end := start
	if len(parts) == 2 {
		end, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || end < start || end > 65535 {
			return 0, 0, errors.New("port range must be ascending and within 1 through 65535")
		}
	}
	return start, end, nil
}

func unmarshalRequiredStringSlice(fields map[string]json.RawMessage, name string, out *[]string) error {
	raw, ok := fields[name]
	if !ok {
		return fmt.Errorf("missing required field %q", name)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("field %q: %w", name, err)
	}
	if len(*out) == 0 {
		return fmt.Errorf("field %q must not be empty", name)
	}
	for _, value := range *out {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("field %q contains a blank value", name)
		}
	}
	return nil
}

func firstUnknownField(fields map[string]json.RawMessage, allowed ...string) string {
	allowedSet := make(map[string]bool, len(allowed))
	for _, field := range allowed {
		allowedSet[field] = true
	}
	unknown := make([]string, 0)
	for field := range fields {
		if !allowedSet[field] {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) == 0 {
		return ""
	}
	sort.Strings(unknown)
	return unknown[0]
}

func reservedPolicySelector(selector string) bool {
	if selector == "*" || net.ParseIP(selector) != nil {
		return true
	}
	if _, _, err := net.ParseCIDR(selector); err == nil {
		return true
	}
	return strings.HasPrefix(selector, "group:") ||
		strings.HasPrefix(selector, "tag:") ||
		strings.HasPrefix(selector, "autogroup:") ||
		unsupportedPolicySelector(selector) ||
		strings.Contains(selector, "@")
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

func jsonStringSliceEmpty(raw json.RawMessage) (bool, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return false, errors.New("must be an array of strings")
	}
	return len(values) == 0, nil
}

func jsonObjectEmpty(raw json.RawMessage) (bool, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("must be an object")
	}
	return len(value) == 0, nil
}

func jsonSectionObjectEmpty(raw json.RawMessage) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("must be an object, not null")
	}
	return jsonObjectEmpty(raw)
}

func jsonArrayEmpty(raw json.RawMessage) (bool, error) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New("must be an array, not null")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return false, errors.New("must be an array")
	}
	return len(values) == 0, nil
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
