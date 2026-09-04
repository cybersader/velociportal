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
			if machineConsoleCapable(machine.ID, data) {
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
				`<p class="machines-help">These labels describe Tailscale's identity check, not machine health. Velociportal reads policy but does not discover which local accounts actually exist.</p>`+
				`<div class="grid" id="machines">%s</div>`+
				`</section>`,
			machinesBody.String(),
		)
	}

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

func renderBottomNav(machinesAvailable bool) string {
	machinesHidden := ""
	if !machinesAvailable {
		machinesHidden = " hidden"
	}
	return `<nav class="bottom-nav" id="bottom-nav" aria-label="Portal navigation">` +
		`<a class="bottom-nav-item" id="bottom-nav-services" href="#services-heading" data-bottom-nav-scroll="services-heading"><span class="bottom-nav-icon" aria-hidden="true">&#9638;</span><span class="bottom-nav-label">Services</span></a>` +
		`<a class="bottom-nav-item" id="bottom-nav-machines" href="#machines-heading" data-bottom-nav-scroll="machines-heading"` + machinesHidden + `><span class="bottom-nav-icon" aria-hidden="true">&#9673;</span><span class="bottom-nav-label">Machines</span></a>` +
		`<button type="button" class="bottom-nav-item" id="bottom-nav-more" aria-haspopup="dialog" aria-controls="account-panel" aria-expanded="false"><span class="bottom-nav-icon" aria-hidden="true">&#8943;</span><span class="bottom-nav-label">More</span></button>` +
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
				`<span class="card-head"><span class="card-name">%s</span>%s</span>`+
				`<span class="badge">%s</span>`+
				`</a>`,
			html.EscapeString(card.URL),
			html.EscapeString(card.Name),
			html.EscapeString(card.Name),
			healthMarkup,
			html.EscapeString(scheme),
		)
		return
	}

	if card.LinkState == serviceLinkReady {
		slog.Warn("rendering card with invalid browser URL as unlinked", "proxy_host_id", card.ID)
	}
	fmt.Fprintf(body,
		`<article class="card card-unlinked" data-service="%s">`+
			`<span class="card-head"><span class="card-name">%s</span>%s</span>`+
			`<span class="badge">link needed</span>`+
			`<span class="card-note">Add a concrete service URL in Velociportal metadata.</span>`+
			`</article>`,
		html.EscapeString(card.Name),
		html.EscapeString(card.Name),
		healthMarkup,
	)
}

func renderMachineCard(body *strings.Builder, machine MachineCard, index int, consoleEligible bool) {
	machineID := fmt.Sprintf("machine-%d", index)
	shortName, fullTarget, hasFullTarget := machineCardNames(machine.Target)
	fmt.Fprintf(body,
		`<article class="card machine-card" data-machine="%s" aria-labelledby="%s-name">`+
			`<span class="card-head"><span class="card-name" id="%s-name">%s</span></span>`,
		html.EscapeString(machine.Target),
		machineID,
		machineID,
		html.EscapeString(shortName),
	)
	if hasFullTarget {
		fmt.Fprintf(body, `<p class="machine-target">%s</p>`, html.EscapeString(fullTarget))
	}
	body.WriteString(`<p class="machine-policy">Supported by SSH policy and a network Grant for port 22.</p>` +
		`<div class="machine-accounts">` +
		`<div class="machine-accounts-heading">Accounts allowed by policy</div>` +
		`<ul class="machine-accounts-list" aria-label="Accounts allowed by policy">`)

	literalUsers := make([]string, 0, len(machine.Access))
	hasNonroot := false
	for _, access := range machine.Access {
		account := access.User
		if account == machineNonrootSelector {
			account = "Any non-root account"
			hasNonroot = true
		} else if _, ok := machineSSHCommand(access.User, machine.Target); ok {
			literalUsers = append(literalUsers, access.User)
		}
		fmt.Fprintf(body,
			`<li class="machine-account-row"><span class="machine-account-name">%s</span><span class="machine-account-detail">%s</span></li>`,
			html.EscapeString(account),
			html.EscapeString(machineActionLabel(access)),
		)
	}
	body.WriteString(`</ul>`)

	// autogroup:nonroot is a policy scope, not an inventory of accounts that
	// exist on the machine. This nested field only combines a client-validated
	// typed account with the already safe server-rendered target.
	if hasNonroot {
		accountID := machineID + "-account"
		accountFeedbackID := machineID + "-account-feedback"
		fmt.Fprintf(body,
			`<div class="machine-account-policy">`+
				`<div class="machine-account-policy-heading">Use another non-root account</div>`+
				`<div class="machine-command machine-command-custom">`+
				`<label for="%s">Account on this machine</label>`+
				`<input type="text" id="%s" class="machine-account-input" list="ssh-account-suggestions" autocomplete="off" spellcheck="false" maxlength="256" placeholder="e.g. jdoe" data-account-target="%s">`+
				`<button type="button" class="copy-command" data-copy-ssh-account data-account-input="%s" aria-describedby="%s">Copy command</button>`+
				`<span class="copy-feedback" id="%s" role="status" aria-live="polite"></span>`+
				`<p class="machine-account-note">Velociportal cannot verify that this account exists on the machine.</p>`+
				`</div></div>`,
			accountID,
			accountID,
			html.EscapeString(machine.Target),
			accountID,
			accountFeedbackID,
			accountFeedbackID,
		)
	}
	body.WriteString(`</div>`)

	if len(literalUsers) > 0 {
		selectID := machineID + "-user"
		feedbackID := machineID + "-copy-feedback"
		fmt.Fprintf(body,
			`<div class="machine-command">`+
				`<label for="%s">SSH account</label>`+
				`<select id="%s">`,
			selectID,
			selectID,
		)
		for _, user := range literalUsers {
			command, _ := machineSSHCommand(user, machine.Target)
			fmt.Fprintf(body,
				`<option value="%s" data-command="%s">%s</option>`,
				html.EscapeString(user),
				html.EscapeString(command),
				html.EscapeString(user),
			)
		}
		fmt.Fprintf(body,
			`</select>`+
				`<button type="button" class="copy-command" data-copy-ssh-command data-user-select="%s" aria-describedby="%s">Copy command</button>`+
				`<span class="copy-feedback" id="%s" role="status" aria-live="polite"></span>`+
				`</div>`,
			selectID,
			feedbackID,
			feedbackID,
		)
	}

	if consoleEligible {
		if consoleURL, ok := machineConsoleURL(machine.Target); ok {
			noteID := machineID + "-console-note"
			fmt.Fprintf(body,
				`<div class="machine-console">`+
					`<a class="btn-console" href="%s" target="_blank" rel="noopener noreferrer" aria-describedby="%s">Open in Tailscale Machines</a>`+
					`<p class="machine-console-note" id="%s">Opens the filtered Machines page in a new tab. Start browser SSH there; Tailscale handles eligibility, account choice, reauthentication, posture, policy, and the session.</p>`+
					`</div>`,
				html.EscapeString(consoleURL),
				noteID,
				noteID,
			)
		}
	}
	body.WriteString(`</article>`)
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

	label := "unknown"
	className := "unknown"
	switch result.State {
	case ServiceHealthStateReachable:
		label = "reachable"
		className = "reachable"
	case ServiceHealthStateAuthRequired:
		label = "authentication required"
		className = "auth-required"
	case ServiceHealthStateResponseError:
		label = "response error"
		className = "response-error"
	case ServiceHealthStateUnreachable:
		label = "unreachable"
		className = "unreachable"
	case ServiceHealthStateStale:
		label = "stale"
		className = "stale"
	}
	return fmt.Sprintf(
		`<span class="health-status health-%s" aria-label="Service health: %s">%s</span>`,
		className,
		html.EscapeString(label),
		html.EscapeString(label),
	)
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
  --health-reachable: #3fb950;
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
    --health-reachable: #1a7f37;
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
  --health-reachable: #1a7f37;
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
  --health-reachable: #3fb950;
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
.card-head { display: flex; align-items: center; gap: .5rem; min-width: 0; }
.card-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--text); }
.card-note { font-size: .82rem; line-height: 1.35; }
.badge { align-self: flex-start; padding: .1rem .5rem; border-radius: 999px; font-size: .72rem; font-weight: 600; letter-spacing: .02em; text-transform: uppercase; background: var(--badge-bg); color: var(--badge-text); }
.health-status { margin-left: auto; flex-shrink: 0; font-size: .72rem; font-weight: 650; line-height: 1.2; text-align: right; }
.health-reachable { color: var(--health-reachable); }
.health-auth-required { color: var(--health-auth); }
.health-response-error { color: var(--health-response); }
.health-unreachable { color: var(--health-unreachable); }
.health-stale { color: var(--health-stale); }
.health-unknown { color: var(--health-unknown); }
.machines-help { margin: 0 0 .85rem; max-width: 60ch; color: var(--muted); font-size: .85rem; }
.machine-card { min-width: 0; }
.machine-target { margin: 0 0 .3rem; color: var(--muted); font-size: .78rem; overflow-wrap: anywhere; }
.machine-policy { margin: 0; color: var(--muted); font-size: .88rem; }
.machine-accounts { display: grid; gap: .45rem; margin: .15rem 0 .25rem; min-width: 0; }
.machine-accounts-heading { color: var(--muted); font-size: .74rem; font-weight: 600; letter-spacing: .02em; text-transform: uppercase; }
.machine-accounts-list { display: grid; gap: .5rem; margin: 0; padding: 0; list-style: none; }
.machine-account-row { display: flex; flex-direction: column; gap: .08rem; min-width: 0; }
.machine-account-name { font-size: .9rem; overflow-wrap: anywhere; }
.machine-account-detail { color: var(--muted); font-size: .76rem; line-height: 1.35; overflow-wrap: anywhere; }
.machine-account-policy { display: grid; gap: .35rem; min-width: 0; margin-top: .15rem; padding: .55rem 0 0 .7rem; border-left: 2px solid var(--border); }
.machine-account-policy-heading { color: var(--muted); font-size: .76rem; font-weight: 600; }
.machine-command { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: .45rem .6rem; align-items: end; margin-top: .3rem; }
.machine-command label { grid-column: 1 / -1; color: var(--muted); font-size: .78rem; font-weight: 600; }
.machine-command select, .machine-account-input, .copy-command { min-height: 2.35rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; }
.machine-command select, .machine-account-input { min-width: 0; padding: .35rem .5rem; }
.copy-command { padding: .35rem .7rem; cursor: pointer; }
.copy-command:hover, .copy-command:focus-visible { border-color: var(--accent); }
.copy-feedback { grid-column: 1 / -1; min-height: 1.2em; color: var(--muted); font-size: .78rem; }
.machine-command-custom { margin-top: 0; }
.machine-account-note { grid-column: 1 / -1; margin: .35rem 0 0; color: var(--muted); font-size: .78rem; }
.machine-console { margin-top: .5rem; }
.btn-console { display: inline-block; padding: .35rem .7rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font-size: .85rem; text-decoration: none; }
.btn-console:hover, .btn-console:focus-visible { border-color: var(--accent); }
.machine-console-note { margin: .35rem 0 0; color: var(--muted); font-size: .78rem; }
.btn-clear-accounts { min-height: 2.1rem; padding: .3rem .7rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; cursor: pointer; }
.btn-clear-accounts:hover, .btn-clear-accounts:focus-visible { border-color: var(--accent); }
.account-panel-status { display: block; margin-top: .35rem; color: var(--muted); font-size: .78rem; }
.empty { grid-column: 1 / -1; text-align: center; padding: 4rem 1.5rem; color: var(--muted); }
.machine-empty { padding-block: 2rem; }
.empty-icon { font-size: 2.5rem; line-height: 1; margin-bottom: .5rem; opacity: .5; }
.empty p { margin: 0; }
.bottom-nav { display: none; }
@media (max-width: 600px) {
  header { padding: 1.5rem 1rem .75rem; align-items: flex-start; }
  main { padding: 1rem 1rem 2rem; }
  .header-side { width: 100%; justify-content: space-between; align-items: flex-start; flex-wrap: wrap; }
  .user { text-align: left; max-width: 100%; }
  .user-name, .user .login { white-space: normal; overflow-wrap: anywhere; }
  .grid { grid-template-columns: minmax(0, 1fr); }
  .card-head { align-items: flex-start; flex-wrap: wrap; }
  .card-name { white-space: normal; overflow-wrap: anywhere; }
  .health-status { margin-left: 0; text-align: left; }
  .machine-command { grid-template-columns: minmax(0, 1fr); align-items: stretch; }
  .machine-command label, .copy-feedback { grid-column: 1; }
  body { padding-bottom: calc(4.75rem + env(safe-area-inset-bottom)); }
  .account { width: 100%; }
  .account-trigger { width: 100%; text-align: left; }
  .account-panel { position: fixed; top: auto; left: 1rem; right: 1rem; bottom: calc(4.5rem + env(safe-area-inset-bottom)); width: auto; max-width: none; max-height: min(70vh, 32rem); overflow-y: auto; z-index: 40; }
  .bottom-nav { display: flex; position: fixed; left: 0; right: 0; bottom: 0; z-index: 30; justify-content: space-around; gap: .25rem; padding: .35rem max(.5rem, env(safe-area-inset-right)) calc(.35rem + env(safe-area-inset-bottom)) max(.5rem, env(safe-area-inset-left)); border-top: 1px solid var(--border); background: var(--card-bg); box-shadow: 0 -8px 24px rgba(0, 0, 0, .2); }
  .bottom-nav-item { display: flex; flex: 1 1 0; min-width: 0; min-height: 44px; flex-direction: column; align-items: center; justify-content: center; gap: .12rem; padding: .3rem .2rem; border: 0; border-radius: 9px; background: transparent; color: var(--muted); text-decoration: none; font: inherit; cursor: pointer; }
  .bottom-nav-item:hover, .bottom-nav-item:focus-visible { color: var(--text); background: var(--card-hover-bg); }
  .bottom-nav-icon { font-size: 1rem; line-height: 1; }
  .bottom-nav-label { font-size: .7rem; font-weight: 650; line-height: 1.15; }
}
</style>
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
<div class="portal-content" id="portal-content" hx-get="/portal" hx-trigger="every 60s" hx-target="#portal-content" hx-select="#portal-content" hx-swap="outerHTML">
<section class="portal-section" aria-labelledby="services-heading">
<h2 class="section-title" id="services-heading">Services</h2>
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

  function syncBottomNavMachines() {
    var machinesLink = document.getElementById("bottom-nav-machines");
    if (machinesLink) machinesLink.hidden = !document.getElementById("machines-heading");
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
  });

  document.body.addEventListener("htmx:afterSwap", syncBottomNavMachines);
  syncBottomNavMachines();
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

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy-ssh-command]");
    if (!button) return;

    var select = document.getElementById(button.getAttribute("data-user-select"));
    var feedback = document.getElementById(button.getAttribute("aria-describedby"));
    var option = select && select.options[select.selectedIndex];
    var command = option && option.getAttribute("data-command");
    if (!feedback || !command || command.indexOf("tailscale ssh ") !== 0) return;

    feedback.textContent = "Copying command...";
    copyText(command).then(function () {
      feedback.textContent = "Command copied.";
    }, function () {
      feedback.textContent = "Copy unavailable.";
    });
  });
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

  document.addEventListener("click", function (event) {
    var button = event.target.closest("[data-copy-ssh-account]");
    if (!button) return;

    var input = document.getElementById(button.getAttribute("data-account-input"));
    var feedback = document.getElementById(button.getAttribute("aria-describedby"));
    if (!input || !feedback) return;

    var value = input.value;
    var target = input.getAttribute("data-account-target");
    var copyText = window.__velociportalCopyText;
    if (!isValidAccount(value) || !target || !copyText) {
      feedback.textContent = "Enter a valid account name.";
      return;
    }

    var command = buildAccountCommand(value, target);
    feedback.textContent = "Copying command...";
    copyText(command).then(function () {
      feedback.textContent = "Command copied.";
      rememberAccount(value);
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
  });

  refreshAccountUI();
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
