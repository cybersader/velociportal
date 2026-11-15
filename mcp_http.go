package main

import (
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	mcpProtocolVersion  = "2025-11-25"
	mcpMaxRequestBytes  = 16 * 1024
	mcpMaxResponseBytes = 256 * 1024
	mcpMaxJSONDepth     = 16
	mcpRequestTimeout   = 10 * time.Second
)

type mcpHandler struct {
	config MCPConfig
	slots  chan struct{}
	tools  *mcpApplicationTools
}

func mountMCP(mux *http.ServeMux, config *MCPConfig, trusted *net.IPNet, tools *mcpApplicationTools) {
	if config != nil {
		mux.Handle("/mcp", newMCPHandler(*config, trusted, tools))
	}
}

func newMCPHandler(config MCPConfig, trusted *net.IPNet, tools *mcpApplicationTools) http.Handler {
	h := &mcpHandler{config: config, slots: make(chan struct{}, 8), tools: tools}
	protected := IdentityMiddleware(trusted, h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setIdentityResponseCacheHeaders(w.Header())
		// Rejecting a body before consuming it must not let net/http's implicit
		// drain escape the route deadline after this handler returns.
		if r.Body != nil && r.Body != http.NoBody {
			w.Header().Set("Connection", "close")
		}
		controller := http.NewResponseController(w)
		originalBody := r.Body
		defer func() {
			// Connection: close skips the pre-response drain, but net/http still
			// closes (and may drain) its original body after ServeHTTP returns.
			// Finish that cleanup under a deadline, before clearing either one.
			if originalBody != nil && originalBody != http.NoBody {
				if w.Header().Get("Connection") == "close" {
					_ = controller.SetReadDeadline(time.Now())
				}
				_ = originalBody.Close()
			}
			// Flush small buffered responses while the write deadline is active.
			_ = controller.Flush()
			_ = controller.SetWriteDeadline(time.Time{})
			_ = controller.SetReadDeadline(time.Time{})
		}()
		deadline := time.Now().Add(mcpRequestTimeout)
		if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, "Unavailable", http.StatusServiceUnavailable)
			return
		}
		if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			http.Error(w, "Unavailable", http.StatusServiceUnavailable)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func (h *mcpHandler) admittedIdentity(r *http.Request) bool {
	return mcpAdmittedIdentity(r, h.config)
}

func mcpAdmittedIdentity(r *http.Request, config MCPConfig) bool {
	identity := IdentityFromContext(r.Context())
	if identity == nil || !validPersonalLogin(identity.Login) || strings.ContainsAny(identity.Login, ",;<>\"\\[]") || r.Host != config.Host {
		return false
	}
	for _, key := range []string{"Tailscale-User-Login", "Tailscale-User-Name", "Tailscale-User-Profile-Pic"} {
		value, single, present := singleRawHeader(r.Header, key)
		if present && (!single || value == "" || containsControl(value)) || key == "Tailscale-User-Login" && (!single || value != identity.Login) {
			return false
		}
	}
	// These are not alternative authority/session channels, even if empty.
	for _, key := range []string{"Authorization", "Cookie", "Mcp-Session-Id", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		if _, _, present := singleRawHeader(r.Header, key); present {
			return false
		}
	}
	origin, single, originPresent := singleRawHeader(r.Header, "Origin")
	if originPresent && (!single || origin != config.Origin) {
		return false
	}
	// Native requests may omit all four headers. Supplied Fetch Metadata must
	// describe same-origin JSON, never a navigation or cross-site request.
	for key, allowed := range map[string][]string{
		"Sec-Fetch-Site": {"same-origin"},
		"Sec-Fetch-Mode": {"cors", "same-origin"},
		"Sec-Fetch-Dest": {"empty"},
	} {
		value, single, present := singleRawHeader(r.Header, key)
		if !present {
			continue
		}
		if !originPresent || !single || !mcpContains(allowed, value) {
			return false
		}
	}
	if _, _, present := singleRawHeader(r.Header, "Sec-Fetch-User"); present {
		return false
	}
	return true
}

func mcpContains(values []string, value string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (h *mcpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setIdentityResponseCacheHeaders(w.Header())
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fail := func(status int) { http.Error(w, http.StatusText(status), status) }
	if !h.admittedIdentity(r) {
		fail(http.StatusForbidden)
		return
	}
	if r.URL.Path != "/mcp" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		fail(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(http.StatusMethodNotAllowed)
		return
	}
	if !mcpJSONContentType(r.Header) {
		fail(http.StatusUnsupportedMediaType)
		return
	}
	if !mcpAccept(r.Header) {
		fail(http.StatusNotAcceptable)
		return
	}
	version, single, present := singleRawHeader(r.Header, "MCP-Protocol-Version")
	if present && (!single || version != mcpProtocolVersion) {
		fail(http.StatusBadRequest)
		return
	}
	if !mcpRequestFraming(r) {
		fail(http.StatusBadRequest)
		return
	}
	if r.ContentLength > mcpMaxRequestBytes {
		fail(http.StatusRequestEntityTooLarge)
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		fail(http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mcpMaxRequestBytes)
	defer r.Body.Close()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		var timeout net.Error
		switch {
		case errors.As(err, &maxErr):
			fail(http.StatusRequestEntityTooLarge)
		case errors.As(err, &timeout) && timeout.Timeout():
			fail(http.StatusRequestTimeout)
		default:
			fail(http.StatusBadRequest)
		}
		return
	}
	w.Header().Del("Connection") // Complete bounded body consumed; keepalive is safe.
	h.dispatch(w, r, body, present)
}

// checkMCPToolRequest repeats concrete request admission at an operation checkpoint.
// It conveys no retained permission or identity receipt. Operations must also pin
// their entry login and check their own current configuration and target authority.
// Lifecycle negotiation is separate: tool operations always need an explicit
// supported version. The body has already been bounded and consumed by the route;
// this check neither reads it again nor changes slots, deadlines or cleanup.
func checkMCPToolRequest(r *http.Request, config *MCPConfig, trusted *net.IPNet) error {
	denied := errors.New("forbidden")
	if r == nil || r.URL == nil || config == nil || trusted == nil || r.Context() == nil || r.Context().Err() != nil {
		return denied
	}
	host, err := validatePortalPublicOrigin(config.Origin)
	if err != nil || host != config.Host {
		return denied
	}
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		source = r.RemoteAddr
	}
	if percent := strings.LastIndexByte(source, '%'); percent >= 0 {
		source = source[:percent]
	}
	ip := net.ParseIP(source)
	if ip == nil || !trusted.Contains(ip) || !mcpAdmittedIdentity(r, *config) {
		return denied
	}
	if r.Method != http.MethodPost || r.URL.Path != "/mcp" || r.URL.RawPath != "" || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return denied
	}
	version, single, present := singleRawHeader(r.Header, "MCP-Protocol-Version")
	if !present || !single || version != mcpProtocolVersion || !mcpJSONContentType(r.Header) || !mcpAccept(r.Header) || !mcpRequestFraming(r) || r.ContentLength > mcpMaxRequestBytes {
		return denied
	}
	return nil
}

func mcpRequestFraming(r *http.Request) bool {
	// net/http validates wire framing before dispatch. Allow its one supported
	// streaming framing, but never encoding, trailers, or ambiguous raw fields.
	for _, key := range []string{"Content-Encoding", "Trailer", "Transfer-Encoding"} {
		if _, _, exists := singleRawHeader(r.Header, key); exists {
			return false
		}
	}
	if len(r.Trailer) != 0 || len(r.TransferEncoding) > 1 || len(r.TransferEncoding) == 1 && (r.TransferEncoding[0] != "chunked" || r.ContentLength != -1) {
		return false
	}
	if value, one, exists := singleRawHeader(r.Header, "Content-Length"); exists && (!one || value == "" || strings.Contains(value, ",")) {
		return false
	}
	return true
}

func mcpJSONContentType(header http.Header) bool {
	value, single, _ := singleRawHeader(header, "Content-Type")
	if !single {
		return false
	}
	media, params, err := mime.ParseMediaType(value)
	if err != nil || media != "application/json" || len(params) > 1 {
		return false
	}
	for key, value := range params {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func mcpAccept(header http.Header) bool {
	value, single, _ := singleRawHeader(header, "Accept")
	if !single || len(value) > 1024 {
		return false
	}
	// MCP clients must advertise both response types, even though this prototype
	// always chooses JSON and never creates an event stream.
	found := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		media, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || len(params) != 0 || found[media] {
			return false
		}
		found[media] = true
	}
	return found["application/json"] && found["text/event-stream"]
}
