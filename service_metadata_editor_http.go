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
	serviceMetadataEditorRoute  = "/api/service-metadata/"
	maxServiceMetadataEditBytes = 16 * 1024
	serviceMetadataCSRFDuration = 30 * time.Minute
)

var errMetadataNotVisible = errors.New("service not available")
var errMetadataStale = errors.New("fresh complete snapshot required")
var errMetadataCSRF = errors.New("invalid or expired csrf")

// API contract for the dashboard: GET returns this selected authorized form
// only. The scope is the same opaque exact-login SHA-256 used by preferences.
// Defaults are current NPM presentation defaults, not private backend topology.
// revision covers the whole file; target_revision covers the current NPM target
// and defaults. POST success is {"status":"saved"}; it does not return a catalog.
type serviceMetadataEditableFields struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Icon string `json:"icon"`
}
type serviceMetadataEditForm struct {
	ID             int                           `json:"id"`
	Fields         serviceMetadataEditableFields `json:"fields"`
	Defaults       serviceMetadataEditableFields `json:"defaults"`
	Icons          []string                      `json:"icons"`
	Revision       string                        `json:"revision"`
	TargetRevision string                        `json:"target_revision"`
	CSRF           string                        `json:"csrf"`
	IdentityScope  string                        `json:"identity_scope"`
}

// Save requires all three fields, with exact empty strings meaning removal.
// Reset requires only action/revision/target_revision/csrf and leaves category,
// order and every unrelated entry untouched. Null, absent required strings,
// unknown/case-variant keys, duplicates and trailing JSON are invalid.
type serviceMetadataEditRequest struct {
	Action         string `json:"action"`
	Revision       string `json:"revision"`
	TargetRevision string `json:"target_revision"`
	CSRF           string `json:"csrf"`
	Name           string `json:"name"`
	URL            string `json:"url"`
	Icon           string `json:"icon"`
}

type serviceMetadataEditorHandler struct {
	editor     *ServiceMetadataEditor
	cache      *Cache
	staleAfter time.Duration
	now        func() time.Time
}

func newServiceMetadataEditorHandler(editor *ServiceMetadataEditor, cache *Cache, trusted *net.IPNet, interval time.Duration) http.Handler {
	handler := &serviceMetadataEditorHandler{editor: editor, cache: cache, staleAfter: interval * 3, now: time.Now}
	protected := IdentityMiddleware(trusted, handler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setIdentityResponseCacheHeaders(w.Header())
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Type", "application/json")
		protected.ServeHTTP(w, r)
	})
}

func singleRawHeader(header http.Header, name string) (string, bool, bool) {
	count := 0
	value := ""
	for key, values := range header {
		if strings.EqualFold(key, name) {
			for _, v := range values {
				count++
				value = v
			}
		}
	}
	return value, count == 1, count > 0
}

func (e *ServiceMetadataEditor) editingIdentity(r *http.Request) bool {
	identity := IdentityFromContext(r.Context())
	if e == nil || identity == nil || !e.config.Logins[identity.Login] || r.Host != e.config.Host {
		return false
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
		value, single, present := singleRawHeader(r.Header, key)
		if present && !single || key == "Tailscale-User-Login" && (!single || value != identity.Login) {
			return false
		}
	}
	return true
}

func (h *serviceMetadataEditorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, message string) { writeEditorJSON(w, code, map[string]string{"error": message}) }
	identity := IdentityFromContext(r.Context())
	if !h.editor.editingIdentity(r) {
		fail(http.StatusForbidden, "forbidden")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		fail(http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		fail(http.StatusNotFound, "not_found")
		return
	}
	idText := strings.TrimPrefix(r.URL.Path, serviceMetadataEditorRoute)
	id, err := strconv.Atoi(idText)
	if err != nil || id <= 0 || strconv.Itoa(id) != idText || r.URL.Path != serviceMetadataEditorRoute+idText {
		fail(http.StatusNotFound, "not_found")
		return
	}
	if r.Method == http.MethodPost {
		origin, single, _ := singleRawHeader(r.Header, "Origin")
		if !single || origin != h.editor.config.Origin {
			fail(http.StatusForbidden, "forbidden")
			return
		}
		site, single, present := singleRawHeader(r.Header, "Sec-Fetch-Site")
		if present && (!single || (site != "same-origin" && site != "none")) {
			fail(http.StatusForbidden, "forbidden")
			return
		}
		contentType, single, _ := singleRawHeader(r.Header, "Content-Type")
		if !single || contentType != "application/json" {
			fail(http.StatusUnsupportedMediaType, "unsupported_media_type")
			return
		}
		request, err := decodeServiceMetadataEditRequest(w, r)
		if err != nil {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				fail(http.StatusRequestEntityTooLarge, "body_too_large")
			} else {
				fail(http.StatusBadRequest, "invalid_request")
			}
			return
		}
		if !h.editor.validCSRF(identity.Login, request.CSRF, h.now()) {
			fail(http.StatusForbidden, "forbidden")
			return
		}
		h.save(w, identity, id, request)
		return
	}
	h.editor.mu.Lock()
	defer h.editor.mu.Unlock()
	if h.editor.disabled {
		fail(http.StatusServiceUnavailable, "uncertain")
		return
	}
	host, err := h.visibleHost(identity, id, "")
	if err != nil {
		h.respondError(w, err)
		return
	}
	view := h.editor.view.Load()
	if err := h.editor.checkDisk(view); err != nil {
		h.respondError(w, err)
		return
	}
	override := view.metadata.Overrides[id]
	defaults, _ := resolveServiceCard(host, nil)
	scope := sha256.Sum256([]byte(identity.Login))
	writeEditorJSON(w, http.StatusOK, serviceMetadataEditForm{
		ID:             id,
		Fields:         serviceMetadataEditableFields{Name: override.Name, URL: override.URL, Icon: override.Icon},
		Defaults:       serviceMetadataEditableFields{Name: defaults.Name, URL: defaults.URL, Icon: "generic"},
		Icons:          append([]string(nil), serviceIconIDs...),
		Revision:       view.revision,
		TargetRevision: h.editor.targetRevision(host),
		CSRF:           h.editor.csrf(identity.Login, h.now()),
		IdentityScope:  hex.EncodeToString(scope[:]),
	})
}

func (h *serviceMetadataEditorHandler) visibleHost(identity *Identity, id int, target string) (ProxyHost, error) {
	data := h.editor.requestData(h.cache.Get())
	now := h.now()
	if data == nil || data.UpdatedAt.IsZero() || now.Sub(data.UpdatedAt) > h.staleAfter || data.UpdatedAt.After(now) {
		return ProxyHost{}, errMetadataStale
	}
	var selected *ProxyHost
	for _, match := range evaluateServices(identity, data) {
		if match.ProxyHost.ID == id {
			if selected != nil {
				return ProxyHost{}, errMetadataNotVisible
			} // Ambiguous IDs fail closed.
			copy := match.ProxyHost
			selected = &copy
		}
	}
	if selected == nil {
		return ProxyHost{}, errMetadataNotVisible
	}
	if target != "" && !hmac.Equal([]byte(target), []byte(h.editor.targetRevision(*selected))) {
		return ProxyHost{}, errMetadataConflict
	}
	return *selected, nil
}

func (h *serviceMetadataEditorHandler) save(w http.ResponseWriter, identity *Identity, id int, request serviceMetadataEditRequest) {
	e := h.editor
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.disabled {
		h.respondError(w, errMetadataUncertain)
		return
	}
	if _, err := h.visibleHost(identity, id, request.TargetRevision); err != nil {
		h.respondError(w, err)
		return
	}
	document, err := mutateServiceMetadata(e.view.Load().metadata.document, id, request.Name, request.URL, request.Icon, request.Action == "reset")
	if err != nil {
		writeEditorJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_fields"})
		return
	}
	err = e.commit(request.Revision, document, func() error {
		if !e.validCSRF(identity.Login, request.CSRF, h.now()) {
			return errMetadataCSRF
		}
		_, err := h.visibleHost(identity, id, request.TargetRevision)
		return err
	})
	if err != nil {
		h.respondError(w, err)
		return
	}
	writeEditorJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (h *serviceMetadataEditorHandler) respondError(w http.ResponseWriter, err error) {
	status, message := http.StatusInternalServerError, "storage_unavailable"
	switch {
	case errors.Is(err, errMetadataCSRF):
		status, message = http.StatusForbidden, "forbidden"
	case errors.Is(err, errMetadataConflict):
		status, message = http.StatusConflict, "conflict"
	case errors.Is(err, errMetadataNotVisible):
		status, message = http.StatusNotFound, "not_found"
	case errors.Is(err, errMetadataStale):
		status, message = http.StatusServiceUnavailable, "unavailable"
	case errors.Is(err, errMetadataUncertain):
		status, message = http.StatusServiceUnavailable, "uncertain"
	}
	writeEditorJSON(w, status, map[string]string{"error": message})
}

func decodeServiceMetadataEditRequest(w http.ResponseWriter, r *http.Request) (serviceMetadataEditRequest, error) {
	var request serviceMetadataEditRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxServiceMetadataEditBytes))
	if err != nil {
		return request, err
	}
	if !utf8.Valid(data) || rejectDuplicateJSONFields(data) != nil {
		return request, errors.New("invalid request")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil || !containsOnlyJSONFields(fields, "action", "revision", "target_revision", "csrf", "name", "url", "icon") {
		return request, errors.New("invalid fields")
	}
	for _, field := range []string{"action", "revision", "target_revision", "csrf"} {
		var value string
		raw, present := fields[field]
		if !present || json.Unmarshal(raw, &value) != nil || value == "" || len(value) > 256 {
			return request, errors.New("missing field")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || requireJSONEOF(decoder) != nil {
		return request, errors.New("invalid request")
	}
	if request.Action != "save" && request.Action != "reset" {
		return request, errors.New("invalid action")
	}
	for _, field := range []string{"name", "url", "icon"} {
		raw, present := fields[field]
		if request.Action == "reset" {
			if present {
				return request, errors.New("reset must not carry fields")
			}
			continue
		}
		var value string
		if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return request, errors.New("missing editable field")
		}
	}
	return request, nil
}

func (e *ServiceMetadataEditor) csrf(login string, now time.Time) string {
	stamp := strconv.FormatInt(now.Unix(), 10)
	return stamp + "." + e.csrfMAC(login, stamp)
}
func (e *ServiceMetadataEditor) csrfMAC(login, stamp string) string {
	payload, _ := json.Marshal([]string{login, e.config.Origin, stamp})
	return e.digest("csrf", payload)
}
func (e *ServiceMetadataEditor) validCSRF(login, token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	issued, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || strconv.FormatInt(issued, 10) != parts[0] {
		return false
	}
	age := now.Sub(time.Unix(issued, 0))
	return age >= 0 && age <= serviceMetadataCSRFDuration && hmac.Equal([]byte(parts[1]), []byte(e.csrfMAC(login, parts[0])))
}

func writeEditorJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
