package main

import (
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

	for tag, owners := range policy.TagOwners {
		for _, o := range owners {
			if set[o] || normalizeLogin(o) == login {
				set[tag] = true
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

func dstMatches(dst []string, host string) bool {
	for _, d := range dst {
		d = stripPort(d)
		if d == "*" || d == host {
			return true
		}
	}
	return false
}

// stripPort removes a trailing ":port" from an ACL dst entry, keeping IPv6 brackets aside.
func stripPort(d string) string {
	if i := strings.LastIndexByte(d, ':'); i >= 0 {
		return d[:i]
	}
	return d
}

func MatchServices(identity *Identity, data *CacheData) []ServiceCard {
	if data == nil || data.Policy == nil || identity == nil {
		return []ServiceCard{}
	}

	ids := buildIdentitySet(identity, data.Policy)
	cards := []ServiceCard{}

	for _, ph := range data.ProxyHosts {
		if !ph.Enabled || len(ph.DomainNames) == 0 {
			continue
		}

		granted := false
		for _, acl := range data.Policy.ACLs {
			if acl.Action != "accept" {
				continue
			}
			if srcGranted(acl.Src, ids) && dstMatches(acl.Dst, ph.ForwardHost) {
				granted = true
				break
			}
		}
		if !granted {
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
