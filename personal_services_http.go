package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	personalServicesRoute   = "/api/personal-services"
	maxPersonalRequestBytes = 128 * 1024
	personalCSRFDuration    = 30 * time.Minute
)

type personalServicesResponse struct {
	PersonalServicesState
	CSRF          string                  `json:"csrf"`
	IdentityScope string                  `json:"identity_scope"`
	Icons         []string                `json:"icons"`
	Projection    []PersonalServiceStatus `json:"projection"`
}

type personalServicesHandler struct {
	store    *PersonalServicesStore
	cache    *Cache
	interval time.Duration
	now      func() time.Time
}

// Mount only when enabled; this never initializes or calls the shared writer.
func mountPersonalServices(mux *http.ServeMux, store *PersonalServicesStore, cache *Cache, trusted *net.IPNet, interval time.Duration) {
	if store != nil {
		mux.Handle(personalServicesRoute, newPersonalServicesHandler(store, cache, trusted, interval))
	}
}

func newPersonalServicesHandler(store *PersonalServicesStore, cache *Cache, trusted *net.IPNet, interval time.Duration) http.Handler {
	h := &personalServicesHandler{store: store, cache: cache, interval: interval, now: time.Now}
	protected := IdentityMiddleware(trusted, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setIdentityResponseCacheHeaders(w.Header())
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/json")
		// Acquire before decoding or copying any document; do not queue bodies.
		select {
		case store.requests <- struct{}{}:
			defer func() { <-store.requests }()
		default:
			writeEditorJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "busy"})
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (s *PersonalServicesStore) requestIdentity(r *http.Request) bool {
	identity := IdentityFromContext(r.Context())
	if identity == nil || !validPersonalLogin(identity.Login) || r.Host != s.config.Host {
		return false
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
		value, single, present := singleRawHeader(r.Header, key)
		if present && !single || key == "Tailscale-User-Login" && (!single || value != identity.Login) {
			return false
		}
	}
	origin, single, present := singleRawHeader(r.Header, "Origin")
	if present && (!single || origin != s.config.Origin) || r.Method == http.MethodPost && !present {
		return false
	}
	for _, key := range []string{"Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		value, single, present := singleRawHeader(r.Header, key)
		if !present {
			continue
		}
		if !single {
			return false
		}
		switch key {
		case "Sec-Fetch-Site":
			if value != "same-origin" && value != "none" {
				return false
			}
		case "Sec-Fetch-Mode":
			if value != "cors" && value != "same-origin" {
				return false
			}
		case "Sec-Fetch-Dest":
			if value != "empty" {
				return false
			}
		}
	}
	return true
}

func (h *personalServicesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) { writeEditorJSON(w, status, map[string]string{"error": code}) }
	if !h.store.requestIdentity(r) {
		fail(http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.Path != personalServicesRoute || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(http.StatusNotFound, "not_found")
		return
	}
	identity := IdentityFromContext(r.Context())
	var state PersonalServicesState
	var err error
	if r.Method == http.MethodPost {
		contentType, single, _ := singleRawHeader(r.Header, "Content-Type")
		if !single || contentType != "application/json" {
			fail(http.StatusUnsupportedMediaType, "unsupported_media_type")
			return
		}
		request, decodeErr := decodePersonalMutation(w, r)
		if decodeErr != nil {
			var maxErr *http.MaxBytesError
			if errors.As(decodeErr, &maxErr) {
				fail(http.StatusRequestEntityTooLarge, "body_too_large")
			} else {
				fail(http.StatusBadRequest, "invalid_request")
			}
			return
		}
		if !h.store.validCSRF(identity.Login, request.CSRF, h.now()) {
			fail(http.StatusForbidden, "forbidden")
			return
		}
		state, err = h.store.Mutate(identity.Login, request)
	} else {
		state, err = h.store.Read(identity.Login)
	}
	if err != nil {
		switch {
		case errors.Is(err, errPersonalConflict):
			fail(http.StatusConflict, "conflict")
		case errors.Is(err, errPersonalCapacity):
			fail(http.StatusInsufficientStorage, "capacity")
		case errors.Is(err, errPersonalInvalid):
			fail(http.StatusBadRequest, "invalid_request")
		default:
			fail(http.StatusServiceUnavailable, "unavailable")
		}
		return
	}
	var data *CacheData
	if h.cache != nil {
		data = h.cache.Get()
	}
	_, statuses := ProjectPersonalServices(identity, data, state, h.now(), h.interval)
	scope := sha256.Sum256([]byte(identity.Login))
	writeEditorJSON(w, http.StatusOK, personalServicesResponse{PersonalServicesState: state, CSRF: h.store.csrf(identity.Login, h.now()), IdentityScope: hex.EncodeToString(scope[:]), Icons: append([]string{}, serviceIconIDs...), Projection: statuses})
}

func decodePersonalMutation(w http.ResponseWriter, r *http.Request) (PersonalServicesMutation, error) {
	var request PersonalServicesMutation
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPersonalRequestBytes))
	if err != nil {
		return request, err
	}
	if !utf8.Valid(data) || !json.Valid(data) || !personalJSONScalars(data) || rejectDuplicateJSONFields(data) != nil {
		return request, errPersonalInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return request, errPersonalInvalid
	}
	for _, key := range []string{"action", "generation", "revision", "csrf"} {
		value, valid := personalJSONString(fields[key])
		if !valid || value == "" || len(value) > 256 {
			return request, errPersonalInvalid
		}
	}
	if err := json.Unmarshal(data, &request); err != nil {
		return request, errPersonalInvalid
	}
	if len(request.Generation) != 32 {
		return request, errPersonalInvalid
	}
	for _, c := range request.Generation {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return request, errPersonalInvalid
		}
	}
	revision, err := strconv.ParseUint(request.Revision, 10, 64)
	if err != nil || strconv.FormatUint(revision, 10) != request.Revision {
		return request, errPersonalInvalid
	}
	allowed := []string{"action", "generation", "revision", "csrf"}
	switch request.Action {
	case "create", "update":
		allowed = append(allowed, "name", "url", "icon")
		for _, key := range []string{"name", "url"} {
			if _, valid := personalJSONString(fields[key]); !valid {
				return request, errPersonalInvalid
			}
		}
		if raw, present := fields["icon"]; present {
			if _, valid := personalJSONString(raw); !valid {
				return request, errPersonalInvalid
			}
		}
		if request.Action == "update" {
			allowed = append(allowed, "id")
			if !validPersonalID(request.ID) {
				return request, errPersonalInvalid
			}
		}
	case "delete":
		allowed = append(allowed, "id")
		if !validPersonalID(request.ID) {
			return request, errPersonalInvalid
		}
	case "restore":
		allowed = append(allowed, "document")
		if request.Document == nil || !strictPersonalDocument(fields["document"]) {
			return request, errPersonalInvalid
		}
	default:
		return request, errPersonalInvalid
	}
	if !containsOnlyJSONFields(fields, allowed...) {
		return request, errPersonalInvalid
	}
	return request, nil
}

func personalJSONString(raw json.RawMessage) (string, bool) {
	var value string
	returnValue := len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && json.Unmarshal(raw, &value) == nil
	return value, returnValue
}

func strictPersonalDocument(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 || !containsOnlyJSONFields(fields, "version", "entries") || !bytes.Equal(bytes.TrimSpace(fields["version"]), []byte("1")) {
		return false
	}
	var entries []map[string]json.RawMessage
	if json.Unmarshal(fields["entries"], &entries) != nil || entries == nil {
		return false
	}
	for _, entry := range entries {
		if !containsOnlyJSONFields(entry, "id", "name", "url", "icon") {
			return false
		}
		for _, key := range []string{"id", "name", "url"} {
			if _, valid := personalJSONString(entry[key]); !valid {
				return false
			}
		}
		if icon, present := entry["icon"]; present {
			if _, valid := personalJSONString(icon); !valid {
				return false
			}
		}
	}
	return true
}

// encoding/json accepts unpaired UTF-16 escapes by replacing them with U+FFFD.
// Do not silently repair invalid Unicode in private authored/recovery records.
// Input has already passed json.Valid, so every escape below is well formed.
func personalJSONScalars(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		value, _ := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value < 0xd800 || value > 0xdbff {
			continue
		}
		if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
			return false
		}
		low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
		if err != nil || low < 0xdc00 || low > 0xdfff {
			return false
		}
		i += 6
	}
	return true
}

func (s *PersonalServicesStore) csrf(login string, now time.Time) string {
	stamp := strconv.FormatInt(now.Unix(), 10)
	return stamp + "." + s.csrfMAC(login, stamp)
}

func (s *PersonalServicesStore) csrfMAC(login, stamp string) string {
	payload, _ := json.Marshal([]string{"personal-services-csrf-v1", login, s.config.Origin, s.generation, stamp})
	mac := hmac.New(sha256.New, s.secret[:])
	_, _ = mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *PersonalServicesStore) validCSRF(login, token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || len(token) > 100 {
		return false
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || strconv.FormatInt(issued, 10) != parts[0] {
		return false
	}
	age := now.Sub(time.Unix(issued, 0))
	return age >= 0 && age <= personalCSRFDuration && hmac.Equal([]byte(parts[1]), []byte(s.csrfMAC(login, parts[0])))
}
