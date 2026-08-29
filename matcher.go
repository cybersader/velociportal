package main

import (
	"log/slog"
	"net"
	"net/url"
	"sort"
	"strings"
)

type serviceLinkState string

const (
	serviceLinkReady         serviceLinkState = "ready"
	serviceLinkNeedsMetadata serviceLinkState = "needs_metadata"
	serviceLinkInvalid       serviceLinkState = "invalid"
)

type ServiceCard struct {
	ID        int              `json:"id"`
	Name      string           `json:"name"`
	URL       string           `json:"url"`
	Domain    string           `json:"domain"`
	LinkState serviceLinkState `json:"link_state"`
}

type destinationMatchKind string

const (
	destinationMatchExact     destinationMatchKind = "exact"
	destinationMatchWildcard  destinationMatchKind = "wildcard"
	destinationMatchCIDR      destinationMatchKind = "cidr"
	destinationMatchHostAlias destinationMatchKind = "host_alias"
	destinationMatchTag       destinationMatchKind = "tag"
	destinationMatchSelf      destinationMatchKind = "autogroup_self"
)

type destinationMatchEvidence struct {
	Kind               destinationMatchKind
	Selector           string
	NormalizedSelector string
	ResolvedValue      string
}

type serviceMatchEvidence struct {
	Card        ServiceCard
	ProxyHost   ProxyHost
	RuleKind    accessRuleKind
	RuleIndex   int
	SourceToken string
	Destination destinationMatchEvidence
}

// normalizeLogin preserves fully qualified identities, canonicalizes legacy bare
// names to the explicit short form ("alice" -> "alice@"), and rejects blank input.
func normalizeLogin(login string) string {
	login = strings.TrimSpace(login)
	if login == "" {
		return ""
	}
	if strings.Contains(login, "@") {
		return login
	}
	return login + "@"
}

// identityTokens returns the policy source forms that identify a login. Fully
// qualified identities match only exactly; short/bare legacy identities may use
// both "alice@" and "alice". This fails closed rather than collapsing domains.
func identityTokens(login string) map[string]bool {
	set := map[string]bool{}
	login = normalizeLogin(login)
	if login == "" {
		return set
	}

	set[login] = true
	if strings.HasSuffix(login, "@") {
		if local := strings.TrimSuffix(login, "@"); local != "" {
			set[local] = true
		}
	}
	return set
}

// loginMatches reports whether candidate identifies login without collapsing fully
// qualified identities to a shared local part.
func loginMatches(login, candidate string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	return identityTokens(login)[candidate]
}

// buildIdentitySet returns the safe access-rule source tokens that identify the
// user plus every group with a matching member. Fully qualified logins do not gain
// short aliases.
//
// tagOwners is deliberately NOT consulted here. It only says who may assign a tag
// to a node; neither tag ownership nor tags on owned nodes make a human identity be
// that tag.
func buildIdentitySet(identity *Identity, policy *Policy) map[string]bool {
	if identity == nil {
		return map[string]bool{}
	}
	set := identityTokens(identity.Login)
	if len(set) == 0 || policy == nil {
		return set
	}

	for group, members := range policy.Groups {
		for _, member := range members {
			if set[strings.TrimSpace(member)] {
				set[group] = true
				break
			}
		}
	}

	return set
}

func sourceGrant(src []string, ids map[string]bool) (string, bool) {
	for _, source := range src {
		if source == "*" || ids[source] {
			return source, true
		}
	}
	return "", false
}

func srcGranted(src []string, ids map[string]bool) bool {
	_, granted := sourceGrant(src, ids)
	return granted
}

func sourceGrantForRule(rule accessRule, ids, roleSelectors map[string]bool) (string, bool) {
	for _, source := range rule.Src {
		if source == "*" || ids[source] || (rule.Kind == accessRuleGrant && roleSelectors[source]) {
			return source, true
		}
	}
	return "", false
}

func grantRoleSelectorSet(login string, data *CacheData) map[string]bool {
	set := make(map[string]bool)
	if data == nil || login == "" {
		return set
	}
	for _, selector := range data.GrantRoleSelectorsByLogin[login] {
		set[selector] = true
	}
	return set
}

// matchContext carries the resolved data needed to match access-rule destinations
// against a proxy host's forward address: host aliases, tag→IP resolution, and the
// requesting user's own node IPs (for autogroup:self).
type matchContext struct {
	hosts   map[string]string   // Policy.Hosts: alias name -> IP/CIDR
	tagIPs  map[string][]string // tag -> IPs of all nodes wearing that tag
	selfIPs []string            // IPs of the requesting user's own nodes
}

func destinationGrant(dst []string, host string, mc *matchContext) (destinationMatchEvidence, bool) {
	for _, selector := range dst {
		if evidence, matched := matchDestination(selector, host, mc); matched {
			return evidence, true
		}
	}
	return destinationMatchEvidence{}, false
}

func dstMatches(dst []string, host string, mc *matchContext) bool {
	_, matched := destinationGrant(dst, host, mc)
	return matched
}

func matchDestination(selector, host string, mc *matchContext) (destinationMatchEvidence, bool) {
	normalized := stripPort(selector)
	if !reservedPolicySelector(normalized) && mc != nil && mc.hosts != nil {
		if resolved, ok := mc.hosts[normalized]; ok {
			resolved = stripPort(resolved)
			if _, matched := matchResolvedDestination(resolved, host, mc); matched {
				return destinationMatchEvidence{
					Kind:               destinationMatchHostAlias,
					Selector:           selector,
					NormalizedSelector: normalized,
					ResolvedValue:      resolved,
				}, true
			}
			return destinationMatchEvidence{}, false
		}
	}

	kind, matched := matchResolvedDestination(normalized, host, mc)
	if !matched {
		return destinationMatchEvidence{}, false
	}
	return destinationMatchEvidence{
		Kind:               kind,
		Selector:           selector,
		NormalizedSelector: normalized,
		ResolvedValue:      resolvedMatchValue(kind, normalized, host),
	}, true
}

func matchResolvedDestination(selector, host string, mc *matchContext) (destinationMatchKind, bool) {
	switch {
	case selector == "*":
		return destinationMatchWildcard, true
	case selector == host:
		return destinationMatchExact, true
	case strings.HasPrefix(selector, "tag:"):
		if mc != nil {
			for _, ip := range mc.tagIPs[selector] {
				if ip == host {
					return destinationMatchTag, true
				}
			}
		}
		return "", false
	case selector == "autogroup:self":
		if mc != nil {
			for _, ip := range mc.selfIPs {
				if ip == host {
					return destinationMatchSelf, true
				}
			}
		}
		return "", false
	case strings.HasPrefix(selector, "autogroup:"):
		slog.Debug("unsupported autogroup in dst, skipping", "autogroup", selector)
		return "", false
	case strings.Contains(selector, "/"):
		_, cidr, err := net.ParseCIDR(selector)
		if err != nil {
			slog.Debug("invalid CIDR in dst", "dst", selector, "err", err)
			return "", false
		}
		ip := net.ParseIP(host)
		if ip != nil && cidr.Contains(ip) {
			return destinationMatchCIDR, true
		}
		return "", false
	default:
		return "", false
	}
}

func resolvedMatchValue(kind destinationMatchKind, selector, host string) string {
	switch kind {
	case destinationMatchWildcard, destinationMatchTag, destinationMatchSelf:
		return host
	default:
		return selector
	}
}

// matchDst decides whether one access-rule destination matches a proxy host's
// forward address. The evidence-returning path above is authoritative.
func matchDst(d, host string, mc *matchContext) bool {
	_, matched := matchDestination(d, host, mc)
	return matched
}

// stripPort removes a trailing ":port" from a legacy ACL destination without
// mangling IPv6. Grant destinations carry ports separately in network capabilities.
//
//   - Bracketed IPv6 (with or without a port): "[::1]:443" -> "::1", "[fd7a::1]" -> "fd7a::1"
//   - Bare IP literal (v4 or v6): returned unchanged so IPv6 colons survive.
//   - Otherwise: a trailing ":<digits>" or ":*" is stripped ("10.0.0.1:443" -> "10.0.0.1",
//     "tag:server:*" -> "tag:server"); anything else is returned as-is ("tag:server").
func stripPort(d string) string {
	if strings.HasPrefix(d, "[") {
		if end := strings.IndexByte(d, ']'); end >= 0 {
			return d[1:end]
		}
	}
	if net.ParseIP(d) != nil {
		return d
	}
	if i := strings.LastIndexByte(d, ':'); i >= 0 && isPortLike(d[i+1:]) {
		return d[:i]
	}
	return d
}

// isPortLike reports whether s is a port specifier: all digits, or the "*" wildcard.
func isPortLike(s string) bool {
	if s == "*" {
		return true
	}
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func nodeTags(n Node) []string {
	return n.Tags
}

func buildMatchContext(login string, data *CacheData) *matchContext {
	tagIPs := map[string][]string{}
	var selfIPs []string
	for _, node := range data.Nodes {
		owned := loginMatches(login, node.OwnerLogin)
		for _, tag := range nodeTags(node) {
			tagIPs[tag] = append(tagIPs[tag], node.Addresses...)
		}
		if owned {
			selfIPs = append(selfIPs, node.Addresses...)
		}
	}
	return &matchContext{
		hosts:   data.Policy.Hosts,
		tagIPs:  tagIPs,
		selfIPs: selfIPs,
	}
}

// structuralDestinationMatches returns identity-independent destination evidence
// for a proxy host plus whether at least one supported destination depends on the
// requesting identity. Source selectors are deliberately not evaluated here.
func structuralDestinationMatches(proxyHost ProxyHost, snapshot *CacheData) ([]destinationMatchEvidence, bool) {
	matches := []destinationMatchEvidence{}
	if snapshot == nil || snapshot.Policy == nil {
		return matches, false
	}

	mc := buildMatchContext("", snapshot)
	identityDependent := false
	for _, rule := range snapshot.Policy.accessRules() {
		if !rule.permitsTCP(proxyHost.ForwardPort) {
			continue
		}
		for _, selector := range rule.Dst {
			if stripPort(selector) == "autogroup:self" {
				identityDependent = true
				continue
			}
			if evidence, matched := matchDestination(selector, proxyHost.ForwardHost, mc); matched {
				matches = append(matches, evidence)
			}
		}
	}
	return matches, identityDependent
}

// enabledProxyHostHasSupportedDestinationMatch reports whether an enabled NPM
// proxy host has at least one identity-independent destination match in a complete
// snapshot. It does not evaluate sources and therefore does not authorize access.
func enabledProxyHostHasSupportedDestinationMatch(proxyHost ProxyHost, snapshot *CacheData) bool {
	if !proxyHost.Enabled || snapshot == nil || snapshot.Policy == nil {
		return false
	}
	matches, _ := structuralDestinationMatches(proxyHost, snapshot)
	return len(matches) > 0
}

func resolveServiceCard(proxyHost ProxyHost, metadata *ServiceMetadata) (ServiceCard, bool) {
	domains := make([]string, 0, len(proxyHost.DomainNames))
	concreteDomain := ""
	for _, raw := range proxyHost.DomainNames {
		domain := strings.TrimSpace(raw)
		if domain == "" {
			continue
		}
		domains = append(domains, domain)
		if concreteDomain == "" && validConcreteCardDomain(domain) {
			concreteDomain = domain
		}
	}
	if len(domains) == 0 {
		return ServiceCard{}, false
	}

	card := ServiceCard{
		ID:        proxyHost.ID,
		Name:      domains[0],
		Domain:    domains[0],
		LinkState: serviceLinkNeedsMetadata,
	}
	if concreteDomain != "" {
		card.Name = concreteDomain
		card.Domain = concreteDomain
	}

	if metadata != nil {
		if override, exists := metadata.Overrides[proxyHost.ID]; exists {
			if override.Name != "" {
				card.Name = override.Name
			}
			if override.URL != "" {
				card.URL = override.URL
				card.LinkState = serviceLinkReady
				return card, true
			}
		}
	}

	if concreteDomain == "" {
		return card, true
	}
	scheme := strings.ToLower(strings.TrimSpace(proxyHost.ForwardScheme))
	if scheme == "" {
		scheme = "https"
	}
	if scheme != "http" && scheme != "https" {
		card.LinkState = serviceLinkInvalid
		return card, true
	}
	card.URL = (&url.URL{Scheme: scheme, Host: concreteDomain}).String()
	card.LinkState = serviceLinkReady
	return card, true
}

func validConcreteCardDomain(domain string) bool {
	if domain == "" || containsControl(domain) || strings.ContainsAny(domain, "*\\/?#@") || strings.ContainsAny(domain, " \t") {
		return false
	}
	parsed, err := url.Parse("https://" + domain)
	if err != nil || parsed.Host != domain || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return true
}

func evaluateServices(identity *Identity, data *CacheData) []serviceMatchEvidence {
	if data == nil || data.Policy == nil || identity == nil {
		return []serviceMatchEvidence{}
	}

	exactLogin := strings.TrimSpace(identity.Login)
	login := normalizeLogin(exactLogin)
	if login == "" {
		return []serviceMatchEvidence{}
	}
	ids := buildIdentitySet(identity, data.Policy)
	roleSelectors := grantRoleSelectorSet(exactLogin, data)
	mc := buildMatchContext(login, data)

	slog.Debug("matching services",
		"identities", len(ids), "nodes", len(data.Nodes),
		"proxy_hosts", len(data.ProxyHosts))

	matches := []serviceMatchEvidence{}
	for _, proxyHost := range data.ProxyHosts {
		if !proxyHost.Enabled || len(proxyHost.DomainNames) == 0 {
			continue
		}

		for _, rule := range data.Policy.accessRules() {
			if !rule.permitsTCP(proxyHost.ForwardPort) {
				continue
			}
			source, sourceMatched := sourceGrantForRule(rule, ids, roleSelectors)
			if !sourceMatched {
				continue
			}
			destination, destinationMatched := destinationGrant(rule.Dst, proxyHost.ForwardHost, mc)
			if !destinationMatched {
				continue
			}

			card, renderable := resolveServiceCard(proxyHost, data.ServiceMetadata)
			if !renderable {
				break
			}
			matches = append(matches, serviceMatchEvidence{
				Card:        card,
				ProxyHost:   proxyHost,
				RuleKind:    rule.Kind,
				RuleIndex:   rule.Index,
				SourceToken: source,
				Destination: destination,
			})
			slog.Debug("service granted", "proxy_host_id", proxyHost.ID, "rule_kind", rule.Kind, "rule_index", rule.Index)
			break
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		left := strings.ToLower(matches[i].Card.Name)
		right := strings.ToLower(matches[j].Card.Name)
		if left != right {
			return left < right
		}
		return matches[i].Card.ID < matches[j].Card.ID
	})
	return matches
}

func MatchServices(identity *Identity, data *CacheData) []ServiceCard {
	matches := evaluateServices(identity, data)
	cards := make([]ServiceCard, len(matches))
	for index, match := range matches {
		cards[index] = match.Card
	}
	return cards
}
