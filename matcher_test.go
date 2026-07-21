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

	t.Run("member of group and tag via group", func(t *testing.T) {
		id := &Identity{Login: "alice@example.com"}
		set := buildIdentitySet(id, policy)

		for _, want := range []string{"alice@", "group:admin", "tag:server"} {
			if !set[want] {
				t.Errorf("expected %q in identity set", want)
			}
		}
		if set["group:dev"] {
			t.Error("alice should not be in group:dev")
		}
	})

	t.Run("member of group via short login", func(t *testing.T) {
		id := &Identity{Login: "bob@"}
		set := buildIdentitySet(id, policy)

		if !set["group:admin"] {
			t.Error("bob@ should be in group:admin")
		}
		if !set["tag:server"] {
			t.Error("bob@ should have tag:server via group:admin")
		}
	})

	t.Run("tag owner by direct login", func(t *testing.T) {
		id := &Identity{Login: "charlie@example.com"}
		set := buildIdentitySet(id, policy)

		if !set["group:dev"] {
			t.Error("charlie should be in group:dev")
		}
		if !set["tag:ci"] {
			t.Error("charlie should own tag:ci directly")
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
		if got := dstMatches(tt.dst, tt.host); got != tt.want {
			t.Errorf("dstMatches(%v, %q) = %v, want %v", tt.dst, tt.host, got, tt.want)
		}
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
