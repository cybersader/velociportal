package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizeHostnameSuggestion(t *testing.T) {
	valid := map[string]string{
		" Grafana.Internal.Example.COM. ": "grafana.internal.example.com",
		"a-b.example.com":                 "a-b.example.com",
		"1.example.com":                   "1.example.com",
	}
	for input, want := range valid {
		if got, ok := normalizeHostnameSuggestion(input); !ok || got != want {
			t.Errorf("normalizeHostnameSuggestion(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}

	invalid := []string{
		"", "host", "host..example.com", ".host.example.com", "host.example.com..",
		"*.example.com", "https://host.example.com", "host.example.com:443",
		"user@host.example.com", "host.example.com/path", "host.example.com?q=1",
		"host.example.com#fragment", "host_name.example.com", "hé.example.com",
		"127.0.0.1", "2001:db8::1", "-host.example.com", "host-.example.com",
		strings.Repeat("a", 64) + ".example.com", strings.Repeat("a", 244) + ".example.com",
		"host.example.com\nvalue",
	}
	for _, input := range invalid {
		if got, ok := normalizeHostnameSuggestion(input); ok || got != "" {
			t.Errorf("normalizeHostnameSuggestion(%q) = %q, %v; want empty, false", input, got, ok)
		}
	}
}

func TestMergeHostnameSuggestionCandidates(t *testing.T) {
	candidates, err := mergeHostnameSuggestionCandidates(
		[]string{"B.example.com", "short", "both.example.com", "bad_value.example.com"},
		[]string{"a.example.com", "B.EXAMPLE.COM.", "both.example.com"},
	)
	if err != nil {
		t.Fatalf("mergeHostnameSuggestionCandidates() error = %v", err)
	}
	want := []hostnameSuggestionCandidate{
		{Hostname: "a.example.com", Source: hostnameSuggestionSourceStdin},
		{Hostname: "b.example.com", Source: hostnameSuggestionSourceCombined},
		{Hostname: "both.example.com", Source: hostnameSuggestionSourceCombined},
	}
	if !reflect.DeepEqual(candidates, want) {
		t.Fatalf("candidates = %#v, want %#v", candidates, want)
	}

	_, err = mergeHostnameSuggestionCandidates(nil, []string{"secret_invalid_value"})
	if err == nil || !strings.Contains(err.Error(), "record 1") {
		t.Fatalf("invalid stdin error = %v", err)
	}
	if strings.Contains(err.Error(), "secret_invalid_value") {
		t.Fatalf("invalid stdin error leaked value: %v", err)
	}
}

func TestExtractHostnameSuggestionWildcardSuffix(t *testing.T) {
	valid := map[string]string{
		"*.example.com":    "example.com",
		" *.EXAMPLE.COM. ": "example.com",
	}
	for input, want := range valid {
		if got, ok := extractHostnameSuggestionWildcardSuffix(input); !ok || got != want {
			t.Errorf("extractHostnameSuggestionWildcardSuffix(%q) = %q, %v; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"example.com", "*example.com", "*.*.example.com", "*.localhost", "*.127.0.0.1", "*.bad_value.example.com"} {
		if got, ok := extractHostnameSuggestionWildcardSuffix(input); ok || got != "" {
			t.Errorf("extractHostnameSuggestionWildcardSuffix(%q) = %q, %v; want empty, false", input, got, ok)
		}
	}
}

func TestEligibleHostnameSuggestionHosts(t *testing.T) {
	metadata := &ServiceMetadata{Overrides: map[int]ServiceOverride{
		2: {Name: "Preserved name"},
		3: {URL: "https://configured.example.com"},
	}}
	snapshot := &CacheData{
		Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.0/24:*"}}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 80},
			{ID: 2, Enabled: true, DomainNames: []string{"*.internal.example.com", "*.INTERNAL.EXAMPLE.COM."}, ForwardHost: "10.0.0.2", ForwardPort: 80},
			{ID: 3, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.3", ForwardPort: 80},
			{ID: 4, Enabled: true, DomainNames: []string{"*.example.com", "real.example.com"}, ForwardHost: "10.0.0.4", ForwardPort: 80},
			{ID: 5, Enabled: false, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.5", ForwardPort: 80},
			{ID: 6, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "192.0.2.6", ForwardPort: 80},
			{ID: 7, Enabled: true, DomainNames: []string{"invalid"}, ForwardHost: "10.0.0.7", ForwardPort: 80},
			{ID: 0, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.8", ForwardPort: 80},
		},
	}

	got, err := eligibleHostnameSuggestionHosts(snapshot, metadata)
	if err != nil {
		t.Fatalf("eligibleHostnameSuggestionHosts() error = %v", err)
	}
	want := []eligibleHostnameSuggestionHost{
		{ProxyHostID: 1, WildcardSuffixes: []string{"example.com"}},
		{ProxyHostID: 2, WildcardSuffixes: []string{"internal.example.com"}, ExistingName: "Preserved name"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eligible = %#v, want %#v", got, want)
	}

	snapshot.ProxyHosts = append(snapshot.ProxyHosts, ProxyHost{ID: 2})
	_, err = eligibleHostnameSuggestionHosts(snapshot, metadata)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ID error = %v", err)
	}
}

func TestEligibleHostnameSuggestionHostsRejectsAggregateDomainBounds(t *testing.T) {
	snapshot := &CacheData{
		Policy:     &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"*"}, Dst: []string{"10.0.0.1:*"}}}},
		ProxyHosts: []ProxyHost{{ID: 1, Enabled: true, ForwardHost: "10.0.0.1", ForwardPort: 80}},
	}
	snapshot.ProxyHosts[0].DomainNames = make([]string, maxHostnameSuggestionDomains+1)
	for index := range snapshot.ProxyHosts[0].DomainNames {
		snapshot.ProxyHosts[0].DomainNames[index] = "invalid"
	}
	if _, err := eligibleHostnameSuggestionHosts(snapshot, nil); err == nil || !strings.Contains(err.Error(), "domains exceed") {
		t.Fatalf("domain bound error = %v", err)
	}

	snapshot.ProxyHosts[0].DomainNames = make([]string, maxHostnameSuggestionWildcardSuffixes+1)
	for index := range snapshot.ProxyHosts[0].DomainNames {
		snapshot.ProxyHosts[0].DomainNames[index] = "*.s" + strconv.Itoa(index) + ".example.com"
	}
	if _, err := eligibleHostnameSuggestionHosts(snapshot, nil); err == nil || !strings.Contains(err.Error(), "wildcard suffixes exceed") {
		t.Fatalf("wildcard bound error = %v", err)
	}
}

func TestHostnameMatchesSuggestionSuffix(t *testing.T) {
	for _, test := range []struct {
		hostname string
		suffix   string
		want     bool
	}{
		{hostname: "grafana.example.com", suffix: "example.com", want: true},
		{hostname: "deep.grafana.example.com", suffix: "example.com", want: true},
		{hostname: "example.com", suffix: "example.com", want: false},
		{hostname: "notexample.com", suffix: "example.com", want: false},
		{hostname: "grafana.other.com", suffix: "example.com", want: false},
	} {
		if got := hostnameMatchesSuggestionSuffix(test.hostname, test.suffix); got != test.want {
			t.Errorf("hostnameMatchesSuggestionSuffix(%q, %q) = %v, want %v", test.hostname, test.suffix, got, test.want)
		}
	}
}

func TestBuildHostnameSuggestionsExcludesAmbiguousComponents(t *testing.T) {
	candidates := []hostnameSuggestionCandidate{
		{Hostname: "a.one.example.com", Source: hostnameSuggestionSourceControlPlane},
		{Hostname: "b.two.example.com", Source: hostnameSuggestionSourceStdin},
		{Hostname: "c.three.example.com", Source: hostnameSuggestionSourceCombined},
		{Hostname: "d.isolated.test.com", Source: hostnameSuggestionSourceControlPlane},
		{Hostname: "unmatched.invalid.com", Source: hostnameSuggestionSourceStdin},
	}
	eligible := []eligibleHostnameSuggestionHost{
		{ProxyHostID: 10, WildcardSuffixes: []string{"one.example.com", "two.example.com"}},
		{ProxyHostID: 11, WildcardSuffixes: []string{"two.example.com", "three.example.com"}},
		{ProxyHostID: 12, WildcardSuffixes: []string{"isolated.test.com"}},
	}

	suggestions, ambiguities, err := buildHostnameSuggestions(candidates, eligible)
	if err != nil {
		t.Fatalf("buildHostnameSuggestions() error = %v", err)
	}
	wantSuggestions := []hostnameSuggestion{{ProxyHostID: 12, Hostname: "d.isolated.test.com", Source: hostnameSuggestionSourceControlPlane}}
	if !reflect.DeepEqual(suggestions, wantSuggestions) {
		t.Fatalf("suggestions = %#v, want %#v", suggestions, wantSuggestions)
	}
	if len(ambiguities) != 1 {
		t.Fatalf("ambiguities = %#v, want one", ambiguities)
	}
	if !reflect.DeepEqual(ambiguities[0].ProxyHostIDs, []int{10, 11}) {
		t.Fatalf("ambiguous IDs = %v", ambiguities[0].ProxyHostIDs)
	}
	gotNames := []string{}
	for _, candidate := range ambiguities[0].Candidates {
		gotNames = append(gotNames, candidate.Hostname)
	}
	if !reflect.DeepEqual(gotNames, []string{"a.one.example.com", "b.two.example.com", "c.three.example.com"}) {
		t.Fatalf("ambiguous candidates = %v", gotNames)
	}
}

func TestBuildHostnameSuggestionsIsInputOrderIndependent(t *testing.T) {
	forwardCandidates := []hostnameSuggestionCandidate{
		{Hostname: "a.example.com", Source: hostnameSuggestionSourceControlPlane},
		{Hostname: "b.example.com", Source: hostnameSuggestionSourceStdin},
	}
	reverseCandidates := []hostnameSuggestionCandidate{forwardCandidates[1], forwardCandidates[0]}
	forwardEligible := []eligibleHostnameSuggestionHost{
		{ProxyHostID: 2, WildcardSuffixes: []string{"isolated.example.net"}},
		{ProxyHostID: 1, WildcardSuffixes: []string{"example.com"}},
	}
	reverseEligible := []eligibleHostnameSuggestionHost{forwardEligible[1], forwardEligible[0]}

	forwardSuggestions, forwardAmbiguities, forwardErr := buildHostnameSuggestions(forwardCandidates, forwardEligible)
	reverseSuggestions, reverseAmbiguities, reverseErr := buildHostnameSuggestions(reverseCandidates, reverseEligible)
	if forwardErr != nil || reverseErr != nil {
		t.Fatalf("graph errors: forward=%v reverse=%v", forwardErr, reverseErr)
	}
	if !reflect.DeepEqual(forwardSuggestions, reverseSuggestions) || !reflect.DeepEqual(forwardAmbiguities, reverseAmbiguities) {
		t.Fatalf("order changed result: forward=%#v/%#v reverse=%#v/%#v", forwardSuggestions, forwardAmbiguities, reverseSuggestions, reverseAmbiguities)
	}
}

func TestBuildHostnameSuggestionsRejectsGraphEdgeBound(t *testing.T) {
	candidateCount := 129
	hostCount := 128
	candidates := make([]hostnameSuggestionCandidate, candidateCount)
	for index := range candidates {
		candidates[index] = hostnameSuggestionCandidate{
			Hostname: "h" + strconv.Itoa(index) + ".example.com",
			Source:   hostnameSuggestionSourceControlPlane,
		}
	}
	eligible := make([]eligibleHostnameSuggestionHost, hostCount)
	for index := range eligible {
		eligible[index] = eligibleHostnameSuggestionHost{
			ProxyHostID:      index + 1,
			WildcardSuffixes: []string{"example.com"},
		}
	}
	if _, _, err := buildHostnameSuggestions(candidates, eligible); err == nil || !strings.Contains(err.Error(), "graph exceeds") {
		t.Fatalf("graph edge bound error = %v", err)
	}
}

func TestBuildHostnameSuggestionProposal(t *testing.T) {
	suggestions := []hostnameSuggestion{
		{ProxyHostID: 2, Hostname: "z.example.com", Source: hostnameSuggestionSourceStdin},
		{ProxyHostID: 1, Hostname: "a.example.com", Source: hostnameSuggestionSourceControlPlane},
	}
	eligible := []eligibleHostnameSuggestionHost{
		{ProxyHostID: 1, ExistingName: "Existing display name"},
		{ProxyHostID: 2},
	}
	got, err := buildHostnameSuggestionProposal(suggestions, eligible, "https")
	if err != nil {
		t.Fatalf("buildHostnameSuggestionProposal() error = %v", err)
	}
	want := `{
  "version": 1,
  "services": [
    {
      "proxy_host_id": 1,
      "name": "Existing display name",
      "url": "https://a.example.com"
    },
    {
      "proxy_host_id": 2,
      "name": "z.example.com",
      "url": "https://z.example.com"
    }
  ]
}
`
	if string(got) != want {
		t.Fatalf("proposal =\n%s\nwant =\n%s", got, want)
	}
	metadata, err := parseServiceMetadata(got)
	if err != nil {
		t.Fatalf("proposal did not round-trip: %v", err)
	}
	if metadata.Overrides[1].Name != "Existing display name" || metadata.Overrides[2].URL != "https://z.example.com" {
		t.Fatalf("round-trip metadata = %#v", metadata.Overrides)
	}

	if _, err := buildHostnameSuggestionProposal(suggestions, eligible, "ftp"); err == nil {
		t.Fatal("invalid browser scheme was accepted")
	}
	if _, err := buildHostnameSuggestionProposal([]hostnameSuggestion{{ProxyHostID: 9, Hostname: "a.example.com"}}, eligible, "http"); err == nil {
		t.Fatal("ineligible proxy host was accepted")
	}
}

func TestHostnameSuggestionGenerationDoesNotChangeCardsOrMetadata(t *testing.T) {
	metadata := &ServiceMetadata{Overrides: map[int]ServiceOverride{}}
	snapshot := &CacheData{
		Policy: &Policy{ACLs: []ACLRule{{Action: "accept", Src: []string{"alice@example.com"}, Dst: []string{"10.0.0.1:*"}}}},
		ProxyHosts: []ProxyHost{
			{ID: 1, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 443},
			{ID: 2, Enabled: false, DomainNames: []string{"*.example.com"}, ForwardHost: "10.0.0.1", ForwardPort: 443},
			{ID: 3, Enabled: true, DomainNames: nil, ForwardHost: "10.0.0.1", ForwardPort: 443},
			{ID: 4, Enabled: true, DomainNames: []string{"*.example.com"}, ForwardHost: "192.0.2.4", ForwardPort: 443},
		},
		ServiceMetadata: metadata,
	}
	identity := &Identity{Login: "alice@example.com"}
	cardsBefore := MatchServices(identity, snapshot)
	metadataBefore := snapshot.ServiceMetadata

	candidates, err := mergeHostnameSuggestionCandidates([]string{"grafana.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eligible, err := eligibleHostnameSuggestionHosts(snapshot, snapshot.ServiceMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(eligible) != 1 || eligible[0].ProxyHostID != 1 {
		t.Fatalf("eligible = %#v", eligible)
	}
	suggestions, _, err := buildHostnameSuggestions(candidates, eligible)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildHostnameSuggestionProposal(suggestions, eligible, "https"); err != nil {
		t.Fatal(err)
	}

	cardsAfter := MatchServices(identity, snapshot)
	if !reflect.DeepEqual(cardsAfter, cardsBefore) {
		t.Fatalf("cards changed: before=%#v after=%#v", cardsBefore, cardsAfter)
	}
	if snapshot.ServiceMetadata != metadataBefore || len(snapshot.ServiceMetadata.Overrides) != 0 {
		t.Fatalf("active metadata changed: %#v", snapshot.ServiceMetadata)
	}
}

func TestSerializeServiceMetadataDocumentValidatesOutput(t *testing.T) {
	name := "name"
	proposalURL := "https://safe.example.com"
	data, err := serializeServiceMetadataDocumentV1([]serviceMetadataEntry{{ProxyHostID: 1, Name: &name, URL: &proposalURL}})
	if err != nil {
		t.Fatalf("serializeServiceMetadataDocumentV1() error = %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("serialized proposal lacks trailing newline")
	}

	invalidURL := "https://*.secret.example.com"
	_, err = serializeServiceMetadataDocumentV1([]serviceMetadataEntry{{ProxyHostID: 1, URL: &invalidURL}})
	if err == nil {
		t.Fatal("invalid proposal was accepted")
	}
	if strings.Contains(err.Error(), "secret.example.com") {
		t.Fatalf("proposal error leaked private value: %v", err)
	}

	category := "Infrastructure"
	order := 0
	_, err = serializeServiceMetadataDocumentV1([]serviceMetadataEntry{{ProxyHostID: 1, Category: &category, Order: &order}})
	if err == nil {
		t.Fatal("v1 serializer accepted v2 organization fields")
	}
}
