package main

import (
	"testing"
)

func TestNormalizeLogin(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"alice@example.com", "alice@"},
		{"alice@", "alice@"},
		{"alice", "alice@"},
		{"bob@corp.net", "bob@"},
		{"", "@"},
	}
	for _, tt := range tests {
		if got := normalizeLogin(tt.input); got != tt.want {
			t.Errorf("normalizeLogin(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestStripPort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"10.0.0.1:443", "10.0.0.1"},
		{"10.0.0.1:*", "10.0.0.1"},
		{"10.0.0.1", "10.0.0.1"},
		{"*", "*"},
		{"host:80", "host"},
		// IPv6: bare literals must survive untouched; bracketed forms drop the port.
		{"fd7a::1", "fd7a::1"},
		{"::1", "::1"},
		{"[::1]:443", "::1"},
		{"[fd7a::1]:8080", "fd7a::1"},
		{"[fd7a::1]", "fd7a::1"},
		// Tags carry a port suffix that must be stripped, but the "tag:name" kept.
		{"tag:server:*", "tag:server"},
		{"tag:server:443", "tag:server"},
		{"tag:server", "tag:server"},
		// CIDRs with and without a port.
		{"10.0.0.0/24:443", "10.0.0.0/24"},
		{"10.0.0.0/24", "10.0.0.0/24"},
	}
	for _, tt := range tests {
		if got := stripPort(tt.input); got != tt.want {
			t.Errorf("stripPort(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildIdentitySet(t *testing.T) {
	policy := &Policy{
		Groups: map[string][]string{
			"group:admin": {"alice@example.com", "bob@"},
			"group:dev":   {"charlie@example.com"},
		},
		TagOwners: map[string][]string{
			"tag:server": {"group:admin"},
			"tag:ci":     {"charlie@example.com"},
		},
	}

	t.Run("member of group", func(t *testing.T) {
		id := &Identity{Login: "alice@example.com"}
		set := buildIdentitySet(id, policy)

		for _, want := range []string{"alice@", "group:admin"} {
			if !set[want] {
				t.Errorf("expected %q in identity set", want)
			}
		}
		if set["group:dev"] {
			t.Error("alice should not be in group:dev")
		}
		// Bug 3 fix: tagOwners only controls who may ASSIGN a tag; being an owner
		// must NOT put the tag in the identity set.
		if set["tag:server"] {
			t.Error("owning tag:server via tagOwners must not add it to the identity set")
		}
	})

	t.Run("member of group via short login", func(t *testing.T) {
		id := &Identity{Login: "bob@"}
		set := buildIdentitySet(id, policy)

		if !set["group:admin"] {
			t.Error("bob@ should be in group:admin")
		}
		if set["tag:server"] {
			t.Error("tag:server must not be granted via tagOwners")
		}
	})

	t.Run("tag owner by direct login does not get the tag", func(t *testing.T) {
		id := &Identity{Login: "charlie@example.com"}
		set := buildIdentitySet(id, policy)

		if !set["group:dev"] {
			t.Error("charlie should be in group:dev")
		}
		if set["tag:ci"] {
			t.Error("owning tag:ci via tagOwners must not add it to the identity set")
		}
	})

	t.Run("unknown user gets only login", func(t *testing.T) {
		id := &Identity{Login: "nobody@example.com"}
		set := buildIdentitySet(id, policy)

		if !set["nobody@"] {
			t.Error("should have normalized login")
		}
		if len(set) != 1 {
			t.Errorf("unknown user should have 1 entry, got %d", len(set))
		}
	})
}

func TestSrcGranted(t *testing.T) {
	ids := map[string]bool{"alice@": true, "group:admin": true}

	tests := []struct {
		src  []string
		want bool
	}{
		{[]string{"*"}, true},
		{[]string{"alice@"}, true},
		{[]string{"group:admin"}, true},
		{[]string{"bob@"}, false},
		{[]string{"group:dev"}, false},
		{[]string{"bob@", "group:admin"}, true},
	}
	for _, tt := range tests {
		if got := srcGranted(tt.src, ids); got != tt.want {
			t.Errorf("srcGranted(%v) = %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestDstMatches(t *testing.T) {
	tests := []struct {
		dst  []string
		host string
		want bool
	}{
		{[]string{"*"}, "10.0.0.1", true},
		{[]string{"10.0.0.1:443"}, "10.0.0.1", true},
		{[]string{"10.0.0.1:*"}, "10.0.0.1", true},
		{[]string{"10.0.0.2:443"}, "10.0.0.1", false},
		{[]string{"10.0.0.1"}, "10.0.0.1", true},
	}
	for _, tt := range tests {
		if got := dstMatches(tt.dst, tt.host, nil); got != tt.want {
			t.Errorf("dstMatches(%v, %q) = %v, want %v", tt.dst, tt.host, got, tt.want)
		}
	}
}

func TestDstMatchesAdvanced(t *testing.T) {
	mc := &matchContext{
		hosts: map[string]string{
			"webserver": "10.0.0.5",
			"lan":       "10.0.0.0/24",
		},
		tagIPs: map[string][]string{
			"tag:server": {"10.0.0.1", "10.0.0.2"},
		},
		selfIPs: []string{"100.64.0.9"},
	}

	tests := []struct {
		name string
		dst  []string
		host string
		want bool
	}{
		{"cidr contains host", []string{"10.0.0.0/24:*"}, "10.0.0.5", true},
		{"cidr excludes host", []string{"10.0.0.0/24:*"}, "10.1.0.5", false},
		{"cidr with explicit port", []string{"10.0.0.0/24:443"}, "10.0.0.200", true},
		{"tag resolves to node ip", []string{"tag:server:*"}, "10.0.0.1", true},
		{"tag does not match other ip", []string{"tag:server:*"}, "10.0.0.9", false},
		{"unknown tag never matches", []string{"tag:unknown:*"}, "10.0.0.1", false},
		{"host alias to ip", []string{"webserver:443"}, "10.0.0.5", true},
		{"host alias to cidr", []string{"lan:*"}, "10.0.0.42", true},
		{"autogroup:internet matches all", []string{"autogroup:internet"}, "10.0.0.99", true},
		{"autogroup:self matches own node", []string{"autogroup:self:*"}, "100.64.0.9", true},
		{"autogroup:self excludes others", []string{"autogroup:self:*"}, "100.64.0.1", false},
		{"unsupported autogroup skipped", []string{"autogroup:member:*"}, "10.0.0.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := dstMatches(tt.dst, tt.host, mc); got != tt.want {
				t.Errorf("dstMatches(%v, %q) = %v, want %v", tt.dst, tt.host, got, tt.want)
			}
		})
	}
}

func TestMatchServices(t *testing.T) {
	policy := &Policy{
		Groups: map[string][]string{
			"group:admin": {"alice@"},
			"group:dev":   {"bob@"},
		},
		ACLs: []ACLRule{
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"10.0.0.1:*"}},
			{Action: "accept", Src: []string{"group:dev"}, Dst: []string{"10.0.0.2:*"}},
			{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.3:*"}},
		},
	}

	data := &CacheData{
		Policy: policy,
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"grafana.example.com"}, ForwardScheme: "http", ForwardHost: "10.0.0.1", ForwardPort: 3000, Enabled: true, Meta: ProxyHostMeta{NginxOnline: true}},
			{ID: 2, DomainNames: []string{"jenkins.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.2", ForwardPort: 8080, Enabled: true, Meta: ProxyHostMeta{NginxOnline: true}},
			{ID: 3, DomainNames: []string{"wiki.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.3", ForwardPort: 443, Enabled: true, Meta: ProxyHostMeta{NginxOnline: false}},
			{ID: 4, DomainNames: []string{"disabled.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.1", ForwardPort: 443, Enabled: false},
		},
	}

	t.Run("admin sees grafana and wiki", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "alice@"}, data)
		names := cardNames(cards)

		assertContains(t, names, "grafana.example.com")
		assertContains(t, names, "wiki.example.com")
		assertNotContains(t, names, "jenkins.example.com")
		assertNotContains(t, names, "disabled.example.com")
	})

	t.Run("dev sees jenkins and wiki", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "bob@"}, data)
		names := cardNames(cards)

		assertContains(t, names, "jenkins.example.com")
		assertContains(t, names, "wiki.example.com")
		assertNotContains(t, names, "grafana.example.com")
	})

	t.Run("unknown user sees only wildcard services", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "nobody@"}, data)
		if len(cards) != 1 || cards[0].Name != "wiki.example.com" {
			t.Errorf("expected only wiki, got %v", cardNames(cards))
		}
	})

	t.Run("nil inputs return empty", func(t *testing.T) {
		if cards := MatchServices(nil, data); len(cards) != 0 {
			t.Error("nil identity should return empty")
		}
		if cards := MatchServices(&Identity{Login: "a@"}, nil); len(cards) != 0 {
			t.Error("nil data should return empty")
		}
	})

	t.Run("cards are sorted by name", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "alice@"}, data)
		for i := 1; i < len(cards); i++ {
			if cards[i-1].Name > cards[i].Name {
				t.Errorf("cards not sorted: %q > %q", cards[i-1].Name, cards[i].Name)
			}
		}
	})

	t.Run("default scheme is https", func(t *testing.T) {
		noScheme := &CacheData{
			Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.5:*"}}}},
			ProxyHosts: []ProxyHost{
				{ID: 10, DomainNames: []string{"app.test"}, ForwardHost: "10.0.0.5", Enabled: true},
			},
		}
		cards := MatchServices(&Identity{Login: "x@"}, noScheme)
		if len(cards) != 1 || cards[0].URL != "https://app.test" {
			t.Errorf("expected https://app.test, got %v", cards)
		}
	})

	t.Run("online status propagates", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "alice@"}, data)
		for _, c := range cards {
			if c.Name == "grafana.example.com" && !c.Online {
				t.Error("grafana should be online")
			}
			if c.Name == "wiki.example.com" && c.Online {
				t.Error("wiki should be offline")
			}
		}
	})
}

func TestMatchServicesTagsAndCIDR(t *testing.T) {
	policy := &Policy{
		Groups: map[string][]string{
			"group:admin": {"alice@"},
		},
		TagOwners: map[string][]string{
			// carol owns tag:server (may assign it) but owning it must not grant access.
			"tag:server": {"carol@"},
		},
		Hosts: map[string]string{
			"webserver": "10.0.0.5",
		},
		ACLs: []ACLRule{
			// src is a tag: only users whose own nodes wear tag:server are granted.
			{Action: "accept", Src: []string{"tag:server"}, Dst: []string{"tag:server:*"}},
			// CIDR destination.
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"10.0.0.0/24:443"}},
			// host alias destination.
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"webserver:*"}},
			// a genuinely public service everyone may see.
			{Action: "accept", Src: []string{"*"}, Dst: []string{"203.0.113.4:*"}},
		},
	}

	nodes := []Node{
		// alice owns a node wearing tag:server at 10.0.0.1.
		{ID: "1", Name: "alice-node", User: User{Name: "alice@"}, ForcedTags: []string{"tag:server"}, IPAddresses: []string{"10.0.0.1"}},
		// carol owns a node but it does NOT wear tag:server.
		{ID: "2", Name: "carol-node", User: User{Name: "carol@"}, IPAddresses: []string{"10.0.0.7"}},
	}

	data := &CacheData{
		Policy: policy,
		Nodes:  nodes,
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"tagged.example.com"}, ForwardHost: "10.0.0.1", Enabled: true},
			{ID: 2, DomainNames: []string{"cidr.example.com"}, ForwardHost: "10.0.0.200", Enabled: true},
			{ID: 3, DomainNames: []string{"alias.example.com"}, ForwardHost: "10.0.0.5", Enabled: true},
			{ID: 4, DomainNames: []string{"public.example.com"}, ForwardHost: "203.0.113.4", Enabled: true},
		},
	}

	t.Run("tag src matches when user's own node wears the tag", func(t *testing.T) {
		// alice's node wears tag:server, so the tag-src/tag-dst rule grants tagged service.
		names := cardNames(MatchServices(&Identity{Login: "alice@"}, data))
		assertContains(t, names, "tagged.example.com")
	})

	t.Run("cidr and host alias destinations resolve", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "alice@"}, data))
		assertContains(t, names, "cidr.example.com")  // 10.0.0.200 in 10.0.0.0/24
		assertContains(t, names, "alias.example.com") // webserver -> 10.0.0.5
	})

	t.Run("public service (src *) reaches everyone", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "nobody@"}, data))
		assertContains(t, names, "public.example.com")
	})

	t.Run("autogroup:internet dst matches (equivalent to *)", func(t *testing.T) {
		// Isolated policy: autogroup:internet behaves as match-all, so any granted src
		// sees the service regardless of the proxy host's forward IP.
		iso := &CacheData{
			Policy: &Policy{
				ACLs: []ACLRule{
					{Action: "accept", Src: []string{"*"}, Dst: []string{"autogroup:internet"}},
				},
			},
			ProxyHosts: []ProxyHost{
				{ID: 9, DomainNames: []string{"any.example.com"}, ForwardHost: "192.168.1.50", Enabled: true},
			},
		}
		names := cardNames(MatchServices(&Identity{Login: "whoever@"}, iso))
		assertContains(t, names, "any.example.com")
	})

	t.Run("tagOwners fix: owning a tag does not grant tag-targeted service", func(t *testing.T) {
		// carol OWNS tag:server via tagOwners but no node she owns wears it, so the
		// tag-src rule must not grant her the tagged service.
		names := cardNames(MatchServices(&Identity{Login: "carol@"}, data))
		assertNotContains(t, names, "tagged.example.com")
		// The CIDR/alias rules are group:admin-only; carol is not in that group.
		assertNotContains(t, names, "cidr.example.com")
		assertNotContains(t, names, "alias.example.com")
		// But autogroup:internet (src "*") still reaches her.
		assertContains(t, names, "public.example.com")
	})
}

func cardNames(cards []ServiceCard) []string {
	names := make([]string, len(cards))
	for i, c := range cards {
		names[i] = c.Name
	}
	return names
}

func assertContains(t *testing.T, names []string, want string) {
	t.Helper()
	for _, n := range names {
		if n == want {
			return
		}
	}
	t.Errorf("expected %q in %v", want, names)
}

func assertNotContains(t *testing.T, names []string, unwanted string) {
	t.Helper()
	for _, n := range names {
		if n == unwanted {
			t.Errorf("did not expect %q in %v", unwanted, names)
			return
		}
	}
}
