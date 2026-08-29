package main

import (
	"reflect"
	"testing"
)

func TestNormalizeLogin(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"alice@example.com", "alice@example.com"},
		{"alice@", "alice@"},
		{"alice", "alice@"},
		{"bob@corp.net", "bob@corp.net"},
		{"", ""},
		{"   ", ""},
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
			"group:exact":        {"alice@example.com"},
			"group:other-domain": {"alice@other.example"},
			"group:short":        {"alice@"},
			"group:legacy":       {"alice"},
			"group:bob":          {"bob@"},
		},
		TagOwners: map[string][]string{
			"tag:server": {"group:exact"},
		},
	}

	t.Run("full login matches only its exact identity", func(t *testing.T) {
		set := buildIdentitySet(&Identity{Login: "alice@example.com"}, policy)

		for _, want := range []string{"alice@example.com", "group:exact"} {
			if !set[want] {
				t.Errorf("expected %q in identity set", want)
			}
		}
		for _, unwanted := range []string{
			"alice@", "alice", "group:short", "group:legacy",
			"alice@other.example", "group:other-domain", "tag:server",
		} {
			if set[unwanted] {
				t.Errorf("did not expect %q in identity set", unwanted)
			}
		}
	})

	t.Run("short login matches short and legacy policy members only", func(t *testing.T) {
		set := buildIdentitySet(&Identity{Login: "alice@"}, policy)

		for _, want := range []string{"alice@", "alice", "group:short", "group:legacy"} {
			if !set[want] {
				t.Errorf("expected %q in identity set", want)
			}
		}
		if set["group:exact"] || set["group:other-domain"] {
			t.Error("a short login must not satisfy a fully qualified policy member")
		}
	})

	t.Run("legacy bare login matches explicit short policy member", func(t *testing.T) {
		set := buildIdentitySet(&Identity{Login: "alice"}, policy)

		if !set["alice@"] || !set["alice"] || !set["group:short"] || !set["group:legacy"] {
			t.Errorf("legacy bare login did not get expected short identities: %v", set)
		}
		if set["group:exact"] {
			t.Error("a legacy bare login must not satisfy a fully qualified policy member")
		}
	})

	t.Run("blank login yields no identities", func(t *testing.T) {
		if set := buildIdentitySet(&Identity{Login: "   "}, policy); len(set) != 0 {
			t.Errorf("blank login should have no identities, got %v", set)
		}
	})
}

func TestSrcGranted(t *testing.T) {
	ids := map[string]bool{
		"alice@example.com": true,
		"alice@":            true,
		"alice":             true,
		"group:admin":       true,
	}

	tests := []struct {
		src  []string
		want bool
	}{
		{[]string{"*"}, true},
		{[]string{"alice@example.com"}, true},
		{[]string{"alice@"}, true},
		{[]string{"alice"}, true},
		{[]string{"alice@other.example"}, false},
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
		{"autogroup:internet fails closed", []string{"autogroup:internet"}, "10.0.0.99", false},
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

	t.Run("fully qualified source identities do not collide across domains", func(t *testing.T) {
		iso := &CacheData{
			Policy: &Policy{ACLs: []ACLRule{
				{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"10.0.0.10:*"}},
				{Action: "accept", Src: []string{"alice@other.example"}, Dst: []string{"10.0.0.11:*"}},
			}},
			ProxyHosts: []ProxyHost{
				{ID: 10, DomainNames: []string{"example-service.test"}, ForwardHost: "10.0.0.10", Enabled: true},
				{ID: 11, DomainNames: []string{"other-service.test"}, ForwardHost: "10.0.0.11", Enabled: true},
			},
		}

		names := cardNames(MatchServices(&Identity{Login: "alice@example.com"}, iso))
		assertContains(t, names, "example-service.test")
		assertNotContains(t, names, "other-service.test")
	})

	t.Run("full login does not inherit an ambiguous short policy identity", func(t *testing.T) {
		iso := &CacheData{
			Policy: &Policy{
				Groups: map[string][]string{"group:short": {"alice@"}},
				ACLs:   []ACLRule{{Action: "accept", Src: []string{"group:short"}, Dst: []string{"10.0.0.12:*"}}},
			},
			ProxyHosts: []ProxyHost{{ID: 12, DomainNames: []string{"short-policy.test"}, ForwardHost: "10.0.0.12", Enabled: true}},
		}

		for _, login := range []string{"alice@example.com", "alice@other.example"} {
			if cards := MatchServices(&Identity{Login: login}, iso); len(cards) != 0 {
				t.Errorf("%s inherited ambiguous short identity: %v", login, cardNames(cards))
			}
		}
		assertContains(t, cardNames(MatchServices(&Identity{Login: "alice@"}, iso)), "short-policy.test")
	})

	t.Run("blank login receives no services including wildcard grants", func(t *testing.T) {
		if cards := MatchServices(&Identity{Login: "   "}, data); len(cards) != 0 {
			t.Errorf("blank login should receive no services, got %v", cardNames(cards))
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

	t.Run("NPM route state does not become card health", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "alice@"}, data)
		for _, card := range cards {
			if card.LinkState != serviceLinkReady {
				t.Errorf("card %q link state = %q", card.Name, card.LinkState)
			}
		}
	})
}

func TestMatchServicesUsesTruthfulCardTargets(t *testing.T) {
	base := &CacheData{
		Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.5:*"}}}},
		ProxyHosts: []ProxyHost{{
			ID:            42,
			DomainNames:   []string{"*.rader.wiki"},
			ForwardScheme: "https",
			ForwardHost:   "10.0.0.5",
			Enabled:       true,
		}},
		ServiceMetadata: emptyServiceMetadata(),
	}

	t.Run("wildcard-only remains visible and unlinked", func(t *testing.T) {
		cards := MatchServices(&Identity{Login: "alice@example.com"}, base)
		if len(cards) != 1 {
			t.Fatalf("cards = %#v", cards)
		}
		if cards[0].Name != "*.rader.wiki" || cards[0].URL != "" || cards[0].LinkState != serviceLinkNeedsMetadata {
			t.Fatalf("card = %#v", cards[0])
		}
	})

	t.Run("first concrete domain wins over wildcard", func(t *testing.T) {
		data := *base
		data.ProxyHosts = append([]ProxyHost(nil), base.ProxyHosts...)
		data.ProxyHosts[0].DomainNames = []string{"*.rader.wiki", "home.rader.wiki"}
		cards := MatchServices(&Identity{Login: "alice@example.com"}, &data)
		if len(cards) != 1 || cards[0].Name != "home.rader.wiki" || cards[0].URL != "https://home.rader.wiki" || cards[0].LinkState != serviceLinkReady {
			t.Fatalf("cards = %#v", cards)
		}
	})

	t.Run("explicit metadata resolves wildcard without changing evidence", func(t *testing.T) {
		data := *base
		data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{
			42: {Name: "Rader Wiki", URL: "https://wiki.rader.wiki/"},
		}}
		matches := evaluateServices(&Identity{Login: "alice@example.com"}, &data)
		if len(matches) != 1 {
			t.Fatalf("matches = %#v", matches)
		}
		if matches[0].Card.Name != "Rader Wiki" || matches[0].Card.URL != "https://wiki.rader.wiki/" || matches[0].Card.LinkState != serviceLinkReady {
			t.Fatalf("card = %#v", matches[0].Card)
		}
		if matches[0].Destination.ResolvedValue != "10.0.0.5" || matches[0].ProxyHost.ForwardHost != "10.0.0.5" {
			t.Fatalf("override changed match evidence: %#v", matches[0])
		}
	})

	t.Run("metadata cannot create a card", func(t *testing.T) {
		data := *base
		data.ProxyHosts = nil
		data.ServiceMetadata = &ServiceMetadata{Overrides: map[int]ServiceOverride{42: {URL: "https://wiki.rader.wiki/"}}}
		if cards := MatchServices(&Identity{Login: "alice@example.com"}, &data); len(cards) != 0 {
			t.Fatalf("cards = %#v", cards)
		}
	})
}

func TestMatchServicesTagsAndCIDR(t *testing.T) {
	policy := &Policy{
		Groups: map[string][]string{
			"group:admin": {"alice@example.com"},
		},
		TagOwners: map[string][]string{
			// carol may assign tag:current, but that must never make her that tag.
			"tag:current": {"carol@"},
		},
		Hosts: map[string]string{
			"webserver": "10.2.0.5",
		},
		ACLs: []ACLRule{
			// Human identities must not inherit a tag from a node they own.
			{Action: "accept", Src: []string{"tag:current"}, Dst: []string{"10.3.0.9:*"}},
			// Tags from the current and legacy node fields still resolve as destinations.
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"tag:current:*"}},
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"tag:forced:*"}},
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"tag:valid:*"}},
			// Supported non-tag selectors retain their existing behavior.
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"10.1.0.0/24:443"}},
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"webserver:*"}},
			{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"autogroup:self:*"}},
			{Action: "accept", Src: []string{"*"}, Dst: []string{"203.0.113.4:*"}},
		},
	}

	nodes := []Node{
		// The current API tag field is on a node owned by alice; it is destination data,
		// not an additional human source identity.
		{ID: "1", Name: "alice-tagged", OwnerLogin: "alice@", Tags: []string{"tag:current"}, Addresses: []string{"10.0.0.1"}},
		// Legacy Headscale tag fields are unified during DTO conversion.
		{ID: "2", Name: "forced-tagged", OwnerLogin: "service@", Tags: []string{"tag:forced"}, Addresses: []string{"10.0.0.2"}},
		{ID: "3", Name: "valid-tagged", OwnerLogin: "service@", Tags: []string{"tag:valid"}, Addresses: []string{"10.0.0.3"}},
		// autogroup:self uses exact ownership for a fully qualified human identity.
		{ID: "4", Name: "alice-self", OwnerLogin: "alice@example.com", Addresses: []string{"100.64.0.9"}},
		// Neither another full domain nor an ambiguous short owner becomes alice's self IP.
		{ID: "5", Name: "other-alice", OwnerLogin: "alice@other.example", Addresses: []string{"100.64.0.10"}},
		{ID: "6", Name: "short-alice", OwnerLogin: "alice@", Addresses: []string{"100.64.0.11"}},
	}

	data := &CacheData{
		Policy: policy,
		Nodes:  nodes,
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"current-tag.example.com"}, ForwardHost: "10.0.0.1", Enabled: true},
			{ID: 2, DomainNames: []string{"forced-tag.example.com"}, ForwardHost: "10.0.0.2", Enabled: true},
			{ID: 3, DomainNames: []string{"valid-tag.example.com"}, ForwardHost: "10.0.0.3", Enabled: true},
			{ID: 4, DomainNames: []string{"source-tag.example.com"}, ForwardHost: "10.3.0.9", Enabled: true},
			{ID: 5, DomainNames: []string{"cidr.example.com"}, ForwardHost: "10.1.0.200", Enabled: true},
			{ID: 6, DomainNames: []string{"alias.example.com"}, ForwardHost: "10.2.0.5", Enabled: true},
			{ID: 7, DomainNames: []string{"self.example.com"}, ForwardHost: "100.64.0.9", Enabled: true},
			{ID: 8, DomainNames: []string{"other-self.example.com"}, ForwardHost: "100.64.0.10", Enabled: true},
			{ID: 9, DomainNames: []string{"short-self.example.com"}, ForwardHost: "100.64.0.11", Enabled: true},
			{ID: 10, DomainNames: []string{"public.example.com"}, ForwardHost: "203.0.113.4", Enabled: true},
		},
	}

	t.Run("node tags resolve destinations from current and legacy fields", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "alice@example.com"}, data))
		assertContains(t, names, "current-tag.example.com")
		assertContains(t, names, "forced-tag.example.com")
		assertContains(t, names, "valid-tag.example.com")
	})

	t.Run("human identity does not inherit a source tag from an owned node", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "alice@example.com"}, data))
		assertNotContains(t, names, "source-tag.example.com")
	})

	t.Run("cidr host alias and self destinations resolve", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "alice@example.com"}, data))
		assertContains(t, names, "cidr.example.com")
		assertContains(t, names, "alias.example.com")
		assertContains(t, names, "self.example.com")
		assertNotContains(t, names, "other-self.example.com")
		assertNotContains(t, names, "short-self.example.com")
	})

	t.Run("public service reaches an identified user", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "nobody@example.com"}, data))
		assertContains(t, names, "public.example.com")
	})

	t.Run("autogroup:internet destination fails closed", func(t *testing.T) {
		iso := &CacheData{
			Policy: &Policy{
				ACLs: []ACLRule{
					{Action: "accept", Src: []string{"*"}, Dst: []string{"autogroup:internet"}},
				},
			},
			ProxyHosts: []ProxyHost{
				{ID: 10, DomainNames: []string{"internet.example.com"}, ForwardHost: "192.168.1.50", Enabled: true},
			},
		}
		names := cardNames(MatchServices(&Identity{Login: "whoever@example.com"}, iso))
		assertNotContains(t, names, "internet.example.com")
	})

	t.Run("tag owner does not gain source or destination access", func(t *testing.T) {
		names := cardNames(MatchServices(&Identity{Login: "carol@example.com"}, data))
		assertNotContains(t, names, "current-tag.example.com")
		assertNotContains(t, names, "source-tag.example.com")
		assertNotContains(t, names, "cidr.example.com")
		assertNotContains(t, names, "alias.example.com")
		assertContains(t, names, "public.example.com")
	})
}

func TestDestinationMatchEvidenceKinds(t *testing.T) {
	mc := &matchContext{
		hosts: map[string]string{
			"webserver":          "10.0.0.5",
			"lan":                "10.0.0.0/24",
			"*":                  "10.0.0.200",
			"10.0.0.1":           "10.0.0.200",
			"10.0.0.0/24":        "10.0.0.200",
			"autogroup:self":     "10.0.0.200",
			"autogroup:internet": "10.0.0.200",
			"tag:server":         "10.0.0.200",
			"svc:web":            "10.0.0.200",
		},
		tagIPs: map[string][]string{
			"tag:server": {"10.0.0.1"},
		},
		selfIPs: []string{"100.64.0.9"},
	}
	tests := []struct {
		name     string
		selector string
		host     string
		kind     destinationMatchKind
		resolved string
	}{
		{name: "exact", selector: "10.0.0.1:443", host: "10.0.0.1", kind: destinationMatchExact, resolved: "10.0.0.1"},
		{name: "wildcard", selector: "*", host: "10.0.0.9", kind: destinationMatchWildcard, resolved: "10.0.0.9"},
		{name: "cidr", selector: "10.0.0.0/24:*", host: "10.0.0.8", kind: destinationMatchCIDR, resolved: "10.0.0.0/24"},
		{name: "host alias", selector: "webserver:443", host: "10.0.0.5", kind: destinationMatchHostAlias, resolved: "10.0.0.5"},
		{name: "host alias cidr", selector: "lan:*", host: "10.0.0.20", kind: destinationMatchHostAlias, resolved: "10.0.0.0/24"},
		{name: "tag", selector: "tag:server:*", host: "10.0.0.1", kind: destinationMatchTag, resolved: "10.0.0.1"},
		{name: "self", selector: "autogroup:self:*", host: "100.64.0.9", kind: destinationMatchSelf, resolved: "100.64.0.9"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence, matched := matchDestination(test.selector, test.host, mc)
			if !matched {
				t.Fatal("matchDestination() matched = false")
			}
			if evidence.Kind != test.kind || evidence.ResolvedValue != test.resolved {
				t.Fatalf("evidence = %#v, want kind=%q resolved=%q", evidence, test.kind, test.resolved)
			}
			if evidence.Selector != test.selector || evidence.NormalizedSelector != stripPort(test.selector) {
				t.Fatalf("selector evidence = %#v", evidence)
			}
		})
	}

	for _, selector := range []string{"autogroup:self", "autogroup:internet", "tag:server", "svc:web"} {
		if evidence, matched := matchDestination(selector, "10.0.0.200", mc); matched {
			t.Fatalf("reserved selector %q was shadowed by a host alias: %#v", selector, evidence)
		}
	}
}

func TestStructuralDestinationMatchesPreservesValidationSemantics(t *testing.T) {
	data := &CacheData{
		Policy: &Policy{
			Hosts: map[string]string{"app": "10.0.0.5"},
			ACLs: []ACLRule{{
				Action: "accept",
				Src:    []string{"group:admin"},
				Dst:    []string{"app:443", "tag:app:*", "autogroup:self:*"},
			}},
		},
		Nodes: []Node{{Tags: []string{"tag:app"}, Addresses: []string{"10.0.0.5"}}},
	}
	proxyHost := ProxyHost{ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true}

	matches, identityDependent := structuralDestinationMatches(proxyHost, data)
	if !identityDependent {
		t.Fatal("structuralDestinationMatches() did not record autogroup:self as identity-dependent")
	}
	if len(matches) != 2 {
		t.Fatalf("structuralDestinationMatches() = %#v, want two identity-independent paths", matches)
	}
	if matches[0].Kind != destinationMatchHostAlias || matches[1].Kind != destinationMatchTag {
		t.Fatalf("structural destination kinds = %#v", matches)
	}
}

func TestEnabledProxyHostHasSupportedDestinationMatch(t *testing.T) {
	tcp443, err := parseGrantIPCapability("tcp:443")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		proxyHost ProxyHost
		data      *CacheData
		want      bool
	}{
		{
			name:      "enabled exact ACL destination",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true},
			data: &CacheData{Policy: &Policy{ACLs: []ACLRule{{
				Action: "accept",
				Src:    []string{"group:unresolved"},
				Dst:    []string{"10.0.0.5:443"},
			}}}},
			want: true,
		},
		{
			name:      "enabled tag destination resolved by current nodes",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.6", ForwardPort: 443, Enabled: true},
			data: &CacheData{
				Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"tag:app:*"}}}},
				Nodes:  []Node{{Tags: []string{"tag:app"}, Addresses: []string{"10.0.0.6"}}},
			},
			want: true,
		},
		{
			name:      "enabled Grant destination with matching TCP port",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.7", ForwardPort: 443, Enabled: true},
			data: &CacheData{Policy: &Policy{Grants: []GrantRule{{
				Src:            []string{"group:unresolved"},
				Dst:            []string{"10.0.0.7"},
				IPCapabilities: []grantIPCapability{tcp443},
			}}}},
			want: true,
		},
		{
			name:      "Grant destination with different TCP port",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.7", ForwardPort: 8443, Enabled: true},
			data: &CacheData{Policy: &Policy{Grants: []GrantRule{{
				Src:            []string{"*"},
				Dst:            []string{"10.0.0.7"},
				IPCapabilities: []grantIPCapability{tcp443},
			}}}},
			want: false,
		},
		{
			name:      "autogroup self remains identity-dependent",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.8", ForwardPort: 443, Enabled: true},
			data: &CacheData{
				Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"autogroup:self:*"}}}},
				Nodes:  []Node{{OwnerLogin: "alice@example.com", Addresses: []string{"10.0.0.8"}}},
			},
			want: false,
		},
		{
			name:      "disabled matching proxy host",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: false},
			data: &CacheData{Policy: &Policy{ACLs: []ACLRule{{
				Action: "accept",
				Src:    []string{"*"},
				Dst:    []string{"10.0.0.5:443"},
			}}}},
			want: false,
		},
		{
			name:      "incomplete snapshot",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true},
			data:      &CacheData{},
			want:      false,
		},
		{
			name:      "nil snapshot",
			proxyHost: ProxyHost{ForwardHost: "10.0.0.5", ForwardPort: 443, Enabled: true},
			want:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := enabledProxyHostHasSupportedDestinationMatch(test.proxyHost, test.data); got != test.want {
				t.Fatalf("enabledProxyHostHasSupportedDestinationMatch() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestEvaluateServicesExplainsMatchServices(t *testing.T) {
	data := &CacheData{
		Policy: &Policy{
			Groups: map[string][]string{"group:admin": {"alice@example.com"}},
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"group:admin"}, Dst: []string{"tag:app:*"}},
				{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.0/24:*"}},
			},
		},
		Nodes: []Node{{OwnerLogin: "service@example.com", Tags: []string{"tag:app"}, Addresses: []string{"10.0.0.10"}}},
		ProxyHosts: []ProxyHost{
			{ID: 2, DomainNames: []string{"wiki.example.com"}, ForwardHost: "10.0.0.20", Enabled: true},
			{ID: 1, DomainNames: []string{"app.example.com"}, ForwardHost: "10.0.0.10", Enabled: true},
		},
	}
	identity := &Identity{Login: "alice@example.com"}
	matches := evaluateServices(identity, data)
	cards := MatchServices(identity, data)
	projected := make([]ServiceCard, len(matches))
	for index, match := range matches {
		projected[index] = match.Card
	}
	if !reflect.DeepEqual(projected, cards) {
		t.Fatalf("evaluateServices cards = %#v, MatchServices = %#v", projected, cards)
	}
	if len(matches) != 2 {
		t.Fatalf("evaluateServices() returned %d matches", len(matches))
	}
	if matches[0].SourceToken != "group:admin" || matches[0].Destination.Kind != destinationMatchTag || matches[0].RuleKind != accessRuleACL || matches[0].RuleIndex != 0 {
		t.Fatalf("first evidence = %#v", matches[0])
	}
	if matches[1].SourceToken != "*" || matches[1].Destination.Kind != destinationMatchCIDR || matches[1].RuleKind != accessRuleACL || matches[1].RuleIndex != 1 {
		t.Fatalf("second evidence = %#v", matches[1])
	}
}

func TestEvaluateServicesMatchesGrantTCPPortsAndSkipsUnauthoritativeSources(t *testing.T) {
	tcp443, err := parseGrantIPCapability("tcp:443")
	if err != nil {
		t.Fatal(err)
	}
	udp53, err := parseGrantIPCapability("udp:53")
	if err != nil {
		t.Fatal(err)
	}
	wildcard, err := parseGrantIPCapability("*")
	if err != nil {
		t.Fatal(err)
	}
	data := &CacheData{
		Policy: &Policy{
			Groups: map[string][]string{"group:admin": {"alice@example.com"}},
			Grants: []GrantRule{
				{Src: []string{"group:admin"}, BrowserSrc: []string{"group:admin"}, Dst: []string{"tag:app"}, IPCapabilities: []grantIPCapability{tcp443}},
				{Src: []string{"group:admin"}, BrowserSrc: []string{"group:admin"}, Dst: []string{"10.0.0.20"}, IPCapabilities: []grantIPCapability{udp53}},
				{Src: []string{"tag:client"}, Dst: []string{"10.0.0.30"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:member"}, BrowserSrc: []string{"autogroup:member"}, Dst: []string{"10.0.0.40"}, IPCapabilities: []grantIPCapability{wildcard}},
			},
		},
		Nodes: []Node{{Tags: []string{"tag:app"}, Addresses: []string{"10.0.0.10"}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"app.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.10", ForwardPort: 443, Enabled: true},
			{ID: 2, DomainNames: []string{"dns.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.20", ForwardPort: 53, Enabled: true},
			{ID: 3, DomainNames: []string{"machine.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.30", ForwardPort: 443, Enabled: true},
			{ID: 4, DomainNames: []string{"role.example.com"}, ForwardScheme: "https", ForwardHost: "10.0.0.40", ForwardPort: 443, Enabled: true},
		},
	}

	matches := evaluateServices(&Identity{Login: "alice@example.com"}, data)
	if len(matches) != 1 {
		t.Fatalf("evaluateServices() = %#v", matches)
	}
	if matches[0].Card.Domain != "app.example.com" || matches[0].RuleKind != accessRuleGrant || matches[0].RuleIndex != 0 {
		t.Fatalf("grant evidence = %#v", matches[0])
	}
}

func TestEvaluateServicesMatchesAuthoritativeGrantRolesOnly(t *testing.T) {
	wildcard, err := parseGrantIPCapability("*")
	if err != nil {
		t.Fatal(err)
	}
	data := &CacheData{
		Policy: &Policy{
			ACLs: []ACLRule{
				{Action: "accept", Src: []string{"autogroup:admin"}, Dst: []string{"10.0.1.90:443"}},
			},
			Grants: []GrantRule{
				{Src: []string{"autogroup:member"}, BrowserSrc: []string{"autogroup:member"}, Dst: []string{"10.0.1.10"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:admin"}, BrowserSrc: []string{"autogroup:admin"}, Dst: []string{"10.0.1.20"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:owner"}, BrowserSrc: []string{"autogroup:owner"}, Dst: []string{"10.0.1.30"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:it-admin"}, BrowserSrc: []string{"autogroup:it-admin"}, Dst: []string{"10.0.1.40"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:network-admin"}, BrowserSrc: []string{"autogroup:network-admin"}, Dst: []string{"10.0.1.50"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:billing-admin"}, BrowserSrc: []string{"autogroup:billing-admin"}, Dst: []string{"10.0.1.60"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:auditor"}, BrowserSrc: []string{"autogroup:auditor"}, Dst: []string{"10.0.1.70"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"autogroup:admin"}, BrowserSrc: []string{"autogroup:admin"}, Dst: []string{"autogroup:self"}, IPCapabilities: []grantIPCapability{wildcard}},
				{Src: []string{"tag:client"}, Dst: []string{"10.0.1.80"}, IPCapabilities: []grantIPCapability{wildcard}},
			},
		},
		Nodes: []Node{{OwnerLogin: "admin@example.com", Addresses: []string{"10.0.2.10"}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"member.example.com"}, ForwardHost: "10.0.1.10", ForwardPort: 443, Enabled: true},
			{ID: 2, DomainNames: []string{"admin.example.com"}, ForwardHost: "10.0.1.20", ForwardPort: 443, Enabled: true},
			{ID: 3, DomainNames: []string{"owner.example.com"}, ForwardHost: "10.0.1.30", ForwardPort: 443, Enabled: true},
			{ID: 4, DomainNames: []string{"it.example.com"}, ForwardHost: "10.0.1.40", ForwardPort: 443, Enabled: true},
			{ID: 5, DomainNames: []string{"network.example.com"}, ForwardHost: "10.0.1.50", ForwardPort: 443, Enabled: true},
			{ID: 6, DomainNames: []string{"billing.example.com"}, ForwardHost: "10.0.1.60", ForwardPort: 443, Enabled: true},
			{ID: 7, DomainNames: []string{"auditor.example.com"}, ForwardHost: "10.0.1.70", ForwardPort: 443, Enabled: true},
			{ID: 8, DomainNames: []string{"machine.example.com"}, ForwardHost: "10.0.1.80", ForwardPort: 443, Enabled: true},
			{ID: 9, DomainNames: []string{"acl-role.example.com"}, ForwardHost: "10.0.1.90", ForwardPort: 443, Enabled: true},
			{ID: 10, DomainNames: []string{"self.example.com"}, ForwardHost: "10.0.2.10", ForwardPort: 443, Enabled: true},
		},
		GrantRoleSelectorsByLogin: map[string][]string{
			"owner@example.com":   {"autogroup:admin", "autogroup:member", "autogroup:owner"},
			"admin@example.com":   {"autogroup:admin", "autogroup:member"},
			"member@example.com":  {"autogroup:member"},
			"it@example.com":      {"autogroup:it-admin", "autogroup:member"},
			"network@example.com": {"autogroup:member", "autogroup:network-admin"},
			"billing@example.com": {"autogroup:billing-admin", "autogroup:member"},
			"auditor@example.com": {"autogroup:auditor", "autogroup:member"},
		},
	}

	tests := map[string]struct {
		login string
		want  []string
	}{
		"owner":         {login: "owner@example.com", want: []string{"admin.example.com", "member.example.com", "owner.example.com"}},
		"admin":         {login: "admin@example.com", want: []string{"admin.example.com", "member.example.com", "self.example.com"}},
		"member":        {login: "member@example.com", want: []string{"member.example.com"}},
		"it admin":      {login: "it@example.com", want: []string{"it.example.com", "member.example.com"}},
		"network admin": {login: "network@example.com", want: []string{"member.example.com", "network.example.com"}},
		"billing admin": {login: "billing@example.com", want: []string{"billing.example.com", "member.example.com"}},
		"auditor":       {login: "auditor@example.com", want: []string{"auditor.example.com", "member.example.com"}},
		"shared":        {login: "shared@example.com", want: []string{}},
		"case variant":  {login: "Admin@example.com", want: []string{}},
		"short alias":   {login: "admin@", want: []string{}},
		"bare alias":    {login: "admin", want: []string{}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := cardNames(MatchServices(&Identity{Login: test.login}, data)); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("cards = %v, want %v", got, test.want)
			}
		})
	}

	matches := evaluateServices(&Identity{Login: "admin@example.com"}, data)
	for _, match := range matches {
		if match.Card.Domain == "admin.example.com" {
			if match.RuleKind != accessRuleGrant || match.RuleIndex != 1 || match.SourceToken != "autogroup:admin" {
				t.Fatalf("admin role evidence = %#v", match)
			}
			return
		}
	}
	t.Fatal("admin role match not found")
}

func TestEvaluateServicesCombinesACLsAndGrants(t *testing.T) {
	wildcard, err := parseGrantIPCapability("*")
	if err != nil {
		t.Fatal(err)
	}
	data := &CacheData{
		Policy: &Policy{
			ACLs:   []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"10.0.0.10:443"}}},
			Grants: []GrantRule{{Src: []string{"alice@example.com"}, BrowserSrc: []string{"alice@example.com"}, Dst: []string{"10.0.0.20"}, IPCapabilities: []grantIPCapability{wildcard}}},
		},
		ProxyHosts: []ProxyHost{
			{ID: 1, DomainNames: []string{"legacy.example.com"}, ForwardHost: "10.0.0.10", ForwardPort: 443, Enabled: true},
			{ID: 2, DomainNames: []string{"grant.example.com"}, ForwardHost: "10.0.0.20", ForwardPort: 8443, Enabled: true},
		},
	}
	matches := evaluateServices(&Identity{Login: "alice@example.com"}, data)
	if len(matches) != 2 || matches[0].RuleKind != accessRuleGrant || matches[1].RuleKind != accessRuleACL {
		t.Fatalf("mixed matches = %#v", matches)
	}
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
