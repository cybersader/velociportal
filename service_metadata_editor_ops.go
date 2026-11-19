package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errMetadataForbidden = errors.New("forbidden")
var errMetadataInvalidFields = errors.New("invalid fields")

// SharedIconOperations owns selected presentation operations, not another writer
// or authorization store. NPM/Machine operations never inspect or lock the
// independent personal store.
type SharedIconOperations struct {
	editor   *ServiceMetadataEditor
	cache    *Cache
	personal *PersonalServicesStore
	mode     serviceEditMode
	binding  serviceMetadataBinding
	trusted  *net.IPNet
	interval time.Duration
	now      func() time.Time
	origin   string
	host     string
	mcp      *MCPConfig

	// customIcons is the PORTAL_CUSTOM_ICONS opt-in; it adds no authority.
	customIcons bool
}

// Capture resolved values without I/O, validation, defaults or new authority.
// In particular, construction does not validate presentation-only fixture views.
func newSharedIconOperations(cfg *Config, editor *ServiceMetadataEditor, cache *Cache, personal *PersonalServicesStore) *SharedIconOperations {
	o := &SharedIconOperations{editor: editor, cache: cache, personal: personal, now: time.Now}
	if cfg == nil {
		return o
	}
	o.mode, o.binding, o.interval = cfg.PortalEditMode, cfg.metadataBinding(), cfg.PollInterval
	o.customIcons = cfg.CustomIcons
	if cfg.TrustedProxyCIDR != nil {
		o.trusted = &net.IPNet{IP: append(net.IP(nil), cfg.TrustedProxyCIDR.IP...), Mask: append(net.IPMask(nil), cfg.TrustedProxyCIDR.Mask...)}
	}
	if cfg.MCP != nil {
		copy := *cfg.MCP
		o.mcp = &copy
	}
	if editor != nil && editor.config != nil {
		o.origin, o.host = editor.config.Origin, editor.config.Host
	}
	return o
}

// This is a repeated check of the actual request, never a reusable admission
// receipt. Return a detached value so the entry login can be pinned privately.
func (o *SharedIconOperations) browserNPMIdentity(r *http.Request, id int) (*Identity, error) {
	if o == nil || o.editor == nil || o.editor.config == nil || o.now == nil || o.cache == nil || o.trusted == nil || o.origin == "" || o.host == "" {
		return nil, errMetadataStale
	}
	if r == nil || r.URL == nil {
		return nil, errMetadataForbidden
	}
	if r.Context() == nil || r.Context().Err() != nil {
		return nil, errMetadataStale
	}
	if o.mode != serviceEditShared || id <= 0 {
		return nil, errMetadataForbidden
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
	if ip == nil || !o.trusted.Contains(ip) || identity == nil || !validPersonalLogin(identity.Login) || !o.editor.config.Logins[identity.Login] {
		return nil, errMetadataForbidden
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
		value, single, present := singleRawHeader(r.Header, key)
		if present && !single || key == "Tailscale-User-Login" && (!single || value != identity.Login) {
			return nil, errMetadataForbidden
		}
	}
	canonicalHost, err := validatePortalPublicOrigin(o.origin)
	if err != nil || canonicalHost != o.host || r.Host != o.host || o.editor.config.Host != o.host || o.editor.config.Origin != o.origin {
		return nil, errMetadataForbidden
	}
	if r.URL.Path != serviceMetadataEditorRoute+strconv.Itoa(id) || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return nil, errMetadataForbidden
	}
	copy := *identity
	return &copy, nil
}

func (o *SharedIconOperations) checkBrowserNPMRead(r *http.Request, id int, expectedLogin string, at time.Time) (*Identity, error) {
	identity, err := o.browserNPMIdentity(r, id)
	if err != nil {
		return nil, err
	}
	if r.Method != http.MethodGet || expectedLogin == "" || identity.Login != expectedLogin {
		return nil, errMetadataForbidden
	}
	origin, single, present := singleRawHeader(r.Header, "Origin")
	if present && (!single || origin != o.origin) {
		return nil, errMetadataForbidden
	}
	return identity, nil
}

func (o *SharedIconOperations) checkBrowserNPMSave(r *http.Request, id int, req serviceMetadataEditRequest, expectedLogin string, at time.Time) (*Identity, error) {
	identity, err := o.browserNPMIdentity(r, id)
	if err != nil {
		return nil, err
	}
	if r.Method != http.MethodPost || expectedLogin == "" || identity.Login != expectedLogin {
		return nil, errMetadataForbidden
	}
	origin, single, _ := singleRawHeader(r.Header, "Origin")
	if !single || origin != o.origin {
		return nil, errMetadataForbidden
	}
	site, single, present := singleRawHeader(r.Header, "Sec-Fetch-Site")
	if present && (!single || site != "same-origin" && site != "none") {
		return nil, errMetadataForbidden
	}
	contentType, single, _ := singleRawHeader(r.Header, "Content-Type")
	if !single || contentType != "application/json" {
		return nil, errMetadataForbidden
	}
	if !o.editor.validCSRF(identity.Login, req.CSRF, at) {
		return nil, errMetadataCSRF
	}
	return identity, nil
}

// mcpTargetIdentity repeats concrete tool admission on the actual request. The
// entry caller is copied by the operation, never accepted from a transport or
// retained session. expectedLogin is only an internal equality constraint.
func (o *SharedIconOperations) mcpTargetIdentity(r *http.Request, expectedLogin string) (*Identity, error) {
	if o == nil || o.editor == nil || o.editor.config == nil || o.cache == nil || o.now == nil {
		return nil, errMetadataStale
	}
	if checkMCPToolRequest(r, o.mcp, o.trusted) != nil {
		return nil, errMetadataForbidden
	}
	identity := IdentityFromContext(r.Context())
	if o.mode != serviceEditShared || !o.editor.config.Logins[identity.Login] ||
		(expectedLogin != "" && identity.Login != expectedLogin) {
		return nil, errMetadataForbidden
	}
	host, err := validatePortalPublicOrigin(o.origin)
	if err != nil || host != o.host || o.mcp.Origin != o.origin || o.mcp.Host != o.host ||
		o.editor.config.Origin != o.origin || o.editor.config.Host != o.host || r.Host != o.host {
		return nil, errMetadataForbidden
	}
	if !validStateNamespace(o.binding.Namespace) ||
		(o.binding.ControlPlane != controlPlaneHeadscale && o.binding.ControlPlane != controlPlaneTailscale) ||
		o.editor.config.Binding != o.binding {
		return nil, errMetadataStale
	}
	copy := *identity
	return &copy, nil
}

// Caller holds editor.mu; legacy documents remain unbound. Pure model/binding
// checks do not open the dormant configured-v4 runtime gate or recanonicalize a
// compact document that a legitimate shrinking edit may bring under output caps.
func (o *SharedIconOperations) checkWriterLocked() error {
	if o.editor.disabled {
		return errMetadataUncertain
	}
	view := o.editor.view.Load()
	if view == nil || view.metadata == nil || len(view.bytes) == 0 || view.revision == "" || o.editor.config == nil ||
		o.editor.config.Binding != o.binding || (o.binding.ControlPlane != controlPlaneHeadscale && o.binding.ControlPlane != controlPlaneTailscale) ||
		(o.binding.Namespace != "" && !validStateNamespace(o.binding.Namespace)) {
		return errMetadataStale
	}
	if validateServiceMetadataDocumentModel(view.metadata.document) != nil || validateServiceMetadataBinding(view.metadata.document, o.binding) != nil {
		return errMetadataStale
	}
	return nil
}

// Caller holds editor.mu. Selection uses a newly loaded snapshot and a clock
// sampled after lock waits and immediately before the freshness decision.
func (o *SharedIconOperations) visibleNPMLocked(identity *Identity, id int, expectedTargetRevision string) (ProxyHost, error) {
	data := o.editor.requestData(o.cache.Get())
	now := o.now()
	if data == nil || data.Policy == nil || o.interval <= 0 || o.interval > time.Duration(1<<63-1)/3 ||
		data.UpdatedAt.IsZero() || data.UpdatedAt.After(now) || now.Sub(data.UpdatedAt) > 3*o.interval ||
		(o.binding.ControlPlane != controlPlaneHeadscale && o.binding.ControlPlane != controlPlaneTailscale) || data.ControlPlane.Provider != o.binding.ControlPlane {
		return ProxyHost{}, errMetadataStale
	}
	var selected *ProxyHost
	for _, match := range evaluateServices(identity, data) {
		if match.ProxyHost.ID == id && id > 0 {
			if selected != nil {
				return ProxyHost{}, errMetadataNotVisible
			}
			copy := match.ProxyHost
			selected = &copy
		}
	}
	if selected == nil {
		return ProxyHost{}, errMetadataNotVisible
	}
	if expectedTargetRevision != "" && !hmac.Equal([]byte(expectedTargetRevision), []byte(o.editor.targetRevision(*selected))) {
		return ProxyHost{}, errMetadataConflict
	}
	return *selected, nil
}

func (o *SharedIconOperations) GetBrowserNPM(r *http.Request, id int) (serviceMetadataEditForm, error) {
	fail := func(err error) (serviceMetadataEditForm, error) { return serviceMetadataEditForm{}, err }
	identity, err := o.browserNPMIdentity(r, id)
	if err != nil {
		return fail(err)
	}
	login := identity.Login
	if _, err := o.checkBrowserNPMRead(r, id, login, o.now()); err != nil {
		return fail(err)
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	identity, err = o.checkBrowserNPMRead(r, id, login, o.now())
	if err != nil {
		return fail(err)
	}
	if err := o.checkWriterLocked(); err != nil {
		return fail(err)
	}
	host, err := o.visibleNPMLocked(identity, id, "")
	if err != nil {
		return fail(err)
	}
	target := o.editor.targetRevision(host)
	if err := o.editor.checkDisk(o.editor.view.Load()); err != nil {
		return fail(err)
	}
	identity, err = o.checkBrowserNPMRead(r, id, login, o.now())
	if err != nil {
		return fail(err)
	}
	if err := o.checkWriterLocked(); err != nil {
		return fail(err)
	}
	host, err = o.visibleNPMLocked(identity, id, target)
	if err != nil {
		return fail(err)
	}
	view := o.editor.view.Load()
	override := view.metadata.Overrides[id]
	defaults, _ := resolveServiceCard(host, nil)
	scope := sha256.Sum256([]byte(login))
	return serviceMetadataEditForm{
		ID:       id,
		Fields:   serviceMetadataEditableFields{Name: override.Name, URL: override.URL, Icon: override.Icon},
		Defaults: serviceMetadataEditableFields{Name: defaults.Name, URL: defaults.URL, Icon: "generic"},
		Icons:    append([]string(nil), serviceIconIDs...),
		Revision: view.revision, TargetRevision: target,
		CSRF: o.editor.csrf(login, o.now()), IdentityScope: hex.EncodeToString(scope[:]),
	}, nil
}

func (o *SharedIconOperations) SaveBrowserNPM(r *http.Request, id int, req serviceMetadataEditRequest) error {
	identity, err := o.browserNPMIdentity(r, id)
	if err != nil {
		return err
	}
	login := identity.Login
	if req.Action != "save" && req.Action != "reset" || req.Action == "reset" && (req.Name != "" || req.URL != "" || req.Icon != "") {
		return errMetadataInvalidFields
	}
	for _, value := range []string{req.Revision, req.TargetRevision, req.CSRF} {
		if value == "" || len(value) > 256 {
			return errMetadataInvalidFields
		}
	}
	if _, err := o.checkBrowserNPMSave(r, id, req, login, o.now()); err != nil {
		return err
	}
	o.editor.mu.Lock()
	defer o.editor.mu.Unlock()
	identity, err = o.checkBrowserNPMSave(r, id, req, login, o.now())
	if err != nil {
		return err
	}
	if err := o.checkWriterLocked(); err != nil {
		return err
	}
	if _, err := o.visibleNPMLocked(identity, id, req.TargetRevision); err != nil {
		return err
	}
	document, err := mutateServiceMetadata(o.editor.view.Load().metadata.document, id, req.Name, req.URL, req.Icon, req.Action == "reset")
	if err != nil {
		return errMetadataInvalidFields
	}
	// The sole production final-check closure belongs to the concrete operation,
	// never its transport. Repeat save authority, not whichever method remains.
	return o.editor.commit(req.Revision, document, serviceMetadataWriteNPM, func() error {
		identity, err := o.checkBrowserNPMSave(r, id, req, login, o.now())
		if err != nil {
			return err
		}
		if err := o.checkWriterLocked(); err != nil {
			return err
		}
		_, err = o.visibleNPMLocked(identity, id, req.TargetRevision)
		return err
	})
}
