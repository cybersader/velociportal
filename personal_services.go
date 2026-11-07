package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxPersonalCollections   = 128
	maxPersonalEntries       = 128
	maxPersonalDocumentBytes = 64 * 1024
)

var (
	errPersonalInvalid     = errors.New("invalid_request")
	errPersonalConflict    = errors.New("conflict")
	errPersonalCapacity    = errors.New("capacity")
	errPersonalUnavailable = errors.New("unavailable")
)

// PersonalService is authored data, never cached authorization. IDs have a
// separate namespace from positive NPM IDs, including when restored by a user.
type PersonalService struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Icon string `json:"icon,omitempty"`
}

type PersonalServicesDocument struct {
	Version int               `json:"version"`
	Entries []PersonalService `json:"entries"`
}

// Revisions are canonical decimal strings (uint64 internally), safe for JS
// without number rounding. An absent collection has revision "0", not an
// initialized empty document. Every successful mutation increments it once.
type PersonalServicesState struct {
	Generation  string                   `json:"generation"`
	Revision    string                   `json:"revision"`
	Initialized bool                     `json:"initialized"`
	Document    PersonalServicesDocument `json:"document"`
}

// Fields are action-specific; the HTTP decoder enforces the exact wire schema.
// Create has Name/URL/Icon, update additionally ID, delete only ID, and restore
// only Document. Every action requires Generation and Revision, even the first.
type PersonalServicesMutation struct {
	Action     string                    `json:"action"`
	Generation string                    `json:"generation"`
	Revision   string                    `json:"revision"`
	CSRF       string                    `json:"csrf"`
	ID         string                    `json:"id,omitempty"`
	Name       string                    `json:"name,omitempty"`
	URL        string                    `json:"url,omitempty"`
	Icon       string                    `json:"icon,omitempty"`
	Document   *PersonalServicesDocument `json:"document,omitempty"`
}

type personalCollection struct {
	revision uint64
	document PersonalServicesDocument
}

// PersonalServicesStore is deliberately only a bounded volatile map. There is
// no filesystem, eviction, background work, upstream I/O or storage adapter.
type PersonalServicesStore struct {
	mu          sync.Mutex
	config      PersonalServicesConfig
	generation  string
	secret      [32]byte
	collections map[string]personalCollection
	requests    chan struct{}
}

func NewPersonalServicesStore(config *PersonalServicesConfig) (*PersonalServicesStore, error) {
	if config == nil {
		return nil, nil
	}
	host, err := validatePortalPublicOrigin(config.Origin)
	if err != nil || host != config.Host {
		return nil, errPersonalInvalid
	}
	s := &PersonalServicesStore{config: *config, collections: make(map[string]personalCollection), requests: make(chan struct{}, 8)}
	if _, err := rand.Read(s.secret[:]); err != nil {
		return nil, errPersonalUnavailable
	}
	s.generation, err = personalRandomID("")
	if err != nil {
		return nil, err
	}
	return s, nil
}

func personalRandomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", errPersonalUnavailable
	}
	return prefix + hex.EncodeToString(value[:]), nil
}

func validPersonalID(id string) bool {
	if len(id) != 35 || !strings.HasPrefix(id, "ps_") {
		return false
	}
	for _, c := range id[3:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func validPersonalLogin(login string) bool {
	if !utf8.ValidString(login) || len(login) > 254 || !exactSSHLogin(login) || strings.TrimSpace(login) != login || containsControl(login) {
		return false
	}
	for _, c := range login {
		if unicode.IsSpace(c) || unicode.Is(unicode.Cf, c) {
			return false
		}
	}
	return true
}

func copyPersonalDocument(document PersonalServicesDocument) PersonalServicesDocument {
	return PersonalServicesDocument{Version: 1, Entries: append([]PersonalService{}, document.Entries...)}
}

func (s *PersonalServicesStore) readLocked(login string) PersonalServicesState {
	collection, initialized := s.collections[login]
	return PersonalServicesState{Generation: s.generation, Revision: strconv.FormatUint(collection.revision, 10), Initialized: initialized, Document: copyPersonalDocument(collection.document)}
}

// Read returns a coherent private copy. It never allocates a collection, even
// when the map is full. The caller must already trust the exact Serve identity.
func (s *PersonalServicesStore) Read(login string) (PersonalServicesState, error) {
	if s == nil {
		return PersonalServicesState{}, errPersonalUnavailable
	}
	if !validPersonalLogin(login) {
		return PersonalServicesState{}, errPersonalInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(login), nil
}

// Mutate validates, checks CAS/capacity and publishes under one lock. Restore is
// only possible for absent collections; an initialized empty one is authoritative.
// It intentionally does not require projection: users can fix unresolved records.
func (s *PersonalServicesStore) Mutate(login string, request PersonalServicesMutation) (PersonalServicesState, error) {
	if s == nil {
		return PersonalServicesState{}, errPersonalUnavailable
	}
	if !validPersonalLogin(login) {
		return PersonalServicesState{}, errPersonalInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, initialized := s.collections[login]
	if request.Generation != s.generation || request.Revision != strconv.FormatUint(current.revision, 10) {
		return PersonalServicesState{}, errPersonalConflict
	}
	if current.revision == math.MaxUint64 {
		return PersonalServicesState{}, errPersonalUnavailable
	}
	if !initialized && len(s.collections) >= maxPersonalCollections {
		return PersonalServicesState{}, errPersonalCapacity
	}
	document := copyPersonalDocument(current.document)
	switch request.Action {
	case "create":
		if request.ID != "" || request.Document != nil {
			return PersonalServicesState{}, errPersonalInvalid
		}
		if len(document.Entries) >= maxPersonalEntries {
			return PersonalServicesState{}, errPersonalCapacity
		}
		id, err := personalRandomID("ps_")
		if err != nil {
			return PersonalServicesState{}, err
		}
		document.Entries = append(document.Entries, PersonalService{ID: id, Name: request.Name, URL: request.URL, Icon: request.Icon})
	case "update", "delete":
		if !validPersonalID(request.ID) || request.Document != nil || request.Action == "delete" && (request.Name != "" || request.URL != "" || request.Icon != "") {
			return PersonalServicesState{}, errPersonalInvalid
		}
		index := -1
		for i, entry := range document.Entries {
			if entry.ID == request.ID {
				index = i
				break
			}
		}
		if index < 0 {
			return PersonalServicesState{}, errPersonalConflict
		}
		if request.Action == "delete" {
			document.Entries = append(document.Entries[:index], document.Entries[index+1:]...)
		} else {
			document.Entries[index] = PersonalService{ID: request.ID, Name: request.Name, URL: request.URL, Icon: request.Icon}
		}
	case "restore":
		if initialized {
			return PersonalServicesState{}, errPersonalConflict
		}
		if request.Document == nil || request.ID != "" || request.Name != "" || request.URL != "" || request.Icon != "" {
			return PersonalServicesState{}, errPersonalInvalid
		}
		document = *request.Document
	default:
		return PersonalServicesState{}, errPersonalInvalid
	}
	if err := validatePersonalDocument(document); err != nil {
		return PersonalServicesState{}, err
	}
	// Never retain a caller-owned restore slice.
	s.collections[login] = personalCollection{revision: current.revision + 1, document: copyPersonalDocument(document)}
	return s.readLocked(login), nil
}

func validatePersonalDocument(document PersonalServicesDocument) error {
	if document.Version != 1 || document.Entries == nil {
		return errPersonalInvalid
	}
	if len(document.Entries) > maxPersonalEntries {
		return errPersonalCapacity
	}
	seen := make(map[string]bool, len(document.Entries))
	for _, entry := range document.Entries {
		if !validPersonalID(entry.ID) || seen[entry.ID] {
			return errPersonalInvalid
		}
		seen[entry.ID] = true
		if _, err := validateServiceMetadataName(entry.Name); err != nil {
			return errPersonalInvalid
		}
		if _, _, err := validatePersonalURL(entry.URL); err != nil {
			return errPersonalInvalid
		}
		if entry.Icon != "" && !validServiceIcon(entry.Icon) {
			return errPersonalInvalid
		}
	}
	data, err := json.Marshal(document)
	if err != nil {
		return errPersonalInvalid
	}
	if len(data) > maxPersonalDocumentBytes {
		return errPersonalCapacity
	}
	return nil
}

// Returns only a hostname and ingress TCP port. No DNS, URL fetching or probing.
// Canonical URL spelling is required rather than silently repaired. Escapes may
// represent printable UTF-8, but cannot hide delimiters, controls or dot segments.
func validatePersonalURL(raw string) (string, int, error) {
	if len(raw) > maxServiceMetadataURL || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw || containsControl(raw) {
		return "", 0, errPersonalInvalid
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.RawPath != "" || u.String() != raw {
		return "", 0, errPersonalInvalid
	}
	host, err := validatePortalPublicOrigin("https://" + u.Host)
	if err != nil || host != u.Host {
		return "", 0, errPersonalInvalid
	}
	name, valid := normalizeMachineTargetName(u.Hostname())
	if !valid || name != u.Hostname() {
		return "", 0, errPersonalInvalid
	}
	if !utf8.ValidString(u.Path) || containsControl(u.Path) || strings.ContainsAny(u.Path, "\\%?#") || strings.Contains(u.Path, "//") {
		return "", 0, errPersonalInvalid
	}
	for _, c := range u.Path {
		if unicode.IsSpace(c) || unicode.Is(unicode.Cf, c) {
			return "", 0, errPersonalInvalid
		}
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", 0, errPersonalInvalid
		}
	}
	port := 443
	if u.Port() != "" {
		port, _ = strconv.Atoi(u.Port())
	}
	return name, port, nil
}

// PersonalServiceStatus never distinguishes missing devices, ambiguity or policy
// denial. The API only returns the viewer's own authored records and this coarse
// state, not resolved topology, accounts or authorization evidence.
type PersonalServiceStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ProjectPersonalServices uses the SAME complete snapshot as the normal portal.
// Only returned cards may be linked. The authored document must remain separate
// for editing/recovery. The UI must not pass personal cards to NPM metadata,
// numeric-ID editing or health joins (PersonalID != "", ID == 0).
func ProjectPersonalServices(identity *Identity, data *CacheData, state PersonalServicesState, now time.Time, interval time.Duration) ([]ServiceCard, []PersonalServiceStatus) {
	cards := []ServiceCard{}
	statuses := make([]PersonalServiceStatus, 0, len(state.Document.Entries))
	available := identity != nil && validPersonalLogin(identity.Login) && data != nil && data.Policy != nil && data.ControlPlane.Provider == controlPlaneTailscale && interval > 0 && !data.UpdatedAt.IsZero() && !data.UpdatedAt.After(now) && now.Sub(data.UpdatedAt) <= interval*3
	// Only the authoritative exact member map can confer human role membership.
	var ids, roles map[string]bool
	var mc *matchContext
	if available {
		_, available = data.GrantRoleSelectorsByLogin[identity.Login]
		if available {
			ids = buildSSHIdentitySet(identity.Login, data.Policy)
			roles = grantRoleSelectorSet(identity.Login, data)
			mc = buildMatchContext(identity.Login, data)
		}
	}
	for _, entry := range state.Document.Entries {
		status := PersonalServiceStatus{ID: entry.ID, Status: "unavailable"}
		name, port, err := validatePersonalURL(entry.URL)
		if available && err == nil {
			var selected Node
			matches := 0
			for _, node := range data.Nodes {
				if normalized, valid := normalizeMachineTargetName(node.Name); valid && normalized == name {
					selected = node
					matches++
				}
			}
			if matches == 1 {
				if _, _, _, granted := nodeTCPGrantEvidence(selected, data.Policy.Grants, ids, roles, mc, port); granted {
					status.Status = "active"
					cards = append(cards, ServiceCard{PersonalID: entry.ID, Name: entry.Name, URL: entry.URL, Domain: name, Icon: entry.Icon, LinkState: serviceLinkReady})
				}
			}
		}
		statuses = append(statuses, status)
	}
	sort.Slice(cards, func(i, j int) bool {
		a, b := strings.ToLower(cards[i].Name), strings.ToLower(cards[j].Name)
		if a != b {
			return a < b
		}
		return cards[i].PersonalID < cards[j].PersonalID
	})
	return cards, statuses
}
