package main

import (
	"errors"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

var errPersonalForbidden = errors.New("forbidden")
var errPersonalCSRF = errors.New("csrf_expired")

func (s *PersonalServicesStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.collections = nil
	return s.storage.Close()
}

// Ownership is independent of destination eligibility. This checks the complete
// Users API evidence, not roles, devices, tags or a persisted authorization cache.
func personalOwner(login string, data *CacheData, now time.Time, interval time.Duration) (string, error) {
	if !validPersonalLogin(login) || data == nil || data.Policy == nil || data.ControlPlane.Provider != controlPlaneTailscale || interval <= 0 || interval > time.Duration(math.MaxInt64/3) || data.UpdatedAt.IsZero() || data.UpdatedAt.After(now) || now.Sub(data.UpdatedAt) > 3*interval {
		return "", errPersonalUnavailable
	}
	owner, ok := data.TailscaleUsersByLogin[login]
	if !ok || !owner.DirectMember || !validTailscaleUserID(owner.ID) {
		return "", errPersonalUnavailable
	}
	return owner.ID, nil
}

// No login reassignment or renamed-ID adoption. Even an absent login cannot
// initialize a new collection for an ID already bound to a different login.
func (s *PersonalServicesStore) ownedCollection(snapshot personalStorageSnapshot, login, owner string) (personalStorageCollection, bool, error) {
	if snapshot.Namespace != s.config.Namespace || !validPersonalStoreID(snapshot.StoreID) {
		return personalStorageCollection{}, false, errPersonalUnavailable
	}
	for _, record := range snapshot.Collections {
		if record.Login == login || record.UserID == owner {
			if record.Login != login || record.UserID != owner {
				return personalStorageCollection{}, false, errPersonalUnavailable
			}
			return record, true, nil
		}
	}
	return personalStorageCollection{Login: login, UserID: owner, Revision: "0", Document: PersonalServicesDocument{Version: 1, Entries: []PersonalService{}}}, false, nil
}

func (s *PersonalServicesStore) durableState(snapshot personalStorageSnapshot, record personalStorageCollection, initialized bool) PersonalServicesState {
	return PersonalServicesState{Durable: true, StoreID: snapshot.StoreID, Generation: s.generation, Revision: record.Revision, Initialized: initialized, Document: copyPersonalDocument(record.Document)}
}

// ReadForSnapshot keeps the read and projection on one captured complete cache
// pointer. The clock is evaluated AFTER any lock wait, never at request start.
// Production passes time.Now; a required clock is a fixture seam, not authority.
func (s *PersonalServicesStore) ReadForSnapshot(login string, data *CacheData, interval time.Duration, now func() time.Time) (PersonalServicesState, error) {
	if s == nil || now == nil {
		return PersonalServicesState{}, errPersonalUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readForSnapshotLocked(login, data, interval, now)
}

// Caller holds s.mu. Shared application selection pins this owned read through
// its editor operation; never recursively acquire the public ReadForSnapshot lock.
// storage.Read may transiently acquire storage.mu, exactly as the public read did.
func (s *PersonalServicesStore) readForSnapshotLocked(login string, data *CacheData, interval time.Duration, now func() time.Time) (PersonalServicesState, error) {
	if s == nil || now == nil || s.closed {
		return PersonalServicesState{}, errPersonalUnavailable
	}
	if !validPersonalLogin(login) {
		return PersonalServicesState{}, errPersonalInvalid
	}
	if s.storage == nil {
		return s.readLocked(login), nil
	}
	snapshot, _, err := s.storage.Read()
	if err != nil {
		return PersonalServicesState{}, err
	}
	owner, err := personalOwner(login, data, now(), interval)
	if err != nil {
		return PersonalServicesState{}, err
	}
	record, initialized, err := s.ownedCollection(snapshot, login, owner)
	if err != nil {
		return PersonalServicesState{}, err
	}
	return s.durableState(snapshot, record, initialized), nil
}

// browserCommitCheck is concrete mandatory browser authority, not a callback
// callers can replace with a no-op. It repeats the trusted-source and exact
// identity/Host/Origin/CSRF checks at commit time, including after slow fsync.
func (s *PersonalServicesStore) browserCommitCheck(r *http.Request, login, csrf string, trusted *net.IPNet, now time.Time) error {
	if r == nil || r.URL == nil || r.Context().Err() != nil || r.Method != http.MethodPost || r.URL.Path != personalServicesRoute || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return errPersonalForbidden
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if percent := strings.LastIndexByte(host, '%'); percent >= 0 {
		host = host[:percent]
	}
	ip := net.ParseIP(host)
	identity := IdentityFromContext(r.Context())
	contentType, single, _ := singleRawHeader(r.Header, "Content-Type")
	if trusted == nil || ip == nil || !trusted.Contains(ip) || identity == nil || identity.Login != login || !s.requestIdentity(r) || !single || contentType != "application/json" {
		return errPersonalForbidden
	}
	if !s.validCSRF(login, csrf, now) {
		if s.storage != nil {
			return errPersonalCSRF
		}
		return errPersonalForbidden
	}
	return nil
}

// MutateBrowser retains the browser API and its mandatory concrete CSRF checks.
// The HTTP adapter owns the existing personal admission slot; the common body
// does not reacquire it. Upstream revocation is not transactional with rename.
func (s *PersonalServicesStore) MutateBrowser(r *http.Request, request PersonalServicesMutation, cache *Cache, trusted *net.IPNet, interval time.Duration, now func() time.Time) (PersonalServicesState, *CacheData, error) {
	return s.mutateOperation(personalOperationRequest{transport: personalBrowserTransport, request: r, csrf: request.CSRF}, request, cache, trusted, interval, now)
}
