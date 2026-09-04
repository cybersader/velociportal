package main

import (
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
	cache  *Cache
	health *ServiceHealthStore
}

func NewPortalHandler(cache *Cache) *PortalHandler {
	return NewPortalHandlerWithHealth(cache, nil)
}

func NewPortalHandlerWithHealth(cache *Cache, health *ServiceHealthStore) *PortalHandler {
	return &PortalHandler{cache: cache, health: health}
}

func (h *PortalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	slog.Info("portal request", "cards", len(cards), "machines", len(machines))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderPortalWithMachines(w, identity, cards, machines, machinesAvailable, serviceMetadataHasOrganization(data.ServiceMetadata), h.health); err != nil {
		slog.Error("render portal", "err", err)
	}
	slog.Debug("portal rendered", "duration", time.Since(start))
}

func renderPortal(w io.Writer, id *Identity, cards []ServiceCard, healthStores ...*ServiceHealthStore) error {
	return renderPortalWithOrganization(w, id, cards, serviceCardsHaveOrganizationMetadata(cards), healthStores...)
}

func renderPortalWithOrganization(w io.Writer, id *Identity, cards []ServiceCard, organized bool, healthStores ...*ServiceHealthStore) error {
	return renderPortalWithMachines(w, id, cards, nil, false, organized, healthStores...)
}

func renderPortalWithMachines(w io.Writer, id *Identity, cards []ServiceCard, machines []MachineCard, machinesAvailable, organized bool, healthStores ...*ServiceHealthStore) error {
	var health *ServiceHealthStore
	if len(healthStores) > 0 {
		health = healthStores[0]
	}

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
			renderMachineCard(&machinesBody, machine, index+1)
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
				`<div class="grid" id="machines">%s</div>`+
				`</section>`,
			machinesBody.String(),
		)
	}

	page := strings.NewReplacer(
		"{{USER_NAME}}", html.EscapeString(id.Name),
		"{{USER_LOGIN}}", html.EscapeString(id.Login),
		"{{SERVICES_CLASS}}", servicesClass,
		"{{SERVICES_BODY}}", servicesBody.String(),
		"{{MACHINES_SECTION}}", machinesSection.String(),
	).Replace(portalPage)

	if _, err := io.WriteString(w, page); err != nil {
		return fmt.Errorf("renderPortal: %w", err)
	}
	return nil
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

func renderMachineCard(body *strings.Builder, machine MachineCard, index int) {
	machineID := fmt.Sprintf("machine-%d", index)
	fmt.Fprintf(body,
		`<article class="card machine-card" data-machine="%s" aria-labelledby="%s-name">`+
			`<span class="card-head"><span class="card-name" id="%s-name">%s</span></span>`+
			`<p class="machine-policy">Policy allows SSH access to this machine.</p>`+
			`<ul class="machine-access" aria-label="Allowed SSH accounts">`,
		html.EscapeString(machine.Name),
		machineID,
		machineID,
		html.EscapeString(machine.Name),
	)

	literalUsers := make([]string, 0, len(machine.Access))
	for _, access := range machine.Access {
		account := access.User
		if account == machineNonrootSelector {
			account = "any non-root account"
		} else if _, ok := machineSSHCommand(access.User, machine.Target); ok {
			literalUsers = append(literalUsers, access.User)
		}
		fmt.Fprintf(body,
			`<li><span class="machine-user-summary">%s</span><span class="badge machine-action">%s</span></li>`,
			html.EscapeString(account),
			html.EscapeString(access.Action),
		)
	}
	body.WriteString(`</ul>`)

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
	body.WriteString(`</article>`)
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
<meta name="viewport" content="width=device-width, initial-scale=1">
<link rel="icon" type="image/svg+xml" href="/static/logo.svg">
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
.logo-toggle { border: 1px solid var(--border); border-radius: 999px; padding: .35rem .7rem; background: var(--card-bg); color: var(--muted); font: inherit; font-size: .78rem; line-height: 1.25; cursor: pointer; }
.logo-toggle:hover, .logo-toggle:focus-visible { border-color: var(--accent); color: var(--text); }
.user { text-align: right; min-width: 0; }
.user-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.user .login { color: var(--muted); font-size: .85rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
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
.machine-card { min-width: 0; }
.machine-policy { margin: 0; color: var(--muted); font-size: .88rem; }
.machine-access { display: grid; gap: .4rem; margin: .15rem 0 .25rem; padding: 0; list-style: none; }
.machine-access li { display: flex; align-items: center; justify-content: space-between; gap: .75rem; min-width: 0; }
.machine-user-summary { overflow-wrap: anywhere; }
.machine-action { flex-shrink: 0; }
.machine-command { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: .45rem .6rem; align-items: end; margin-top: .3rem; }
.machine-command label { grid-column: 1 / -1; color: var(--muted); font-size: .78rem; font-weight: 600; }
.machine-command select, .copy-command { min-height: 2.35rem; border: 1px solid var(--border); border-radius: 8px; background: var(--card-bg); color: var(--text); font: inherit; }
.machine-command select { min-width: 0; padding: .35rem .5rem; }
.copy-command { padding: .35rem .7rem; cursor: pointer; }
.copy-command:hover, .copy-command:focus-visible { border-color: var(--accent); }
.copy-feedback { grid-column: 1 / -1; min-height: 1.2em; color: var(--muted); font-size: .78rem; }
.empty { grid-column: 1 / -1; text-align: center; padding: 4rem 1.5rem; color: var(--muted); }
.machine-empty { padding-block: 2rem; }
.empty-icon { font-size: 2.5rem; line-height: 1; margin-bottom: .5rem; opacity: .5; }
.empty p { margin: 0; }
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
}
</style>
</head>
<body>
<header>
<div class="brand">
<img class="brand-logo" id="brand-logo" src="/static/logo.svg" alt="">
<span class="brand-name">Velociportal</span>
</div>
<div class="header-side">
<button class="logo-toggle" id="logo-toggle" type="button" aria-controls="brand-logo" aria-pressed="true" hidden>Show logo</button>
<div class="user">
<div class="user-name">{{USER_NAME}}</div>
<div class="login">{{USER_LOGIN}}</div>
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
<script src="/static/htmx.min.js"></script>
<script>
(function () {
  "use strict";
  var storageKey = "velociportal.logo.visible";
  var root = document.documentElement;
  var button = document.getElementById("logo-toggle");
  var logo = document.getElementById("brand-logo");
  if (!button || !logo) return;

  var visible = true;
  try {
    visible = window.localStorage.getItem(storageKey) !== "false";
  } catch (_) {
    visible = true;
  }

  function applyLogoPreference() {
    if (visible) root.removeAttribute("data-logo");
    else root.setAttribute("data-logo", "hidden");
    button.setAttribute("aria-pressed", String(visible));
  }

  button.addEventListener("click", function () {
    visible = !visible;
    applyLogoPreference();
    try {
      window.localStorage.setItem(storageKey, String(visible));
    } catch (_) {}
  });
  applyLogoPreference();
  button.hidden = false;
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
</script>
</body>
</html>`
