package main

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"time"
)

// This is a typed operation, not a wire decoder. Later tool adapters must reject
// duplicate/null/unknown/case-alias fields before conversion. No document, restore,
// import, caller login or authorization receipt can be supplied here.
type personalMCPMutation struct {
	StoreID, Generation, Revision string
	Action                        string // create | update | delete only
	ID, Name, URL, Icon           string
}

type personalOperationTransport uint8

const (
	personalBrowserTransport personalOperationTransport = iota + 1
	personalMCPTransport
)

// Concrete request data only, never permission. Zero/unknown transport fails
// closed; native MCP is not a browser POST with CSRF skipped.
type personalOperationRequest struct {
	transport personalOperationTransport
	request   *http.Request
	csrf      string
	mcp       *MCPConfig
}

func (a personalOperationRequest) check(s *PersonalServicesStore, login string, trusted *net.IPNet, at time.Time) error {
	if s == nil || a.request == nil || a.request.URL == nil {
		return errPersonalUnavailable
	}
	switch a.transport {
	case personalBrowserTransport:
		return s.browserCommitCheck(a.request, login, a.csrf, trusted, at)
	case personalMCPTransport:
		// These immutable configuration fields can be checked without recursively
		// acquiring storage.mu, including inside the backend's final callback.
		if a.mcp == nil || s.storage == nil || !validPersonalFile(s.config.File) ||
			!validPersonalNamespace(s.config.Namespace) || s.config.File != s.storage.path ||
			s.config.Namespace != s.storage.namespace {
			return errPersonalUnavailable
		}
		host, err := validatePortalPublicOrigin(s.config.Origin)
		if err != nil || host != s.config.Host || a.mcp.Origin != s.config.Origin || a.mcp.Host != s.config.Host {
			return errPersonalUnavailable
		}
		if checkMCPToolRequest(a.request, a.mcp, trusted) != nil {
			return errPersonalForbidden
		}
		identity := IdentityFromContext(a.request.Context())
		if identity == nil || identity.Login != login {
			return errPersonalForbidden
		}
		return nil
	default:
		return errPersonalForbidden
	}
}

// Caller holds personal.mu. This helper never calls the store/backend, so it is
// safe under storage.mu at pre-rename. Sample time BEFORE recapturing the cache:
// a blocking clock/read seam must never leave an older snapshot as authority.
func (a personalOperationRequest) owner(s *PersonalServicesStore, login string, cache *Cache, trusted *net.IPNet, interval time.Duration, now func() time.Time) (string, *CacheData, error) {
	at := now()
	if err := a.check(s, login, trusted, at); err != nil {
		return "", nil, err
	}
	var data *CacheData
	if cache != nil {
		data = cache.Get()
	}
	owner, err := personalOwner(login, data, at, interval)
	if err != nil {
		return "", nil, err
	}
	return owner, data, nil
}

func validatePersonalMCPMutation(req personalMCPMutation) error {
	if !validPersonalStoreID(req.StoreID) || !validPersonalStoreID(req.Generation) {
		return errPersonalInvalid
	}
	revision, err := strconv.ParseUint(req.Revision, 10, 64)
	if err != nil || strconv.FormatUint(revision, 10) != req.Revision {
		return errPersonalInvalid
	}
	switch req.Action {
	case "create":
		if req.ID != "" {
			return errPersonalInvalid
		}
	case "update":
		if !validPersonalID(req.ID) {
			return errPersonalInvalid
		}
	case "delete":
		if !validPersonalID(req.ID) || req.Name != "" || req.URL != "" || req.Icon != "" {
			return errPersonalInvalid
		}
		return nil
	default:
		return errPersonalInvalid
	}
	if _, err := validateServiceMetadataName(req.Name); err != nil {
		return errPersonalInvalid
	}
	if _, _, err := validatePersonalURL(req.URL); err != nil {
		return errPersonalInvalid
	}
	if req.Icon != "" && !validServiceIcon(req.Icon) {
		return errPersonalInvalid
	}
	return nil
}

// ReadMCP returns only this caller's authored state plus current data for the
// eventual projector. Discovery/initialization grants no authority. The route
// slot is already held by the future adapter; acquire the SAME personal slot
// used by browser HTTP exactly once before any private read or copy.
func (s *PersonalServicesStore) ReadMCP(r *http.Request, config *MCPConfig, cache *Cache, trusted *net.IPNet, interval time.Duration, now func() time.Time) (PersonalServicesState, *CacheData, error) {
	fail := func(err error) (PersonalServicesState, *CacheData, error) { return PersonalServicesState{}, nil, err }
	if s == nil || r == nil || now == nil || cap(s.requests) != 8 {
		return fail(errPersonalUnavailable)
	}
	identity := IdentityFromContext(r.Context())
	if identity == nil {
		return fail(errPersonalForbidden)
	}
	login := identity.Login
	a := personalOperationRequest{transport: personalMCPTransport, request: r, mcp: config}
	if err := a.check(s, login, trusted, now()); err != nil {
		return fail(err)
	}
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		return fail(errPersonalUnavailable)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fail(errPersonalUnavailable)
	}
	owner, _, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	snapshot, _, err := s.storage.Read()
	if err != nil {
		return fail(err)
	}
	latestOwner, _, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	if latestOwner != owner {
		return fail(errPersonalUnavailable)
	}
	record, initialized, err := s.ownedCollection(snapshot, login, owner)
	if err != nil {
		return fail(err)
	}
	state := s.durableState(snapshot, record, initialized)
	latestOwner, data, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	if latestOwner != owner {
		return fail(errPersonalUnavailable)
	}
	return state, data, nil
}

func (s *PersonalServicesStore) MutateMCP(r *http.Request, req personalMCPMutation, config *MCPConfig, cache *Cache, trusted *net.IPNet, interval time.Duration, now func() time.Time) (PersonalServicesState, *CacheData, error) {
	if s == nil || cap(s.requests) != 8 {
		return PersonalServicesState{}, nil, errPersonalUnavailable
	}
	// Admission is immediate and bounds every path into the common read/copy body.
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		return PersonalServicesState{}, nil, errPersonalUnavailable
	}
	if err := validatePersonalMCPMutation(req); err != nil {
		return PersonalServicesState{}, nil, err
	}
	request := PersonalServicesMutation{StoreID: req.StoreID, Generation: req.Generation, Revision: req.Revision, Action: req.Action, ID: req.ID, Name: req.Name, URL: req.URL, Icon: req.Icon}
	return s.mutateOperation(personalOperationRequest{transport: personalMCPTransport, request: r, mcp: config}, request, cache, trusted, interval, now)
}

// One browser/MCP mutation body. Browser HTTP owns its incumbent admission slot;
// this body MUST NOT acquire another. Order: route slot -> personal admission ->
// personal.mu -> storage.mu. No editor lock, background writer, retry or replay.
func (s *PersonalServicesStore) mutateOperation(a personalOperationRequest, request PersonalServicesMutation, cache *Cache, trusted *net.IPNet, interval time.Duration, now func() time.Time) (PersonalServicesState, *CacheData, error) {
	fail := func(err error) (PersonalServicesState, *CacheData, error) { return PersonalServicesState{}, nil, err }
	if s == nil || a.request == nil || now == nil {
		return fail(errPersonalUnavailable)
	}
	identity := IdentityFromContext(a.request.Context())
	if identity == nil {
		return fail(errPersonalForbidden)
	}
	login := identity.Login
	if err := a.check(s, login, trusted, now()); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fail(errPersonalUnavailable)
	}
	if err := a.check(s, login, trusted, now()); err != nil {
		return fail(err)
	}
	if s.storage == nil {
		state, err := s.mutateRAMLocked(login, request)
		if err != nil {
			return fail(err)
		}
		var data *CacheData
		if cache != nil {
			data = cache.Get()
		}
		return state, data, nil
	}
	owner, _, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	snapshot, version, err := s.storage.Read()
	if err != nil {
		return fail(err)
	}
	latestOwner, _, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	if latestOwner != owner {
		return fail(errPersonalUnavailable)
	}
	record, initialized, err := s.ownedCollection(snapshot, login, owner)
	if err != nil {
		return fail(err)
	}
	if request.StoreID != snapshot.StoreID || request.Generation != s.generation || request.Revision != record.Revision {
		return fail(errPersonalConflict)
	}
	revision, err := strconv.ParseUint(record.Revision, 10, 64)
	if err != nil || revision == math.MaxUint64 {
		return fail(errPersonalUnavailable)
	}
	if !initialized && len(snapshot.Collections) >= maxPersonalCollections {
		return fail(errPersonalCapacity)
	}
	document, err := mutatePersonalDocument(record.Document, initialized, request)
	if err != nil {
		return fail(err)
	}
	record.Document, record.Revision = document, strconv.FormatUint(revision+1, 10)
	if initialized {
		for i := range snapshot.Collections {
			if snapshot.Collections[i].Login == login {
				snapshot.Collections[i] = record
				break
			}
		}
	} else {
		snapshot.Collections = append(snapshot.Collections, record)
	}
	// Core-owned mandatory concrete check. Never reenter s/storage while Commit
	// holds storage.mu. Cancellation before rename denies; after rename is NOT rollback.
	finalCheck := func() error {
		latestOwner, _, err := a.owner(s, login, cache, trusted, interval, now)
		if err != nil {
			return err
		}
		if latestOwner != owner {
			return errPersonalUnavailable
		}
		return nil
	}
	confirmed, _, err := s.storage.Commit(version, snapshot, finalCheck)
	if err != nil {
		return fail(err)
	}
	// Slow directory sync can outlive request/owner/freshness authority. Withhold
	// private output even after a confirmed write; do not replay or assert rollback.
	latestOwner, data, err := a.owner(s, login, cache, trusted, interval, now)
	if err != nil {
		return fail(err)
	}
	if latestOwner != owner {
		return fail(errPersonalUnavailable)
	}
	return s.durableState(confirmed, record, true), data, nil
}
