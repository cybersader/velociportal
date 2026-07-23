package main

import (
	"log/slog"
	"net"
	"sort"
	"strings"
)

type ServiceCard struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Domain string `json:"domain"`
	Online bool   `json:"online"`
}

// normalizeLogin turns "alice@example.com" or "alice@" into the ACL form "alice@".
func normalizeLogin(login string) string {
	if i := strings.IndexByte(login, '@'); i >= 0 {
		return login[:i+1]
	}
	return login + "@"
}

// buildIdentitySet returns the set of ACL src tokens that identify the user:
// their normalized login plus every group they belong to.
//
// Note: tagOwners is deliberately NOT consulted here. tagOwners only says who may
// ASSIGN a tag to a node — it does not make the user "be" that tag. Tag-based src
// matching is handled in MatchServices by looking at tags the user's own nodes wear.
func buildIdentitySet(identity *Identity, policy *Policy) map[string]bool {
	set := map[string]bool{}
	login := normalizeLogin(identity.Login)
	set[login] = true

	for group, members := range policy.Groups {
		for _, m := range members {
			if normalizeLogin(m) == login {
				set[group] = true
				break
			}
		}
	}

	return set
}

func srcGranted(src []string, ids map[string]bool) bool {
	for _, s := range src {
		if s == "*" || ids[s] {
			return true
		}
	}
	return false
}

// matchContext carries the resolved data needed to match ACL dst entries against a
// proxy host's forward address: host aliases, tag→IP resolution, and the requesting
// user's own node IPs (for autogroup:self).
type matchContext struct {
	hosts   map[string]string   // Policy.Hosts: alias name -> IP/CIDR
	tagIPs  map[string][]string // tag -> IPs of all nodes wearing that tag
	selfIPs []string            // IPs of the requesting user's own nodes
}

func dstMatches(dst []string, host string, mc *matchContext) bool {
	for _, d := range dst {
		if matchDst(stripPort(d), host, mc) {
			return true
		}
	}
	return false
}

// matchDst decides whether a single, port-stripped ACL dst entry matches a proxy
// host's forward address. It handles wildcards, host aliases, tags, autogroups,
// CIDRs, and exact IP/host matches.
func matchDst(d, host string, mc *matchContext) bool {
	// Resolve host aliases (Policy.Hosts) to their underlying IP/CIDR first.
	if mc != nil && mc.hosts != nil {
		if resolved, ok := mc.hosts[d]; ok {
			d = stripPort(resolved)
		}
	}

	switch {
	case d == "*" || d == "autogroup:internet":
		// autogroup:internet is all non-Tailscale traffic — treat as match-all here.
		return true
	case d == host:
		return true
	case strings.HasPrefix(d, "tag:"):
		if mc != nil {
			for _, ip := range mc.tagIPs[d] {
				if ip == host {
					return true
				}
			}
		}
		return false
	case d == "autogroup:self":
		if mc != nil {
			for _, ip := range mc.selfIPs {
				if ip == host {
					return true
				}
			}
		}
		return false
	case strings.HasPrefix(d, "autogroup:"):
		slog.Debug("unsupported autogroup in dst, skipping", "autogroup", d)
		return false
	case strings.Contains(d, "/"):
		_, cidr, err := net.ParseCIDR(d)
		if err != nil {
			slog.Debug("invalid CIDR in dst", "dst", d, "err", err)
			return false
		}
		ip := net.ParseIP(host)
		return ip != nil && cidr.Contains(ip)
	default:
		return false
	}
}

// stripPort removes a trailing ":port" from an ACL dst entry without mangling IPv6.
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
	tags := append([]string(nil), n.ForcedTags...)
	tags = append(tags, n.ValidTags...)
	return tags
}

func MatchServices(identity *Identity, data *CacheData) []ServiceCard {
	if data == nil || data.Policy == nil || identity == nil {
		return []ServiceCard{}
	}

	login := normalizeLogin(identity.Login)
	ids := buildIdentitySet(identity, data.Policy)

	// Resolve node data once per call:
	//   - tagIPs: every tag -> IPs of nodes wearing it (for tag/CIDR dst matching)
	//   - selfIPs: the requesting user's own node IPs (for autogroup:self)
	//   - a tag worn by one of the user's OWN nodes counts as a src identity, so an
	//     ACL src of tag:foo grants the user when a node they own wears tag:foo.
	tagIPs := map[string][]string{}
	var selfIPs []string
	for _, n := range data.Nodes {
		owned := normalizeLogin(n.User.Name) == login
		for _, tag := range nodeTags(n) {
			tagIPs[tag] = append(tagIPs[tag], n.IPAddresses...)
			if owned {
				ids[tag] = true
			}
		}
		if owned {
			selfIPs = append(selfIPs, n.IPAddresses...)
		}
	}

	mc := &matchContext{
		hosts:   data.Policy.Hosts,
		tagIPs:  tagIPs,
		selfIPs: selfIPs,
	}

	slog.Debug("matching services",
		"user", login, "identities", len(ids), "nodes", len(data.Nodes),
		"proxy_hosts", len(data.ProxyHosts))

	cards := []ServiceCard{}
	for _, ph := range data.ProxyHosts {
		if !ph.Enabled || len(ph.DomainNames) == 0 {
			continue
		}

		granted := false
		for i, acl := range data.Policy.ACLs {
			if acl.Action != "accept" {
				continue
			}
			if srcGranted(acl.Src, ids) && dstMatches(acl.Dst, ph.ForwardHost, mc) {
				slog.Debug("service granted",
					"user", login, "domain", ph.DomainNames[0],
					"host", ph.ForwardHost, "acl_index", i)
				granted = true
				break
			}
		}
		if !granted {
			slog.Debug("service rejected",
				"user", login, "domain", ph.DomainNames[0], "host", ph.ForwardHost)
			continue
		}

		domain := ph.DomainNames[0]
		scheme := ph.ForwardScheme
		if scheme == "" {
			scheme = "https"
		}

		cards = append(cards, ServiceCard{
			ID:     ph.ID,
			Name:   domain,
			URL:    scheme + "://" + domain,
			Domain: domain,
			Online: ph.Meta.NginxOnline,
		})
	}

	sort.Slice(cards, func(i, j int) bool {
		return strings.ToLower(cards[i].Name) < strings.ToLower(cards[j].Name)
	})

	return cards
}
