package main

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const (
	maxHostnameSuggestionControlPlaneNames = 4096
	maxHostnameSuggestionCandidates        = 1024
	maxHostnameSuggestionProxyHosts        = 1024
	maxHostnameSuggestionDomains           = 4096
	maxHostnameSuggestionWildcardSuffixes  = 2048
	maxHostnameSuggestionGraphEdges        = 16384
)

type hostnameSuggestionSource string

const (
	hostnameSuggestionSourceControlPlane hostnameSuggestionSource = "selected_control_plane"
	hostnameSuggestionSourceStdin        hostnameSuggestionSource = "stdin"
	hostnameSuggestionSourceCombined     hostnameSuggestionSource = "selected_control_plane+stdin"
)

type hostnameSuggestionCandidate struct {
	Hostname string
	Source   hostnameSuggestionSource
}

type eligibleHostnameSuggestionHost struct {
	ProxyHostID      int
	WildcardSuffixes []string
	ExistingName     string
}

type hostnameSuggestion struct {
	ProxyHostID int
	Hostname    string
	Source      hostnameSuggestionSource
}

type hostnameSuggestionAmbiguity struct {
	ProxyHostIDs []int
	Candidates   []hostnameSuggestionCandidate
}

func normalizeHostnameSuggestion(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	for _, character := range value {
		if character > 127 {
			return "", false
		}
	}
	value = strings.ToLower(value)
	value = strings.TrimSuffix(value, ".")
	if strings.HasSuffix(value, ".") || strings.Count(value, ".") < 1 {
		return "", false
	}
	if err := validateServiceHealthHost(value); err != nil {
		return "", false
	}
	return value, true
}

func mergeHostnameSuggestionCandidates(controlPlaneNames, stdinNames []string) ([]hostnameSuggestionCandidate, error) {
	if len(controlPlaneNames) > maxHostnameSuggestionControlPlaneNames {
		return nil, fmt.Errorf("selected control plane returned more than %d candidate name fields", maxHostnameSuggestionControlPlaneNames)
	}

	sources := make(map[string]hostnameSuggestionSource)
	add := func(hostname string, source hostnameSuggestionSource) error {
		if current, exists := sources[hostname]; exists {
			if current != source {
				sources[hostname] = hostnameSuggestionSourceCombined
			}
			return nil
		}
		if len(sources) >= maxHostnameSuggestionCandidates {
			return fmt.Errorf("hostname candidates exceed %d unique names", maxHostnameSuggestionCandidates)
		}
		sources[hostname] = source
		return nil
	}

	for _, raw := range controlPlaneNames {
		hostname, valid := normalizeHostnameSuggestion(raw)
		if !valid {
			continue
		}
		if err := add(hostname, hostnameSuggestionSourceControlPlane); err != nil {
			return nil, err
		}
	}
	for index, raw := range stdinNames {
		hostname, valid := normalizeHostnameSuggestion(raw)
		if !valid {
			return nil, fmt.Errorf("stdin hostname record %d is invalid", index+1)
		}
		if err := add(hostname, hostnameSuggestionSourceStdin); err != nil {
			return nil, err
		}
	}

	candidates := make([]hostnameSuggestionCandidate, 0, len(sources))
	for hostname, source := range sources {
		candidates = append(candidates, hostnameSuggestionCandidate{Hostname: hostname, Source: source})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Hostname < candidates[j].Hostname
	})
	return candidates, nil
}

func extractHostnameSuggestionWildcardSuffix(domain string) (string, bool) {
	domain = strings.TrimSpace(domain)
	if !strings.HasPrefix(domain, "*.") || strings.Count(domain, "*") != 1 {
		return "", false
	}
	return normalizeHostnameSuggestion(strings.TrimPrefix(domain, "*."))
}

func eligibleHostnameSuggestionHosts(snapshot *CacheData, metadata *ServiceMetadata) ([]eligibleHostnameSuggestionHost, error) {
	if snapshot == nil {
		return nil, errors.New("hostname suggestion snapshot is missing")
	}
	if len(snapshot.ProxyHosts) > maxHostnameSuggestionProxyHosts {
		return nil, fmt.Errorf("NPM returned more than %d proxy hosts", maxHostnameSuggestionProxyHosts)
	}

	seenIDs := make(map[int]struct{}, len(snapshot.ProxyHosts))
	eligible := make([]eligibleHostnameSuggestionHost, 0)
	totalDomains := 0
	totalWildcardSuffixes := 0
	for _, proxyHost := range snapshot.ProxyHosts {
		if proxyHost.ID > 0 {
			if _, exists := seenIDs[proxyHost.ID]; exists {
				return nil, errors.New("NPM returned duplicate positive proxy-host IDs")
			}
			seenIDs[proxyHost.ID] = struct{}{}
		}
		if proxyHost.ID <= 0 || !enabledProxyHostHasSupportedDestinationMatch(proxyHost, snapshot) {
			continue
		}

		wildcardSet := make(map[string]struct{})
		hasConcrete := false
		for _, domain := range proxyHost.DomainNames {
			totalDomains++
			if totalDomains > maxHostnameSuggestionDomains {
				return nil, fmt.Errorf("NPM proxy-host domains exceed %d entries", maxHostnameSuggestionDomains)
			}
			if suffix, valid := extractHostnameSuggestionWildcardSuffix(domain); valid {
				if _, exists := wildcardSet[suffix]; !exists {
					totalWildcardSuffixes++
					if totalWildcardSuffixes > maxHostnameSuggestionWildcardSuffixes {
						return nil, fmt.Errorf("NPM wildcard suffixes exceed %d entries", maxHostnameSuggestionWildcardSuffixes)
					}
					wildcardSet[suffix] = struct{}{}
				}
				continue
			}
			if validConcreteCardDomain(strings.TrimSpace(domain)) {
				hasConcrete = true
			}
		}
		if hasConcrete || len(wildcardSet) == 0 {
			continue
		}

		existingName := ""
		if metadata != nil {
			if override, exists := metadata.Overrides[proxyHost.ID]; exists {
				if override.URL != "" {
					continue
				}
				existingName = override.Name
			}
		}

		suffixes := make([]string, 0, len(wildcardSet))
		for suffix := range wildcardSet {
			suffixes = append(suffixes, suffix)
		}
		sort.Strings(suffixes)
		eligible = append(eligible, eligibleHostnameSuggestionHost{
			ProxyHostID:      proxyHost.ID,
			WildcardSuffixes: suffixes,
			ExistingName:     existingName,
		})
	}

	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].ProxyHostID < eligible[j].ProxyHostID
	})
	return eligible, nil
}

func hostnameMatchesSuggestionSuffix(hostname, suffix string) bool {
	return hostname != suffix && strings.HasSuffix(hostname, "."+suffix)
}

func buildHostnameSuggestions(candidates []hostnameSuggestionCandidate, eligible []eligibleHostnameSuggestionHost) ([]hostnameSuggestion, []hostnameSuggestionAmbiguity, error) {
	hostEdges := make(map[string]map[int]struct{})
	candidateByHostname := make(map[string]hostnameSuggestionCandidate, len(candidates))
	idEdges := make(map[int]map[string]struct{})
	graphEdges := 0

	for _, candidate := range candidates {
		candidateByHostname[candidate.Hostname] = candidate
		for _, host := range eligible {
			matched := false
			for _, suffix := range host.WildcardSuffixes {
				if hostnameMatchesSuggestionSuffix(candidate.Hostname, suffix) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
			graphEdges++
			if graphEdges > maxHostnameSuggestionGraphEdges {
				return nil, nil, fmt.Errorf("hostname suggestion graph exceeds %d edges", maxHostnameSuggestionGraphEdges)
			}
			if hostEdges[candidate.Hostname] == nil {
				hostEdges[candidate.Hostname] = make(map[int]struct{})
			}
			if idEdges[host.ProxyHostID] == nil {
				idEdges[host.ProxyHostID] = make(map[string]struct{})
			}
			hostEdges[candidate.Hostname][host.ProxyHostID] = struct{}{}
			idEdges[host.ProxyHostID][candidate.Hostname] = struct{}{}
		}
	}

	visitedHostnames := make(map[string]bool)
	visitedIDs := make(map[int]bool)
	suggestions := make([]hostnameSuggestion, 0)
	ambiguities := make([]hostnameSuggestionAmbiguity, 0)

	hostnames := make([]string, 0, len(hostEdges))
	for hostname := range hostEdges {
		hostnames = append(hostnames, hostname)
	}
	sort.Strings(hostnames)

	for _, start := range hostnames {
		if visitedHostnames[start] {
			continue
		}
		componentHostnames := []string{}
		componentIDs := []int{}
		hostQueue := []string{start}
		idQueue := []int{}
		for len(hostQueue) > 0 || len(idQueue) > 0 {
			for len(hostQueue) > 0 {
				hostname := hostQueue[0]
				hostQueue = hostQueue[1:]
				if visitedHostnames[hostname] {
					continue
				}
				visitedHostnames[hostname] = true
				componentHostnames = append(componentHostnames, hostname)
				for id := range hostEdges[hostname] {
					if !visitedIDs[id] {
						idQueue = append(idQueue, id)
					}
				}
			}
			for len(idQueue) > 0 {
				id := idQueue[0]
				idQueue = idQueue[1:]
				if visitedIDs[id] {
					continue
				}
				visitedIDs[id] = true
				componentIDs = append(componentIDs, id)
				for hostname := range idEdges[id] {
					if !visitedHostnames[hostname] {
						hostQueue = append(hostQueue, hostname)
					}
				}
			}
		}

		sort.Strings(componentHostnames)
		sort.Ints(componentIDs)
		if len(componentHostnames) == 1 && len(componentIDs) == 1 {
			candidate := candidateByHostname[componentHostnames[0]]
			suggestions = append(suggestions, hostnameSuggestion{
				ProxyHostID: componentIDs[0],
				Hostname:    candidate.Hostname,
				Source:      candidate.Source,
			})
			continue
		}
		ambiguousCandidates := make([]hostnameSuggestionCandidate, 0, len(componentHostnames))
		for _, hostname := range componentHostnames {
			ambiguousCandidates = append(ambiguousCandidates, candidateByHostname[hostname])
		}
		ambiguities = append(ambiguities, hostnameSuggestionAmbiguity{
			ProxyHostIDs: componentIDs,
			Candidates:   ambiguousCandidates,
		})
	}

	sort.Slice(suggestions, func(i, j int) bool {
		if suggestions[i].Hostname != suggestions[j].Hostname {
			return suggestions[i].Hostname < suggestions[j].Hostname
		}
		return suggestions[i].ProxyHostID < suggestions[j].ProxyHostID
	})
	sort.Slice(ambiguities, func(i, j int) bool {
		left := ambiguities[i].Candidates[0].Hostname
		right := ambiguities[j].Candidates[0].Hostname
		if left != right {
			return left < right
		}
		return ambiguities[i].ProxyHostIDs[0] < ambiguities[j].ProxyHostIDs[0]
	})
	return suggestions, ambiguities, nil
}

func buildHostnameSuggestionProposal(suggestions []hostnameSuggestion, eligible []eligibleHostnameSuggestionHost, browserScheme string) ([]byte, error) {
	if browserScheme != "http" && browserScheme != "https" {
		return nil, errors.New("browser scheme must be http or https")
	}
	if len(suggestions) > maxServiceMetadataEntries {
		return nil, fmt.Errorf("hostname proposal contains more than %d services", maxServiceMetadataEntries)
	}

	eligibleByID := make(map[int]eligibleHostnameSuggestionHost, len(eligible))
	for _, host := range eligible {
		eligibleByID[host.ProxyHostID] = host
	}
	ordered := append([]hostnameSuggestion(nil), suggestions...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Hostname != ordered[j].Hostname {
			return ordered[i].Hostname < ordered[j].Hostname
		}
		return ordered[i].ProxyHostID < ordered[j].ProxyHostID
	})

	entries := make([]serviceMetadataEntry, 0, len(ordered))
	for _, suggestion := range ordered {
		host, exists := eligibleByID[suggestion.ProxyHostID]
		if !exists {
			return nil, errors.New("hostname proposal references an ineligible proxy host")
		}
		hostname, valid := normalizeHostnameSuggestion(suggestion.Hostname)
		if !valid || hostname != suggestion.Hostname {
			return nil, errors.New("hostname proposal contains an invalid candidate")
		}
		name := hostname
		if host.ExistingName != "" {
			name = host.ExistingName
		}
		proposalURL := (&url.URL{Scheme: browserScheme, Host: hostname}).String()
		entryName := name
		entryURL := proposalURL
		entries = append(entries, serviceMetadataEntry{
			ProxyHostID: suggestion.ProxyHostID,
			Name:        &entryName,
			URL:         &entryURL,
		})
	}
	return serializeServiceMetadataDocumentV1(entries)
}
