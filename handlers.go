package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PortalHandler struct {
	cache              *Cache
	health             *ServiceHealthStore
	logoDefaultVisible bool
}

func NewPortalHandler(cache *Cache) *PortalHandler {
	return NewPortalHandlerWithHealth(cache, nil)
}

func NewPortalHandlerWithHealth(cache *Cache, health *ServiceHealthStore) *PortalHandler {
	return NewPortalHandlerWithOptions(cache, health, true)
}

// NewPortalHandlerWithOptions is the options-aware constructor used when the
// deployment default for the browser-local logo preference is not simply
// "visible". Existing constructors keep the historical visible-default behavior
// for tests and callers that do not need to override it.
func NewPortalHandlerWithOptions(cache *Cache, health *ServiceHealthStore, logoDefaultVisible bool) *PortalHandler {
	return &PortalHandler{cache: cache, health: health, logoDefaultVisible: logoDefaultVisible}
}

func (h *PortalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setIdentityResponseCacheHeaders(w.Header())
	identity := IdentityFromContext(r.Context())
	if identity == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	data := h.cache.Get()
	if data == nil {
		http.Error(w, "portal unavailable", http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	cards := MatchServices(identity, data)
	machinesAvailable := machineProjectionAvailable(data)
	machines := MatchMachines(identity, data)
	consoleEligible := machineConsoleEligible(identity.Login, data)
	consoleCapableByID := make(map[string]bool)
	if consoleEligible {
		for _, machine := range machines {
			if machineSSHCapable(machine.ID, data) {
				consoleCapableByID[machine.ID] = true
			}
		}
	}
	slog.Info("portal request", "cards", len(cards), "machines", len(machines))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	opts := portalRenderOptions{
		Machines:                  machines,
		MachinesAvailable:         machinesAvailable,
		ConsoleEligible:           consoleEligible,
		MachineConsoleCapableByID: consoleCapableByID,
		Organized:                 serviceMetadataHasOrganization(data.ServiceMetadata),
		LogoDefaultVisible:        h.logoDefaultVisible,
		Health:                    h.health,
	}
	if err := renderPortalWithOptions(w, identity, cards, opts); err != nil {
		slog.Error("render portal", "err", err)
	}
	slog.Debug("portal rendered", "duration", time.Since(start))
}

// portalRenderOptions carries the request-scoped rendering inputs beyond the
// identity and service cards. It exists so renderPortal's smaller-arity legacy
// wrappers can keep working with sensible defaults (no machines section, the
// visible logo default, no browser SSH Console action) while ServeHTTP supplies
// the complete set derived from the current snapshot and viewer.
type portalRenderOptions struct {
	Machines                  []MachineCard
	MachinesAvailable         bool
	ConsoleEligible           bool
	MachineConsoleCapableByID map[string]bool
	Organized                 bool
	LogoDefaultVisible        bool
	Health                    *ServiceHealthStore
}

func renderPortal(w io.Writer, id *Identity, cards []ServiceCard, healthStores ...*ServiceHealthStore) error {
	return renderPortalWithOrganization(w, id, cards, serviceCardsHaveOrganizationMetadata(cards), healthStores...)
}

func renderPortalWithOrganization(w io.Writer, id *Identity, cards []ServiceCard, organized bool, healthStores ...*ServiceHealthStore) error {
	var health *ServiceHealthStore
	if len(healthStores) > 0 {
		health = healthStores[0]
	}
	return renderPortalWithOptions(w, id, cards, portalRenderOptions{
		Organized:          organized,
		LogoDefaultVisible: true,
		Health:             health,
	})
}

func renderPortalWithOptions(w io.Writer, id *Identity, cards []ServiceCard, opts portalRenderOptions) error {
	health := opts.Health
	machines := opts.Machines
	machinesAvailable := opts.MachinesAvailable
	organized := opts.Organized

	var servicesBody strings.Builder
	servicesClass := "grid"
	if organized {
		servicesClass = "services-organized"
		renderServiceSections(&servicesBody, cards, health)
	} else {
		for _, card := range cards {
			renderServiceCard(&servicesBody, card, health)
		}
	}

	if len(cards) == 0 {
		servicesBody.WriteString(`<div class="empty">` +
			`<div class="empty-icon" aria-hidden="true">&#9671;</div>` +
			`<p>No services are available to your account.</p>` +
			`</div>`)
	}

	var machinesSection strings.Builder
	if machinesAvailable {
		var machinesBody strings.Builder
		for index, machine := range machines {
			showConsole := opts.ConsoleEligible && opts.MachineConsoleCapableByID[machine.ID]
			renderMachineCard(&machinesBody, machine, index+1, showConsole)
		}
		if len(machines) == 0 {
			machinesBody.WriteString(`<div class="empty machine-empty">` +
				`<div class="empty-icon" aria-hidden="true">&#9671;</div>` +
				`<p>No machines are available from the supported SSH policy view.</p>` +
				`</div>`)
		}
		fmt.Fprintf(&machinesSection,
			`<section class="portal-section" aria-labelledby="machines-heading">`+
				`<h2 class="section-title" id="machines-heading">Machines</h2>`+
				`<p class="machines-help">Only devices that currently report Tailscale SSH enabled and match your SSH policy plus TCP/22 Grant are shown. Accounts and reachability are not verified. Tailscale Machines opens the admin console, not a session.</p>`+
				`<div class="machine-list" id="machines">%s</div>`+
				`</section>`,
			machinesBody.String(),
		)
	}

	servicesHealthHelp := renderServiceHealthHelp(health != nil, anyCardUnlinked(cards))

	displayName := strings.TrimSpace(id.Name)
	if displayName == "" {
		displayName = id.Login
	}

	// The deployment default maps to one of exactly two fixed HTML/JS literals.
	// Raw PORTAL_LOGO_DEFAULT environment text is never interpolated into the page.
	logoDefaultLiteral := "true"
	if !opts.LogoDefaultVisible {
		logoDefaultLiteral = "false"
	}

	page := strings.NewReplacer(
		"{{USER_NAME}}", html.EscapeString(displayName),
		"{{USER_LOGIN}}", html.EscapeString(id.Login),
		"{{SERVICES_CLASS}}", servicesClass,
		"{{SERVICES_BODY}}", servicesBody.String(),
		"{{SERVICES_HEALTH_HELP}}", servicesHealthHelp,
		"{{MACHINES_SECTION}}", machinesSection.String(),
		"{{BOTTOM_NAV}}", renderBottomNav(machinesAvailable),
		"{{LOGO_PREF_SCOPE}}", logoPreferenceScope(id.Login),
		"{{LOGO_DEFAULT_VISIBLE}}", logoDefaultLiteral,
	).Replace(portalPage)

	if _, err := io.WriteString(w, page); err != nil {
		return fmt.Errorf("renderPortal: %w", err)
	}
	return nil
}

// Bottom-nav icons are small embedded SVG line icons (no icon library, no
// remote fonts). They are decorative only: aria-hidden and non-focusable
// (focusable="false" covers legacy browsers that otherwise tab into SVG).
// The visible, screen-reader-exposed label is always the adjacent text span.
const (
	bottomNavIconServices = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="3" y="3" width="7" height="7"></rect><rect x="14" y="3" width="7" height="7"></rect><rect x="3" y="14" width="7" height="7"></rect><rect x="14" y="14" width="7" height="7"></rect></svg>`
	bottomNavIconMachines = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><rect x="3" y="4" width="18" height="12" rx="2"></rect><line x1="8" y1="20" x2="16" y2="20"></line><line x1="12" y1="16" x2="12" y2="20"></line></svg>`
	bottomNavIconSearch   = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="11" cy="11" r="7"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>`
	bottomNavIconMore     = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false"><circle cx="5" cy="12" r="1.5"></circle><circle cx="12" cy="12" r="1.5"></circle><circle cx="19" cy="12" r="1.5"></circle></svg>`
)

func renderBottomNav(machinesAvailable bool) string {
	machinesHidden := ""
	if !machinesAvailable {
		machinesHidden = " hidden"
	}
	// Services starts as the current location: the page always loads scrolled
	// to the top, so Services is the section in view before any JS section
	// tracking runs. JS updates aria-current on scroll/click; Search and More
	// are actions, not section links, so they never carry aria-current.
	return `<nav class="bottom-nav" id="bottom-nav" aria-label="Portal navigation">` +
		`<a class="bottom-nav-item" id="bottom-nav-services" href="#services-heading" data-bottom-nav-scroll="services-heading" aria-current="location"><span class="bottom-nav-icon" aria-hidden="true">` + bottomNavIconServices + `</span><span class="bottom-nav-label">Services</span></a>` +
		`<a class="bottom-nav-item" id="bottom-nav-machines" href="#machines-heading" data-bottom-nav-scroll="machines-heading"` + machinesHidden + `><span class="bottom-nav-icon" aria-hidden="true">` + bottomNavIconMachines + `</span><span class="bottom-nav-label">Machines</span></a>` +
		`<button type="button" class="bottom-nav-item" id="bottom-nav-search"><span class="bottom-nav-icon" aria-hidden="true">` + bottomNavIconSearch + `</span><span class="bottom-nav-label">Search</span></button>` +
		`<button type="button" class="bottom-nav-item" id="bottom-nav-more" aria-haspopup="dialog" aria-controls="account-panel" aria-expanded="false"><span class="bottom-nav-icon" aria-hidden="true">` + bottomNavIconMore + `</span><span class="bottom-nav-label">More</span></button>` +
		`</nav>`
}

// logoPreferenceScope derives the opaque, fixed-format SHA-256 hex scope used to
// namespace the browser-local logo preference by exact trusted login. The
// plaintext login is never written into the storage-key namespace or otherwise
// rendered by this scope.
func logoPreferenceScope(login string) string {
	sum := sha256.Sum256([]byte(login))
	return hex.EncodeToString(sum[:])
}

func serviceCardsHaveOrganizationMetadata(cards []ServiceCard) bool {
	for _, card := range cards {
		if card.Category != "" || card.Order != nil {
			return true
		}
	}
	return false
}

func renderServiceSections(body *strings.Builder, cards []ServiceCard, health *ServiceHealthStore) {
	for sectionIndex, start := 0, 0; start < len(cards); sectionIndex++ {
		category := cards[start].Category
		end := start + 1
		for end < len(cards) && cards[end].Category == category {
			end++
		}

		label := category
		if label == "" {
			label = "Uncategorized"
		}
		headingID := fmt.Sprintf("service-category-%d", sectionIndex+1)
		fmt.Fprintf(body,
			`<section class="service-category" aria-labelledby="%s">`+
				`<h2 class="service-category-title" id="%s">%s</h2>`+
				`<div class="grid">`,
			headingID,
			headingID,
			html.EscapeString(label),
		)
		for _, card := range cards[start:end] {
			renderServiceCard(body, card, health)
		}
		body.WriteString(`</div></section>`)
		start = end
	}
}

func renderServiceCard(body *strings.Builder, card ServiceCard, health *ServiceHealthStore) {
	healthMarkup := renderServiceHealthStatus(health, card.ID)
	scheme, linkable := cardURLScheme(card.URL)
	if card.LinkState != serviceLinkReady {
		linkable = false
	}
	if linkable {
		fmt.Fprintf(body,
			`<a class="card" href="%s" data-service="%s">`+
				`<span class="card-head"><span class="card-name">%s</span></span>`+
				`<span class="card-meta"><span class="badge">%s</span>%s</span>`+
				`</a>`,
			html.EscapeString(card.URL),
			html.EscapeString(card.Name),
			html.EscapeString(card.Name),
			html.EscapeString(scheme),
			healthMarkup,
		)
		return
	}

	if card.LinkState == serviceLinkReady {
		slog.Warn("rendering card with invalid browser URL as unlinked", "proxy_host_id", card.ID)
	}
	fmt.Fprintf(body,
		`<article class="card card-unlinked" data-service="%s">`+
			`<span class="card-head"><span class="card-name">%s</span></span>`+
			`<span class="card-meta"><span class="badge">link needed</span>%s</span>`+
			`</article>`,
		html.EscapeString(card.Name),
		html.EscapeString(card.Name),
		healthMarkup,
	)
}

func renderMachineCard(body *strings.Builder, machine MachineCard, index int, consoleEligible bool) {
	machineID := fmt.Sprintf("machine-%d", index)
	selectID := machineID + "-user"
	accountID := machineID + "-account"
	accountPanelID := machineID + "-custom-account"
	feedbackID := machineID + "-copy-feedback"
	shortName, fullTarget, hasFullTarget := machineCardNames(machine.Target)
	consoleURL, showConsole := "", false
	if consoleEligible {
		consoleURL, showConsole = machineConsoleURL(machine.Target)
	}

	literalAccess := make([]MachineAccess, 0, len(machine.Access))
	hasNonroot := false
	for _, access := range machine.Access {
		if access.User == machineNonrootSelector {
			hasNonroot = true
			continue
		}
		if _, ok := machineSSHCommand(access.User, machine.Target); ok {
			literalAccess = append(literalAccess, access)
		}
	}
	mixedAccounts := hasNonroot && len(literalAccess) > 0

	fmt.Fprintf(body,
		`<article class="machine-row" data-machine="%s" aria-labelledby="%s-name">`+
			`<div class="machine-identity"><h3 class="machine-name" id="%s-name">%s</h3>`,
		html.EscapeString(machine.Target),
		machineID,
		machineID,
		html.EscapeString(shortName),
	)
	if hasFullTarget {
		fmt.Fprintf(body, `<p class="machine-target">%s</p>`, html.EscapeString(fullTarget))
	}
	body.WriteString(`</div><div class="machine-connect">`)

	if len(literalAccess) > 0 {
		fmt.Fprintf(body, `<label class="machine-field-label" for="%s">SSH as</label>`, selectID)
		if mixedAccounts {
			fmt.Fprintf(body, `<select id="%s" data-machine-account-select aria-controls="%s" aria-expanded="false">`, selectID, accountPanelID)
		} else {
			fmt.Fprintf(body, `<select id="%s" data-machine-account-select>`, selectID)
		}
		for _, access := range literalAccess {
			command, _ := machineSSHCommand(access.User, machine.Target)
			fmt.Fprintf(body,
				`<option value="%s" data-command="%s">%s</option>`,
				html.EscapeString(access.User),
				html.EscapeString(command),
				html.EscapeString(access.User),
			)
		}
		if mixedAccounts {
			body.WriteString(`<option value="" data-custom-account="true">Other non-root account&hellip;</option>`)
		}
		body.WriteString(`</select>`)
	}

	if hasNonroot {
		hidden := ""
		accountLabel := "SSH as"
		if mixedAccounts {
			hidden = " hidden"
			accountLabel = "Non-root account"
		}
		fmt.Fprintf(body,
			`<div class="machine-custom-account" id="%s"%s>`+
				`<label class="machine-field-label" for="%s">%s</label>`+
				`<input type="text" id="%s" class="machine-account-input" list="ssh-account-suggestions" autocomplete="off" spellcheck="false" maxlength="256" placeholder="Account name" data-account-target="%s">`+
				`</div>`,
			accountPanelID,
			hidden,
			accountID,
			accountLabel,
			accountID,
			html.EscapeString(machine.Target),
		)
	}
	actionsClass := "machine-actions"
	if showConsole {
		actionsClass += " machine-actions-with-console"
	}
	fmt.Fprintf(body, `</div><div class="%s">`, actionsClass)

	if len(literalAccess) > 0 || hasNonroot {
		fmt.Fprintf(body, `<button type="button" class="copy-command machine-copy" data-copy-machine-ssh aria-describedby="%s"`, feedbackID)
		if len(literalAccess) > 0 {
			fmt.Fprintf(body, ` data-user-select="%s"`, selectID)
		}
		if hasNonroot {
			fmt.Fprintf(body, ` data-account-input="%s"`, accountID)
		}
		body.WriteString(`>Copy Tailscale SSH</button>`)
	}
	if showConsole {
		fmt.Fprintf(body,
			`<a class="btn-console" href="%s" target="_blank" rel="noopener noreferrer" aria-label="Open %s in Tailscale Machines">Tailscale Machines</a>`,
			html.EscapeString(consoleURL),
			html.EscapeString(shortName),
		)
	}
	fmt.Fprintf(body, `<span class="copy-feedback machine-feedback" id="%s" role="status" aria-live="polite"></span></div>`, feedbackID)

	body.WriteString(`<details class="machine-policy-details"><summary>Policy details</summary><div class="machine-policy-body">` +
		`<p class="machine-policy-evidence">Visible because this device reports Tailscale SSH enabled, SSH policy matches, and a network Grant permits TCP port 22.</p>` +
		`<ul class="machine-policy-accounts" aria-label="Accounts allowed by policy">`)
	for _, access := range machine.Access {
		account := access.User
		if account == machineNonrootSelector {
			account = "Any non-root account"
		}
		fmt.Fprintf(body,
			`<li><span class="machine-account-name">%s</span><span class="machine-account-detail">%s</span></li>`,
			html.EscapeString(account),
			html.EscapeString(machineActionLabel(access)),
		)
	}
	body.WriteString(`</ul><p class="machine-policy-note">Local account existence and machine health are not verified.</p></div></details></article>`)
}

// machineCardNames returns the short, familiar name to render prominently on a
// machine card and, only for a canonical *.ts.net target, the full canonical
// target to render alongside it in smaller muted text. It is derived solely
// from machineShortName's already validated canonical target; a validated IP
// fallback target has no separate short form and is shown once.
func machineCardNames(target string) (short string, full string, hasFull bool) {
	if name, ok := machineShortName(target); ok {
		return name, target, true
	}
	return target, "", false
}

// machineActionLabel renders MachineAccess.Action and CheckPeriod as truthful,
// plain-language SSH policy text. It reads only the existing canonical Action
// and CheckPeriod values and changes no normalization or precedence.
func machineActionLabel(access MachineAccess) string {
	switch access.Action {
	case "accept":
		return "No extra sign-in"
	case "check":
		return "Reauthenticate every " + machineCheckPeriodLabel(access.CheckPeriod)
	default:
		return "SSH policy action unavailable"
	}
}

// machineCheckPeriodLabel formats a canonical checkPeriod duration -- always a
// whole number of minutes or hours -- as a short human label, falling back to
// Tailscale's documented 12-hour default when the optional checkPeriod is unset.
func machineCheckPeriodLabel(period time.Duration) string {
	if period <= 0 {
		return "12h (default)"
	}
	if period%time.Hour == 0 {
		return fmt.Sprintf("%dh", int64(period/time.Hour))
	}
	return fmt.Sprintf("%dm", int64(period/time.Minute))
}

func machineSSHCommand(user, target string) (string, bool) {
	if user == machineNonrootSelector || !supportedSSHUser(user) {
		return "", false
	}
	name, validName := normalizeMachineTargetName(target)
	address, validAddress := tailscaleMachineAddress(target)
	if (!validName || name != target) && (!validAddress || address != target) {
		return "", false
	}
	argument := user + "@" + target
	if strings.HasSuffix(user, "$") {
		argument = strings.TrimSuffix(user, "$") + `\$@` + target
	}
	return "tailscale ssh " + argument, true
}

func renderServiceHealthStatus(store *ServiceHealthStore, proxyHostID int) string {
	if store == nil {
		return ""
	}
	result, ok := store.Get(proxyHostID)
	if !ok {
		return ""
	}

	// Labels describe the scope of the configured backend probe only -- never
	// a claim about what a real browser session will see. renderServiceHealthHelp
	// spells out TCP-vs-HTTP and 401/403 semantics for viewers who want detail.
	// The enum, its ServiceHealthState* constants, and the CSS class names below
	// are unchanged; only the human-facing wording changed.
	label := "check unknown"
	className := "unknown"
	switch result.State {
	case ServiceHealthStateReachable:
		label = "backend check passed"
		className = "reachable"
	case ServiceHealthStateAuthRequired:
		label = "backend denied"
		className = "auth-required"
	case ServiceHealthStateResponseError:
		label = "unexpected response"
		className = "response-error"
	case ServiceHealthStateUnreachable:
		label = "check failed"
		className = "unreachable"
	case ServiceHealthStateStale:
		label = "check stale"
		className = "stale"
	}
	return fmt.Sprintf(
		`<span class="health-status health-%s" aria-label="Backend service check: %s">%s</span>`,
		className,
		html.EscapeString(label),
		html.EscapeString(label),
	)
}

// anyCardUnlinked reports whether at least one card would render in the
// unlinked ("link needed") state -- mirroring the same linkability check
// renderServiceCard applies -- so the shared help disclosure can include
// link/wildcard guidance independently of whether health is configured.
func anyCardUnlinked(cards []ServiceCard) bool {
	for _, card := range cards {
		_, linkable := cardURLScheme(card.URL)
		if card.LinkState != serviceLinkReady {
			linkable = false
		}
		if !linkable {
			return true
		}
	}
	return false
}

// renderServiceHealthHelp renders a single, touch/keyboard-accessible native
// disclosure explaining what a backend check does and does not establish and,
// independently, what an unlinked ("link needed") card means. It is rendered
// once for the Services section (not per card) and reuses no new probe data
// -- it only clarifies the existing ServiceHealthState contract already
// exposed by renderServiceHealthStatus and the existing card-linking
// behavior already exposed by cardURLScheme/LinkState. It renders whenever
// either applies, independently of health being configured, so a portal with
// health checks disabled -- or with JavaScript disabled -- never loses the
// link/wildcard explanation.
func renderServiceHealthHelp(hasHealth, hasUnlinkedCards bool) string {
	if !hasHealth && !hasUnlinkedCards {
		return ""
	}

	var title string
	switch {
	case hasHealth && hasUnlinkedCards:
		title = "About links and backend checks"
	case hasHealth:
		title = "What does a service check mean?"
	default:
		title = `What does "link needed" mean?`
	}

	var body strings.Builder
	if hasHealth {
		body.WriteString(`<p>Each label describes a backend check run from this server, not what your browser will see. ` +
			`A TCP check only confirms a connection could be opened; it says nothing about the page behind it. ` +
			`An HTTP check accepts a configured range of status codes as a pass, but an accepted status does not prove a usable page, ` +
			`and a denial (401/403) is always treated as a denial regardless of that range.</p>` +
			`<p>Browser DNS, proxy rules, application logins, and existing sessions can still block access even after a passed check, ` +
			`and a denial is not assurance that signing in will resolve it. "Check unknown" means no usable recent observation exists, ` +
			`not necessarily that a check was never attempted.</p>`)
	}
	if hasUnlinkedCards {
		body.WriteString(`<p>A card marked "link needed" has no concrete browser URL Velociportal can open directly -- often because ` +
			`the matched destination is a wildcard hostname. It still confirms the destination is authorized; adding a concrete ` +
			`service URL in Velociportal metadata is what turns it into a clickable link.</p>`)
	}

	return `<details class="service-health-help">` +
		`<summary>` + title + `</summary>` +
		`<div class="service-health-help-body">` +
		body.String() +
		`</div>` +
		`</details>`
}

func cardURLScheme(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.Hostname() == "" || strings.Contains(parsed.Hostname(), "*") {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.Scheme, true
}

const portalPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="theme-color" content="#ffffff" media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#0f1115" media="(prefers-color-scheme: dark)">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="Velociportal">
<meta name="apple-mobile-web-app-status-bar-style" content="default">
<link rel="manifest" href="/static/manifest.json">
<link rel="icon" type="image/svg+xml" href="/static/logo.svg">
<link rel="apple-touch-icon" href="/static/icons/apple-touch-icon-180.png">
<title>Velociportal</title>
<style>
:root {
  color-scheme: light dark;
  --bg: #0f1115;
  --text: #e6e6e6;
  --muted: #9aa4b2;
  --border: #232a35;
  --card-bg: #161a22;
  --card-hover-bg: #1a2029;
  --accent: #3b82f6;
  --badge-bg: #1f2733;
  --badge-text: #9aa4b2;
  --health-reachable: #3b82f6;
  --health-auth: #d29922;
  --health-response: #f0883e;
  --health-unreachable: #f85149;
  --health-stale: #8b949e;
  --health-unknown: #6e7681;
}
@media (prefers-color-scheme: light) {
  :root {
    --bg: #ffffff;
    --text: #1a1d21;
    --muted: #5a6472;
    --border: #e2e5ea;
    --card-bg: #ffffff;
    --card-hover-bg: #f6f8fc;
    --accent: #3b82f6;
    --badge-bg: #eef1f6;
    --badge-text: #5a6472;
    --health-reachable: #3b82f6;
    --health-auth: #9a6700;
    --health-response: #bc4c00;
    --health-unreachable: #cf222e;
    --health-stale: #57606a;
    --health-unknown: #6e7781;
  }
}
:root[data-theme="light"] {
  --bg: #ffffff;
  --text: #1a1d21;
  --muted: #5a6472;
  --border: #e2e5ea;
  --card-bg: #ffffff;
  --card-hover-bg: #f6f8fc;
  --accent: #3b82f6;
  --badge-bg: #eef1f6;
  --badge-text: #5a6472;
  --health-reachable: #3b82f6;
  --health-auth: #9a6700;
  --health-response: #bc4c00;
  --health-unreachable: #cf222e;
  --health-stale: #57606a;
  --health-unknown: #6e7781;
}
:root[data-theme="dark"] {
  --bg: #0f1115;
  --text: #e6e6e6;
  --muted: #9aa4b2;
  --border: #232a35;
  --card-bg: #161a22;
  --card-hover-bg: #1a2029;
  --accent: #3b82f6;
  --badge-bg: #1f2733;
  --badge-text: #9aa4b2;
  --health-reachable: #3b82f6;
  --health-auth: #d29922;
  --health-response: #f0883e;
  --health-unreachable: #f85149;
  --health-stale: #8b949e;
  --health-unknown: #6e7681;
}
* { box-sizing: border-box; }
body { margin: 0; font: 16px/1.5 system-ui, sans-serif; background: var(--bg); color: var(--text); }
header { max-width: 1200px; margin: 0 auto; padding: 2rem 1.5rem 1rem; display: flex; align-items: center; justify-content: space-between; gap: 1rem; flex-wrap: wrap; }
.brand { display: flex; align-items: center; gap: .6rem; min-width: 0; }
.brand-logo { width: 32px; height: 32px; flex-shrink: 0; }
:root[data-logo="hidden"] .brand-logo { display: none; }
.brand-name { font-size: 1.25rem; font-weight: 700; letter-spacing: -.01em; }
.header-side { display: flex; align-items: center; justify-content: flex-end; gap: 1rem; min-width: 0; }
.account { position: relative; min-width: 0; }
.account-trigger { display: block; border: 1px solid transparent; border-radius: 10px; padding: .3rem .5rem; margin: -.3rem -.5rem; background: none; text-align: right; cursor: pointer; font: inherit; color: inherit; max-width: 100%; }
.account-trigger:hover, .account-trigger:focus-visible { border-color: var(--border); background: var(--card-bg); }
.user { text-align: right; min-width: 0; }
.user-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.user .login { color: var(--muted); font-size: .85rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.account-panel { position: absolute; top: calc(100% + .5rem); right: 0; z-index: 20; width: max-content; min-width: 14rem; max-width: min(20rem, calc(100vw - 2rem)); padding: .9rem 1rem; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); box-shadow: 0 12px 30px rgba(0, 0, 0, .25); text-align: left; }
.account-panel-title { margin: 0 0 .6rem; font-size: 1rem; }
.account-panel-section + .account-panel-section { margin-top: .75rem; }
.account-panel-heading { margin: 0 0 .4rem; font-size: .8rem; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .02em; }
.account-panel-checkbox { display: flex; align-items: center; gap: .5rem; font-size: .9rem; cursor: pointer; }
main { max-width: 1200px; margin: 0 auto; padding: 1rem 1.5rem 2.5rem; }
.portal-content { display: grid; gap: 2.25rem; }
.portal-section { min-width: 0; }
.section-title { margin: 0 0 .85rem; font-size: 1.2rem; line-height: 1.3; }
.services-organized { display: grid; gap: 1.75rem; }
.service-category { min-width: 0; }
.service-category-title { margin: 0 0 .75rem; font-size: 1rem; line-height: 1.3; color: var(--muted); overflow-wrap: anywhere; }
.grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
.card { display: flex; flex-direction: column; gap: .55rem; padding: 1rem 1.1rem; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); color: inherit; text-decoration: none; transition: border-color .15s, transform .15s, background-color .15s; }
a.card:hover, a.card:focus-visible { border-color: var(--accent); background: var(--card-hover-bg); transform: translateY(-2px); }
.card-unlinked { color: var(--muted); }
.card-head { display: block; min-width: 0; }
.card-name { display: block; color: var(--text); font-weight: 600; line-height: 1.3; overflow-wrap: anywhere; }
.card-meta { display: flex; align-items: center; gap: .5rem; min-width: 0; margin-top: auto; flex-wrap: wrap; }
.badge { align-self: flex-start; padding: .1rem .5rem; border-radius: 999px; font-size: .72rem; font-weight: 600; letter-spacing: .02em; text-transform: uppercase; background: var(--badge-bg); color: var(--badge-text); }
.health-status { margin-left: auto; flex-shrink: 0; font-size: .72rem; font-weight: 650; line-height: 1.2; text-align: right; }
.health-reachable { color: var(--health-reachable); }
.health-auth-required { color: var(--health-auth); }
.health-response-error { color: var(--health-response); }
.health-unreachable { color: var(--health-unreachable); }
.health-stale { color: var(--health-stale); }
.health-unknown { color: var(--health-unknown); }
.service-health-help { margin: -.35rem 0 1rem; color: var(--muted); font-size: .82rem; }
.service-health-help summary { width: fit-content; cursor: pointer; color: var(--muted); font-weight: 600; }
.service-health-help summary:hover, .service-health-help summary:focus-visible { color: var(--text); }
.service-health-help-body { display: grid; gap: .5rem; margin-top: .5rem; max-width: 68ch; }
.service-health-help-body p { margin: 0; }
.card[hidden], .card-unlinked[hidden], .machine-row[hidden], .service-category[hidden] { display: none; }
.portal-search { margin: 0; }
.portal-search-label { display: block; margin-bottom: .35rem; font-size: .85rem; font-weight: 600; color: var(--muted); }
.portal-search-field { position: relative; display: flex; align-items: center; }
.portal-search-icon { position: absolute; left: .85rem; width: 1.1rem; height: 1.1rem; color: var(--muted); pointer-events: none; }
.portal-search-input { width: 100%; min-height: 2.9rem; padding: .6rem 3.35rem .6rem 2.6rem; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); color: var(--text); font: inherit; font-size: 1rem; }
.portal-search-input:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.portal-search-input::-webkit-search-cancel-button { display: none; }
.portal-search-clear { position: absolute; right: .15rem; display: inline-flex; align-items: center; justify-content: center; width: 2.75rem; height: 2.75rem; border: 0; border-radius: 8px; background: transparent; color: var(--muted); cursor: pointer; }
.portal-search-clear[hidden] { display: none; }
.portal-search-clear:hover, .portal-search-clear:focus-visible { color: var(--text); background: var(--card-hover-bg); }
.portal-search-clear svg { width: 1rem; height: 1rem; }
.portal-search-status { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
.portal-search-no-results { margin: .6rem 0 0; color: var(--muted); font-size: .85rem; }
.portal-search-no-results[hidden] { display: none; }
.machines-help { margin: 0 0 1rem; max-width: 76ch; color: var(--muted); font-size: .85rem; }
.machine-list { display: grid; gap: .55rem; }
.machine-row { display: grid; grid-template-columns: minmax(14rem, .7fr) minmax(18rem, 1.3fr) 20rem; grid-template-areas: "identity connect actions" "details details details"; gap: .4rem 1rem; align-items: center; min-width: 0; padding: .7rem 1rem; border: 1px solid var(--border); border-radius: 12px; background: var(--card-bg); }
.machine-identity { grid-area: identity; min-width: 0; }
.machine-name { margin: 0; color: var(--text); font-size: 1rem; line-height: 1.3; font-weight: 650; overflow-wrap: anywhere; }
.machine-target { margin: .18rem 0 0; color: var(--muted); font-size: .76rem; line-height: 1.35; overflow-wrap: anywhere; }
.machine-connect { grid-area: connect; display: grid; grid-template-columns: minmax(0, 1fr); gap: .3rem; min-width: 0; }
.machine-field-label { color: var(--muted); font-size: .76rem; font-weight: 600; }
.machine-connect select, .machine-account-input { width: 100%; min-width: 12rem; min-height: 2.75rem; padding: .45rem .65rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; }
.machine-custom-account { display: grid; gap: .3rem; min-width: 0; }
.machine-custom-account[hidden] { display: none; }
.machine-actions { grid-area: actions; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: center; gap: .4rem .5rem; min-width: 0; }
.copy-command, .btn-console { display: inline-flex; align-items: center; justify-content: center; width: 100%; min-width: 0; min-height: 2.75rem; padding: .45rem .65rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; font-size: .82rem; line-height: 1.2; text-decoration: none; cursor: pointer; white-space: nowrap; }
.machine-copy { border-color: var(--accent); background: var(--accent); color: var(--bg); font-weight: 650; }
.copy-command:hover, .copy-command:focus-visible, .btn-console:hover, .btn-console:focus-visible { filter: brightness(1.08); }
.copy-feedback { min-height: 1.2em; color: var(--muted); font-size: .76rem; }
.machine-feedback { grid-column: 1 / -1; min-height: 0; text-align: right; }
.machine-feedback:empty { display: none; }
.machine-policy-details { grid-area: details; min-width: 0; padding-top: .15rem; border-top: 1px solid transparent; color: var(--muted); font-size: .78rem; }
.machine-policy-details[open] { padding-top: .6rem; border-top-color: var(--border); }
.machine-policy-details summary { width: fit-content; cursor: pointer; color: var(--muted); font-weight: 600; }
.machine-policy-details summary:hover, .machine-policy-details summary:focus-visible { color: var(--text); }
.machine-policy-body { display: grid; gap: .55rem; margin-top: .6rem; }
.machine-policy-evidence, .machine-policy-note { margin: 0; }
.machine-policy-accounts { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: .45rem .9rem; margin: 0; padding: 0; list-style: none; }
.machine-policy-accounts li { display: flex; flex-direction: column; gap: .06rem; min-width: 0; }
.machine-account-name { color: var(--text); font-size: .85rem; overflow-wrap: anywhere; }
.machine-account-detail { color: var(--muted); font-size: .74rem; line-height: 1.35; overflow-wrap: anywhere; }
.btn-clear-accounts { min-height: 2.1rem; padding: .3rem .7rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; cursor: pointer; }
.btn-clear-accounts:hover, .btn-clear-accounts:focus-visible { border-color: var(--accent); }
.account-panel-status { display: block; margin-top: .35rem; color: var(--muted); font-size: .78rem; }
.empty { grid-column: 1 / -1; text-align: center; padding: 4rem 1.5rem; color: var(--muted); }
.machine-empty { padding-block: 2rem; }
.empty-icon { font-size: 2.5rem; line-height: 1; margin-bottom: .5rem; opacity: .5; }
.empty p { margin: 0; }
.bottom-nav { display: none; }
@media (min-width: 601px) {
  .portal-content { padding-top: 1.25rem; }
  .card-meta { flex-direction: column; align-items: flex-start; flex-wrap: nowrap; }
  .health-status { margin-left: 0; text-align: left; }
}
@media (max-width: 900px) {
  .machine-row { grid-template-columns: minmax(11rem, .75fr) minmax(18rem, 1.25fr); grid-template-areas: "identity connect" "details actions"; align-items: start; }
  .machine-feedback { text-align: left; }
}
@media (max-width: 600px) {
  header { padding: 1rem 1rem .6rem; align-items: center; }
  main { padding: .75rem 1rem 2rem; }
  .portal-content { gap: 1.5rem; }
  .portal-search { margin-bottom: .6rem; }
  .header-side { flex: 1 1 auto; min-width: 0; justify-content: flex-end; }
  .user { text-align: right; max-width: 100%; }
  .grid { grid-template-columns: minmax(0, 1fr); }
  .card-meta { align-items: flex-start; }
  .health-status { margin-left: 0; text-align: left; }
  .machine-row { grid-template-columns: minmax(0, 1fr); grid-template-areas: "identity" "connect" "actions" "details"; gap: .6rem; padding: .8rem; }
  .machine-connect select, .machine-account-input { min-width: 0; }
  .machine-actions:not(.machine-actions-with-console) { grid-template-columns: minmax(0, 1fr); }
  .machine-policy-accounts { grid-template-columns: minmax(0, 1fr); }
  body { padding-bottom: calc(4.75rem + env(safe-area-inset-bottom)); }
  .account { min-width: 0; max-width: 100%; }
  .account-trigger { max-width: 100%; text-align: right; }
  .account-panel { position: fixed; top: auto; left: 1rem; right: 1rem; bottom: calc(4.5rem + env(safe-area-inset-bottom)); width: auto; max-width: none; max-height: min(70vh, 32rem); overflow-y: auto; z-index: 40; }
  .bottom-nav { display: flex; position: fixed; left: .6rem; right: .6rem; bottom: calc(.6rem + env(safe-area-inset-bottom)); z-index: 30; justify-content: space-around; align-items: center; gap: .2rem; padding: .3rem max(.4rem, env(safe-area-inset-right)) .3rem max(.4rem, env(safe-area-inset-left)); border: 1px solid var(--border); border-radius: 20px; background: var(--card-bg); box-shadow: 0 12px 28px rgba(0, 0, 0, .22); transition: transform .18s ease, opacity .18s ease; }
  body.portal-typing .bottom-nav { transform: translateY(140%); opacity: 0; pointer-events: none; }
  .bottom-nav-item { display: flex; flex: 1 1 0; min-width: 0; min-height: 44px; flex-direction: column; align-items: center; justify-content: center; gap: .12rem; padding: .3rem .2rem; border: 0; border-radius: 14px; background: transparent; color: var(--muted); text-decoration: none; font: inherit; cursor: pointer; }
  .bottom-nav-item[hidden] { display: none; }
  .bottom-nav-item:hover, .bottom-nav-item:focus-visible { color: var(--text); background: var(--card-hover-bg); }
  .bottom-nav-item[aria-current="location"] { color: var(--text); background: var(--badge-bg); font-weight: 700; }
  .bottom-nav-icon { display: flex; }
  .bottom-nav-icon svg { width: 1.15rem; height: 1.15rem; }
  .bottom-nav-label { font-size: .7rem; font-weight: 650; line-height: 1.15; }
}
@media (prefers-reduced-motion: reduce) {
  .bottom-nav { transition: none; }
  a.card:hover, a.card:focus-visible { transform: none; }
}
</style>
<noscript><style>
  /* Search is a JS-only convenience filter over content that is already fully
     usable without it. Hide the search field and the bottom-nav Search action
     entirely when JS is unavailable so no nonfunctional control is shown;
     Services/Machines links and the settings sheet remain plain HTML. */
  .portal-search, #bottom-nav-search { display: none; }
</style></noscript>
<script>
(function () {
  "use strict";
  var scope = "{{LOGO_PREF_SCOPE}}";
  var scopedKey = "velociportal.logo.visible." + scope;
  var legacyKey = "velociportal.logo.visible";
  var defaultVisible = "{{LOGO_DEFAULT_VISIBLE}}" !== "false";
  var visible = defaultVisible;

  try {
    var scopedValue = window.localStorage.getItem(scopedKey);
    if (scopedValue === "true" || scopedValue === "false") {
      visible = scopedValue === "true";
    } else {
      var legacyValue = window.localStorage.getItem(legacyKey);
      if (legacyValue === "true" || legacyValue === "false") {
        visible = legacyValue === "true";
        window.localStorage.setItem(scopedKey, String(visible));
        window.localStorage.removeItem(legacyKey);
      }
    }
  } catch (_) {
    visible = defaultVisible;
  }

  if (!visible) document.documentElement.setAttribute("data-logo", "hidden");
  window.__velociportalLogoPreference = { scopedKey: scopedKey, visible: visible };
}());
</script>
</head>
<body>
<header>
<div class="brand">
<img class="brand-logo" id="brand-logo" src="/static/logo.svg" alt="">
<span class="brand-name">Velociportal</span>
</div>
<div class="header-side">
<div class="account" id="account">
<button class="account-trigger" id="account-trigger" type="button" aria-label="Account settings for {{USER_LOGIN}}" aria-haspopup="dialog" aria-expanded="false" aria-controls="account-panel" hidden>
<div class="user-name">{{USER_NAME}}</div>
<div class="login">{{USER_LOGIN}}</div>
</button>
<div class="user" id="account-fallback">
<div class="user-name">{{USER_NAME}}</div>
<div class="login">{{USER_LOGIN}}</div>
</div>
<div class="account-panel" id="account-panel" role="dialog" aria-labelledby="account-panel-title" hidden>
<h2 class="account-panel-title" id="account-panel-title">Account settings</h2>
<div class="account-panel-section">
<div class="account-panel-heading">Signed in as</div>
<div class="user-name">{{USER_NAME}}</div>
<div class="login">{{USER_LOGIN}}</div>
</div>
<div class="account-panel-section">
<div class="account-panel-heading">Appearance</div>
<label class="account-panel-checkbox">
<input type="checkbox" id="logo-visible-checkbox">
Show Velociportal logo
</label>
</div>
<div class="account-panel-section">
<div class="account-panel-heading">SSH accounts</div>
<button type="button" class="btn-clear-accounts" id="clear-ssh-accounts-button" hidden>Clear saved SSH accounts</button>
<span class="account-panel-status" id="clear-ssh-accounts-status" role="status" aria-live="polite"></span>
</div>
</div>
</div>
</div>
</header>
<main>
<div class="portal-search">
<label class="portal-search-label" for="portal-search-input">Search services and machines</label>
<div class="portal-search-field">
<svg class="portal-search-icon" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"></circle><line x1="21" y1="21" x2="16.65" y2="16.65"></line></svg>
<input type="search" id="portal-search-input" class="portal-search-input" placeholder="Search services and machines" autocomplete="off" autocorrect="off" autocapitalize="none" spellcheck="false" enterkeyhint="done">
<button type="button" class="portal-search-clear" id="portal-search-clear" aria-label="Clear search" hidden><svg aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg></button>
</div>
<span class="portal-search-status" id="portal-search-status" role="status" aria-live="polite"></span>
</div>
<p class="portal-search-no-results" id="portal-search-no-results" hidden>No results for this search.</p>
<div class="portal-content" id="portal-content" hx-get="/portal" hx-trigger="every 60s" hx-target="#portal-content" hx-select="#portal-content" hx-swap="outerHTML">
<section class="portal-section" aria-labelledby="services-heading">
<h2 class="section-title" id="services-heading">Services</h2>
{{SERVICES_HEALTH_HELP}}
<div class="{{SERVICES_CLASS}}" id="services">
{{SERVICES_BODY}}
</div>
</section>
{{MACHINES_SECTION}}
</div>
</main>
{{BOTTOM_NAV}}
<datalist id="ssh-account-suggestions"></datalist>
<script src="/static/htmx.min.js"></script>
<script>
(function () {
  "use strict";
  var root = document.documentElement;
  var trigger = document.getElementById("account-trigger");
  var fallback = document.getElementById("account-fallback");
  var panel = document.getElementById("account-panel");
  var checkbox = document.getElementById("logo-visible-checkbox");
  var moreButton = document.getElementById("bottom-nav-more");
  if (!trigger || !fallback || !panel || !checkbox) return;

  var pref = window.__velociportalLogoPreference || { scopedKey: "velociportal.logo.visible", visible: true };
  var visible = pref.visible;
  var panelOpener = trigger;

  function applyLogoPreference() {
    if (visible) root.removeAttribute("data-logo");
    else root.setAttribute("data-logo", "hidden");
    checkbox.checked = visible;
  }

  checkbox.addEventListener("change", function () {
    visible = checkbox.checked;
    applyLogoPreference();
    try {
      window.localStorage.setItem(pref.scopedKey, String(visible));
    } catch (_) {}
  });

  function setExpanded(expanded) {
    trigger.setAttribute("aria-expanded", String(expanded));
    if (moreButton) moreButton.setAttribute("aria-expanded", String(expanded));
  }

  function openPanel(opener) {
    panelOpener = opener || trigger;
    panel.hidden = false;
    setExpanded(true);
    checkbox.focus();
    document.addEventListener("keydown", onKeydown, true);
    document.addEventListener("click", onOutsideClick, true);
  }

  function closePanel(restoreFocus) {
    panel.hidden = true;
    setExpanded(false);
    document.removeEventListener("keydown", onKeydown, true);
    document.removeEventListener("click", onOutsideClick, true);
    if (restoreFocus && panelOpener) panelOpener.focus();
  }

  function onKeydown(event) {
    if (event.key === "Escape") {
      event.preventDefault();
      closePanel(true);
    }
  }

  function onOutsideClick(event) {
    if (panel.contains(event.target) || trigger.contains(event.target) || (moreButton && moreButton.contains(event.target))) return;
    closePanel(true);
  }

  trigger.addEventListener("click", function () {
    if (panel.hidden) openPanel(trigger);
    else closePanel(true);
  });
  if (moreButton) {
    moreButton.addEventListener("click", function () {
      if (panel.hidden) openPanel(moreButton);
      else closePanel(true);
    });
  }

  applyLogoPreference();
  fallback.hidden = true;
  trigger.hidden = false;
}());

(function () {
  "use strict";

  // Lightweight active-section tracking: a feature-detected IntersectionObserver
  // drives aria-current="location" on the Services/Machines section links, with
  // a click fallback for browsers without it. The observer is disconnected and
  // rebuilt after every htmx swap so it never watches stale nodes, and Machines'
  // current-location state is cleared the moment the projection disappears.
  var observer = null;

  function machinesLinkEl() { return document.getElementById("bottom-nav-machines"); }
  function servicesLinkEl() { return document.getElementById("bottom-nav-services"); }

  function setCurrentNavLink(link) {
    [servicesLinkEl(), machinesLinkEl()].forEach(function (candidate) {
      if (!candidate) return;
      if (candidate === link) candidate.setAttribute("aria-current", "location");
      else candidate.removeAttribute("aria-current");
    });
  }

  function observeSections() {
    if (observer) {
      observer.disconnect();
      observer = null;
    }
    var servicesHeading = document.getElementById("services-heading");
    var machinesLink = machinesLinkEl();
    var machinesHeading = machinesLink && !machinesLink.hidden ? document.getElementById("machines-heading") : null;
    var targets = [];
    if (servicesHeading) targets.push([servicesHeading, servicesLinkEl()]);
    if (machinesHeading) targets.push([machinesHeading, machinesLink]);

    if (!("IntersectionObserver" in window) || targets.length === 0) {
      setCurrentNavLink(servicesLinkEl());
      return;
    }
    observer = new IntersectionObserver(function (entries) {
      var visible = entries.filter(function (entry) { return entry.isIntersecting; });
      if (visible.length === 0) return;
      var match = targets.filter(function (pair) { return pair[0] === visible[0].target; })[0];
      if (match) setCurrentNavLink(match[1]);
    }, { rootMargin: "-40% 0px -55% 0px", threshold: 0 });
    targets.forEach(function (pair) { observer.observe(pair[0]); });
    setCurrentNavLink(servicesLinkEl());
  }

  function syncBottomNavMachines() {
    var machinesLink = machinesLinkEl();
    var available = !!document.getElementById("machines-heading");
    if (machinesLink) {
      machinesLink.hidden = !available;
      if (!available) machinesLink.removeAttribute("aria-current");
    }
    observeSections();
  }

  document.addEventListener("click", function (event) {
    var link = event.target.closest("[data-bottom-nav-scroll]");
    if (!link) return;
    var target = document.getElementById(link.getAttribute("data-bottom-nav-scroll"));
    if (!target) return;
    event.preventDefault();
    var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    target.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
    target.setAttribute("tabindex", "-1");
    target.focus({ preventScroll: true });
    target.addEventListener("blur", function () { target.removeAttribute("tabindex"); }, { once: true });
    setCurrentNavLink(link);
  });

  document.body.addEventListener("htmx:afterSwap", syncBottomNavMachines);
  syncBottomNavMachines();
}());

(function () {
  "use strict";

  // Suppress the bottom dock while a mobile text field has focus so it never
  // competes with the on-screen keyboard, then restore it on exit. This only
  // toggles a body class the dock's own CSS transition responds to -- no
  // visualViewport reads, no keyboard-height measurement, no repositioning of
  // any element. The settings sheet (a checkbox/button dialog, not a text
  // field) never triggers this class, so it is never obstructed.
  //
  // The CSS transform/opacity used to hide the dock does not remove it from
  // the keyboard tab order, so suppression also marks the dock inert (with a
  // tabindex fallback for browsers without inert support) so its links and
  // buttons are not reachable via Tab while visually/pointer hidden. A
  // control that currently has focus is never hidden: suppression only ever
  // runs when focus is moving into a text field elsewhere on the page, and
  // the dock is left alone if it somehow already contains the active
  // element. Focusability is restored as soon as focus leaves every text
  // field, including moves into the settings sheet.
  var TYPING_CLASS = "portal-typing";
  var dock = document.getElementById("bottom-nav");

  function isTextEntry(el) {
    if (!el) return false;
    if (el.tagName === "TEXTAREA") return true;
    if (el.tagName !== "INPUT") return false;
    var type = (el.getAttribute("type") || "text").toLowerCase();
    return type === "text" || type === "search";
  }

  function dockContainsFocus() {
    var node = dock && document.activeElement;
    while (node) {
      if (node === dock) return true;
      node = node.parentNode;
    }
    return false;
  }

  function setDockInert(state) {
    if (!dock) return;
    if ("inert" in dock) {
      dock.inert = state;
      return;
    }
    var items = dock.querySelectorAll(".bottom-nav-item");
    for (var i = 0; i < items.length; i++) {
      if (state) items[i].setAttribute("tabindex", "-1");
      else items[i].removeAttribute("tabindex");
    }
  }

  document.addEventListener("focusin", function (event) {
    if (!isTextEntry(event.target)) return;
    document.body.classList.add(TYPING_CLASS);
    if (!dockContainsFocus()) setDockInert(true);
  });
  document.addEventListener("focusout", function (event) {
    if (!isTextEntry(event.target)) return;
    window.setTimeout(function () {
      if (!isTextEntry(document.activeElement)) {
        document.body.classList.remove(TYPING_CLASS);
        setDockInert(false);
      }
    }, 0);
  });
}());

(function () {
  "use strict";

  // Prominent, persistent, browser-only search over already-rendered,
  // already-authorized service cards and machine rows. The input lives
  // outside the #portal-content htmx swap boundary, so the query and focus
  // both survive the 60-second refresh untouched; this controller only
  // re-applies the existing query to the freshly swapped DOM afterward. The
  // query is never written to storage, the URL, logs, or any network call.
  var input = document.getElementById("portal-search-input");
  var clearButton = document.getElementById("portal-search-clear");
  var status = document.getElementById("portal-search-status");
  var noResults = document.getElementById("portal-search-no-results");
  var searchNavButton = document.getElementById("bottom-nav-search");
  if (!input) return;

  function cardHaystack(card) {
    var nameEl = card.querySelector(".card-name");
    var text = nameEl ? nameEl.textContent : "";
    if (card.tagName === "A" && card.href) {
      try {
        text += " " + new URL(card.href, window.location.href).hostname;
      } catch (_) {}
    }
    return text.toLowerCase();
  }

  function machineHaystack(row) {
    var nameEl = row.querySelector(".machine-name");
    var targetEl = row.querySelector(".machine-target");
    var text = (nameEl ? nameEl.textContent : "") + " " +
      (targetEl ? targetEl.textContent : "") + " " +
      (row.getAttribute("data-machine") || "");
    return text.toLowerCase();
  }

  function applyPortalSearch() {
    var raw = input.value || "";
    var query = raw.trim().toLowerCase();
    if (clearButton) clearButton.hidden = raw.length === 0;

    var cards = document.querySelectorAll("#services .card, #services .card-unlinked");
    var rows = document.querySelectorAll(".machine-row");
    var visible = 0;

    cards.forEach(function (card) {
      var match = query === "" || cardHaystack(card).indexOf(query) !== -1;
      card.hidden = !match;
      if (match) visible++;
    });
    rows.forEach(function (row) {
      var match = query === "" || machineHaystack(row).indexOf(query) !== -1;
      row.hidden = !match;
      if (match) visible++;
    });

    document.querySelectorAll(".service-category").forEach(function (section) {
      if (query === "") {
        section.hidden = false;
        return;
      }
      var anyVisible = false;
      section.querySelectorAll(".card, .card-unlinked").forEach(function (card) {
        if (!card.hidden) anyVisible = true;
      });
      section.hidden = !anyVisible;
    });

    var searchable = cards.length + rows.length;
    var showNoResults = query !== "" && searchable > 0 && visible === 0;
    if (noResults) noResults.hidden = !showNoResults;
    if (status) status.textContent = showNoResults ? "No results for this search." : "";
  }

  input.addEventListener("input", applyPortalSearch);
  input.addEventListener("keydown", function (event) {
    if (event.key === "Enter") {
      event.preventDefault();
      input.blur();
    }
  });
  if (clearButton) {
    clearButton.addEventListener("click", function () {
      input.value = "";
      applyPortalSearch();
      input.focus();
    });
  }
  if (searchNavButton) {
    searchNavButton.addEventListener("click", function () {
      var reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      input.scrollIntoView({ behavior: reduced ? "auto" : "smooth", block: "start" });
      input.focus();
    });
  }
  document.body.addEventListener("htmx:afterSwap", applyPortalSearch);
  applyPortalSearch();
  window.__velociportalApplyPortalSearch = applyPortalSearch;
}());

(function () {
  "use strict";

  function copyText(value) {
    if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(value);
    }
    return new Promise(function (resolve, reject) {
      var input = document.createElement("textarea");
      input.value = value;
      input.setAttribute("readonly", "");
      input.style.position = "fixed";
      input.style.opacity = "0";
      document.body.appendChild(input);
      input.select();
      try {
        if (!document.execCommand("copy")) throw new Error("copy unavailable");
        resolve();
      } catch (error) {
        reject(error);
      } finally {
        input.remove();
      }
    });
  }
  window.__velociportalCopyText = copyText;
}());

(function () {
  "use strict";

  // Browser-local, per-identity most-recently-used SSH account suggestions.
  // These never leave the browser: the storage key reuses the same opaque
  // SHA-256 login scope as the logo preference, never the raw login, and
  // nothing here is sent to or read by Velociportal.
  var identityScope = "{{LOGO_PREF_SCOPE}}";
  var ACCOUNTS_KEY = "velociportal.ssh.accounts." + identityScope;
  var MAX_ACCOUNTS = 10;
  var MAX_ACCOUNT_LENGTH = 256;
  var ACCOUNT_PATTERN = /^[A-Za-z_][A-Za-z0-9_.-]*\$?$/;

  // isValidAccount mirrors the server's bounded literal-account grammar
  // (supportedSSHUser): 1-256 ASCII characters, a letter/underscore first,
  // then letters/digits/./-, with an optional final $. It additionally
  // rejects "root" outright, because this field is only ever offered for the
  // separate autogroup:nonroot policy scope, never the literal root account.
  function isValidAccount(value) {
    return typeof value === "string" &&
      value.length >= 1 &&
      value.length <= MAX_ACCOUNT_LENGTH &&
      value !== "root" &&
      ACCOUNT_PATTERN.test(value);
  }

  function readSavedAccounts() {
    try {
      var raw = window.localStorage.getItem(ACCOUNTS_KEY);
      if (!raw) return [];
      var parsed = JSON.parse(raw);
      if (!Array.isArray(parsed)) return [];
      var seen = Object.create(null);
      var result = [];
      for (var i = 0; i < parsed.length && result.length < MAX_ACCOUNTS; i++) {
        var value = parsed[i];
        if (!isValidAccount(value) || seen[value] === true) continue;
        seen[value] = true;
        result.push(value);
      }
      return result;
    } catch (_) {
      return [];
    }
  }

  function writeSavedAccounts(list) {
    try {
      window.localStorage.setItem(ACCOUNTS_KEY, JSON.stringify(list));
    } catch (_) {
      // Storage denial or quota exceeded: fail harmlessly, no saved accounts.
    }
  }

  function rememberAccount(value) {
    if (!isValidAccount(value)) return;
    var list = readSavedAccounts().filter(function (entry) { return entry !== value; });
    list.unshift(value);
    if (list.length > MAX_ACCOUNTS) list = list.slice(0, MAX_ACCOUNTS);
    writeSavedAccounts(list);
    refreshAccountUI();
  }

  function clearSavedAccounts() {
    var cleared = false;
    try {
      window.localStorage.removeItem(ACCOUNTS_KEY);
      cleared = window.localStorage.getItem(ACCOUNTS_KEY) === null;
    } catch (_) {}
    refreshAccountUI();
    var status = document.getElementById("clear-ssh-accounts-status");
    if (status) status.textContent = cleared ? "Saved SSH accounts cleared." : "Saved SSH accounts could not be cleared.";
  }

  function populateDatalist() {
    var datalist = document.getElementById("ssh-account-suggestions");
    if (!datalist) return;
    var accounts = readSavedAccounts();
    while (datalist.firstChild) datalist.removeChild(datalist.firstChild);
    accounts.forEach(function (account) {
      var option = document.createElement("option");
      option.value = account;
      datalist.appendChild(option);
    });
  }

  function refreshAccountUI() {
    populateDatalist();
    var button = document.getElementById("clear-ssh-accounts-button");
    if (button) button.hidden = readSavedAccounts().length === 0;
  }

  // buildAccountCommand mirrors machineSSHCommand's safe trailing-$ shell
  // escaping. The target here is always the existing server-rendered, already
  // validated data-account-target attribute; only the typed account is new.
  function buildAccountCommand(user, target) {
    var argument = user + "@" + target;
    if (user.charAt(user.length - 1) === "$") {
      argument = user.slice(0, -1) + "\\$@" + target;
    }
    return "tailscale ssh " + argument;
  }

  function syncMachineAccountControl(select, focusInput) {
    if (!select) return;
    var panelID = select.getAttribute("aria-controls");
    if (!panelID) return;
    var panel = document.getElementById(panelID);
    var option = select.options[select.selectedIndex];
    var custom = !!(option && option.hasAttribute("data-custom-account"));
    if (!panel) return;
    panel.hidden = !custom;
    select.setAttribute("aria-expanded", String(custom));
    if (custom && focusInput) {
      var input = panel.querySelector(".machine-account-input");
      if (input) input.focus();
    }
  }

  function syncMachineAccountControls() {
    document.querySelectorAll("[data-machine-account-select][aria-controls]").forEach(function (select) {
      syncMachineAccountControl(select, false);
    });
  }

  document.addEventListener("change", function (event) {
    var select = event.target.closest("[data-machine-account-select]");
    if (select) syncMachineAccountControl(select, true);
  });

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy-machine-ssh]");
    if (!button) return;

    var feedback = document.getElementById(button.getAttribute("aria-describedby"));
    var selectID = button.getAttribute("data-user-select");
    var select = selectID ? document.getElementById(selectID) : null;
    var option = select && select.options[select.selectedIndex];
    var command = option && option.getAttribute("data-command");
    var custom = !select || !!(option && option.hasAttribute("data-custom-account"));
    var customValue = "";
    var copyText = window.__velociportalCopyText;
    if (!feedback || !copyText) return;

    if (custom) {
      var inputID = button.getAttribute("data-account-input");
      var input = inputID ? document.getElementById(inputID) : null;
      customValue = input && input.value;
      var target = input && input.getAttribute("data-account-target");
      if (!isValidAccount(customValue) || !target) {
        feedback.textContent = "Enter a valid non-root account.";
        return;
      }
      command = buildAccountCommand(customValue, target);
    }
    if (!command || command.indexOf("tailscale ssh ") !== 0) {
      feedback.textContent = "Copy unavailable.";
      return;
    }

    feedback.textContent = "Copying command...";
    copyText(command).then(function () {
      feedback.textContent = "Command copied.";
      if (custom) rememberAccount(customValue);
    }, function () {
      feedback.textContent = "Copy unavailable.";
    });
  });

  document.addEventListener("click", function (event) {
    if (event.target.id !== "clear-ssh-accounts-button") return;
    clearSavedAccounts();
  });

  document.body.addEventListener("htmx:afterSwap", function () {
    refreshAccountUI();
    syncMachineAccountControls();
  });

  refreshAccountUI();
  syncMachineAccountControls();
}());

(function () {
  "use strict";
  if (!("serviceWorker" in navigator)) return;
  window.addEventListener("load", function () {
    navigator.serviceWorker.register("/static/sw.js", { scope: "/" }).catch(function () {});
  });
}());
</script>
</body>
</html>`
