package main

import (
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

const machineNonrootSelector = "autogroup:nonroot"

// tailscaleConsoleMachinesURL is the fixed Tailscale admin-console Machines page.
// Velociportal never builds any other console/device/session route.
const tailscaleConsoleMachinesURL = "https://console.tailscale.com/admin/machines"

// machineConsoleEligibleRoles lists the Tailscale human-role selectors permitted to
// see the browser SSH Console navigation action. This narrows which eligible
// viewers additionally receive the console link on an already matched machine
// card; it never widens machine or Grant TCP/22 evidence.
var machineConsoleEligibleRoles = map[string]bool{
	"autogroup:owner":         true,
	"autogroup:admin":         true,
	"autogroup:it-admin":      true,
	"autogroup:network-admin": true,
}

var tailscaleMachineAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

type MachineAccess struct {
	User        string        `json:"user"`
	Action      string        `json:"action"`
	CheckPeriod time.Duration `json:"check_period,omitempty"`
}

type MachineCard struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Target string          `json:"target"`
	Access []MachineAccess `json:"access"`
}

type machineSSHAccessEvidence struct {
	Access           MachineAccess
	RuleIndex        int
	SourceToken      string
	DestinationToken string
}

type machineMatchEvidence struct {
	Card             MachineCard
	Node             Node
	SSHAccess        []machineSSHAccessEvidence
	GrantRuleIndex   int
	GrantSourceToken string
	GrantDestination destinationMatchEvidence
}

func machineProjectionAvailable(data *CacheData) bool {
	return data != nil && data.Policy != nil &&
		data.ControlPlane.Provider == controlPlaneTailscale &&
		data.Policy.SSH.State == sshPolicySupported
}

// machineConsoleEligible reports whether the exact trusted login carries a
// Tailscale human role permitted to receive the browser SSH Console action. It
// reads only the existing GrantRoleSelectorsByLogin projection built from the
// complete Tailscale Users response; it does not consult devices, owners, tags,
// or policy text, and it hides the action entirely for Headscale.
func machineConsoleEligible(login string, data *CacheData) bool {
	if !machineProjectionAvailable(data) || !exactSSHLogin(login) {
		return false
	}
	roles, directMember := data.GrantRoleSelectorsByLogin[login]
	if !directMember {
		return false
	}
	for _, role := range roles {
		if machineConsoleEligibleRoles[role] {
			return true
		}
	}
	return false
}

// machineConsoleCapable narrows the browser-console navigation action to a
// device whose current Tailscale Devices response explicitly reported SSH
// enabled and incoming connections allowed. It is presentation-only metadata:
// missing evidence hides the link but never hides a policy-matched machine.
func machineConsoleCapable(machineID string, data *CacheData) bool {
	return data != nil && data.MachineSSHCapableByID[machineID]
}

// evaluateMachines is deliberately separate from service evaluation. A machine
// requires both a supported Tailscale SSH rule and a Tailscale Grant that permits
// TCP/22; legacy ACLs, NPM, service metadata, and health observations are not inputs.
func evaluateMachines(identity *Identity, data *CacheData) []machineMatchEvidence {
	if identity == nil || !machineProjectionAvailable(data) {
		return []machineMatchEvidence{}
	}

	login := strings.TrimSpace(identity.Login)
	if !exactSSHLogin(login) {
		return []machineMatchEvidence{}
	}
	roleMembership, directMember := data.GrantRoleSelectorsByLogin[login]
	if !directMember {
		// The complete Tailscale Users response records every direct member and no
		// shared user. Do not infer membership from devices, owners, or policy text.
		return []machineMatchEvidence{}
	}

	ids := buildSSHIdentitySet(login, data.Policy)
	roles := make(map[string]bool, len(roleMembership))
	for _, selector := range roleMembership {
		roles[selector] = true
	}
	mc := buildMatchContext(login, data)

	matches := make([]machineMatchEvidence, 0, len(data.Nodes))
	for _, node := range data.Nodes {
		target, safe := machineTarget(node)
		if !safe {
			continue
		}
		sshAccess := evaluateMachineSSHAccess(node, data.Policy.SSH.Rules, ids, roles, login)
		if len(sshAccess) == 0 {
			continue
		}
		grantIndex, grantSource, grantDestination, granted := machineGrantEvidence(node, data.Policy.Grants, ids, roles, mc)
		if !granted {
			continue
		}

		access := make([]MachineAccess, len(sshAccess))
		for index, evidence := range sshAccess {
			access[index] = evidence.Access
		}
		matches = append(matches, machineMatchEvidence{
			Card: MachineCard{
				ID:     node.ID,
				Name:   target,
				Target: target,
				Access: access,
			},
			Node:             node,
			SSHAccess:        sshAccess,
			GrantRuleIndex:   grantIndex,
			GrantSourceToken: grantSource,
			GrantDestination: grantDestination,
		})
	}

	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Card.Name != matches[j].Card.Name {
			return matches[i].Card.Name < matches[j].Card.Name
		}
		return matches[i].Card.ID < matches[j].Card.ID
	})
	return matches
}

func MatchMachines(identity *Identity, data *CacheData) []MachineCard {
	matches := evaluateMachines(identity, data)
	cards := make([]MachineCard, len(matches))
	for index, match := range matches {
		cards[index] = match.Card
	}
	return cards
}

func buildSSHIdentitySet(login string, policy *Policy) map[string]bool {
	ids := make(map[string]bool)
	if !exactSSHLogin(login) || policy == nil {
		return ids
	}
	ids[login] = true
	for group, members := range policy.Groups {
		for _, member := range members {
			if member == login {
				ids[group] = true
				break
			}
		}
	}
	return ids
}

func sourceGrantForSSHRule(rule SSHRule, ids, roles map[string]bool) (string, bool) {
	for _, source := range rule.Src {
		if ids[source] || roles[source] {
			return source, true
		}
	}
	return "", false
}

func sshDestinationGrant(node Node, destinations []string, login string) (string, bool) {
	for _, destination := range destinations {
		switch destination {
		case "autogroup:self":
			if node.OwnerLogin == login {
				return destination, true
			}
		default:
			if !strings.HasPrefix(destination, "tag:") {
				continue
			}
			for _, tag := range node.Tags {
				if tag == destination {
					return destination, true
				}
			}
		}
	}
	return "", false
}

func evaluateMachineSSHAccess(node Node, rules []SSHRule, ids, roles map[string]bool, login string) []machineSSHAccessEvidence {
	byUser := make(map[string]machineSSHAccessEvidence)
	for ruleIndex, rule := range rules {
		source, sourceMatched := sourceGrantForSSHRule(rule, ids, roles)
		if !sourceMatched {
			continue
		}
		destination, destinationMatched := sshDestinationGrant(node, rule.Dst, login)
		if !destinationMatched {
			continue
		}
		for _, user := range rule.Users {
			candidate := machineSSHAccessEvidence{
				Access: MachineAccess{
					User:        user,
					Action:      rule.Action,
					CheckPeriod: rule.CheckPeriod,
				},
				RuleIndex:        ruleIndex,
				SourceToken:      source,
				DestinationToken: destination,
			}
			current, exists := byUser[user]
			if !exists || current.Access.Action == "accept" && candidate.Access.Action == "check" {
				byUser[user] = candidate
			}
		}
	}

	// autogroup:nonroot overlaps every literal account except root. A matching
	// check rule therefore dominates any accept evidence for a literal non-root
	// account, while root remains available only through an explicit root rule.
	if nonroot, exists := byUser[machineNonrootSelector]; exists && nonroot.Access.Action == "check" {
		for user, evidence := range byUser {
			if user != "root" && user != machineNonrootSelector && evidence.Access.Action == "accept" {
				delete(byUser, user)
			}
		}
	}

	result := make([]machineSSHAccessEvidence, 0, len(byUser))
	for _, evidence := range byUser {
		result = append(result, evidence)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Access.User != result[j].Access.User {
			return result[i].Access.User < result[j].Access.User
		}
		if result[i].Access.Action != result[j].Access.Action {
			return result[i].Access.Action == "check"
		}
		return result[i].RuleIndex < result[j].RuleIndex
	})
	return result
}

func machineGrantEvidence(node Node, grants []GrantRule, ids, roles map[string]bool, mc *matchContext) (int, string, destinationMatchEvidence, bool) {
	for ruleIndex, grant := range grants {
		rule := accessRule{
			Kind:           accessRuleGrant,
			Index:          ruleIndex,
			Src:            grant.BrowserSrc,
			Dst:            grant.Dst,
			IPCapabilities: grant.IPCapabilities,
		}
		if !rule.permitsTCP(22) {
			continue
		}
		source, sourceMatched := sourceGrantForRule(rule, ids, roles)
		if !sourceMatched {
			continue
		}
		for _, rawAddress := range node.Addresses {
			address, safe := tailscaleMachineAddress(rawAddress)
			if !safe {
				continue
			}
			if destination, matched := destinationGrant(rule.Dst, address, mc); matched {
				return ruleIndex, source, destination, true
			}
		}
	}
	return 0, "", destinationMatchEvidence{}, false
}

func machineTarget(node Node) (string, bool) {
	if name, valid := normalizeMachineTargetName(node.Name); valid {
		return name, true
	}

	ipv6Fallback := ""
	for _, raw := range node.Addresses {
		address, safe := tailscaleMachineAddress(raw)
		if !safe {
			continue
		}
		parsed := netip.MustParseAddr(address)
		if parsed.Is4() {
			return address, true
		}
		if ipv6Fallback == "" {
			ipv6Fallback = address
		}
	}
	return ipv6Fallback, ipv6Fallback != ""
}

// machineShortName derives the short, familiar Tailscale machine name from the
// first label of an already validated canonical *.ts.net target. It reads only
// that validated target -- never the raw device hostname or a hostname-suggestion
// candidate -- and is byte-identical-checked the same way machineConsoleURL and
// machineSSHCommand validate their inputs. A validated IP fallback target has no
// separate short form and reports false.
func machineShortName(target string) (string, bool) {
	name, valid := normalizeMachineTargetName(target)
	if !valid || name != target {
		return "", false
	}
	label, _, found := strings.Cut(name, ".")
	if !found || label == "" {
		return "", false
	}
	return label, true
}

// machineConsoleURL builds the fixed, role-gated navigation target for Tailscale's
// browser SSH Console. It revalidates that target is byte-identical to a
// canonical *.ts.net MagicDNS name or a validated Tailscale CGNAT/ULA address --
// the same invariant machineSSHCommand enforces for its copy commands -- before
// ever placing it in a URL query value. Tailscale's own Machines search matches
// the short machine name, not the full canonical name, so a canonical target
// searches on its short name; a validated IP fallback target searches on the
// address itself. Velociportal never invents an account, session, device-ID
// route, or arbitrary host; SSH selection, reauthentication, and policy
// enforcement remain with Tailscale.
func machineConsoleURL(target string) (string, bool) {
	name, validName := normalizeMachineTargetName(target)
	address, validAddress := tailscaleMachineAddress(target)
	canonical := validName && name == target
	if !canonical && (!validAddress || address != target) {
		return "", false
	}

	term := target
	if canonical {
		short, ok := machineShortName(target)
		if !ok {
			return "", false
		}
		term = short
	}
	query := url.Values{}
	query.Set("q", term+" property:tailscale-ssh")
	return tailscaleConsoleMachinesURL + "?" + query.Encode(), true
}

func tailscaleMachineAddress(raw string) (string, bool) {
	address, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil || address.Zone() != "" {
		return "", false
	}
	address = address.Unmap()
	for _, prefix := range tailscaleMachineAddressPrefixes {
		if prefix.Contains(address) {
			return address.String(), true
		}
	}
	return "", false
}

func normalizeMachineTargetName(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", false
	}
	for _, character := range value {
		if character > 127 {
			return "", false
		}
	}
	value = strings.ToLower(strings.TrimSuffix(value, "."))
	if strings.HasSuffix(value, ".") || !strings.HasSuffix(value, ".ts.net") || strings.Count(value, ".") < 3 {
		return "", false
	}
	if err := validateServiceHealthHost(value); err != nil {
		return "", false
	}
	return value, true
}
