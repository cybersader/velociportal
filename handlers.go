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
	slog.Info("portal request", "cards", len(cards))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderPortal(w, identity, cards, h.health); err != nil {
		slog.Error("render portal", "err", err)
	}
	slog.Debug("portal rendered", "duration", time.Since(start))
}

func renderPortal(w io.Writer, id *Identity, cards []ServiceCard, healthStores ...*ServiceHealthStore) error {
	var health *ServiceHealthStore
	if len(healthStores) > 0 {
		health = healthStores[0]
	}

	var body strings.Builder
	for _, card := range cards {
		healthMarkup := renderServiceHealthStatus(health, card.ID)
		scheme, linkable := cardURLScheme(card.URL)
		if card.LinkState != serviceLinkReady {
			linkable = false
		}
		if linkable {
			fmt.Fprintf(&body,
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
			continue
		}

		if card.LinkState == serviceLinkReady {
			slog.Warn("rendering card with invalid browser URL as unlinked", "proxy_host_id", card.ID)
		}
		fmt.Fprintf(&body,
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

	if len(cards) == 0 {
		body.WriteString(`<div class="empty">` +
			`<div class="empty-icon" aria-hidden="true">&#9671;</div>` +
			`<p>No services are available to your account.</p>` +
			`</div>`)
	}

	page := strings.NewReplacer(
		"{{USER_NAME}}", html.EscapeString(id.Name),
		"{{USER_LOGIN}}", html.EscapeString(id.Login),
		"{{BODY}}", body.String(),
	).Replace(portalPage)

	if _, err := io.WriteString(w, page); err != nil {
		return fmt.Errorf("renderPortal: %w", err)
	}
	return nil
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
.brand-name { font-size: 1.25rem; font-weight: 700; letter-spacing: -.01em; }
.user { text-align: right; min-width: 0; }
.user-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.user .login { color: var(--muted); font-size: .85rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
main { max-width: 1200px; margin: 0 auto; padding: 1rem 1.5rem 2.5rem; }
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
.empty { grid-column: 1 / -1; text-align: center; padding: 4rem 1.5rem; color: var(--muted); }
.empty-icon { font-size: 2.5rem; line-height: 1; margin-bottom: .5rem; opacity: .5; }
.empty p { margin: 0; }
@media (max-width: 480px) {
  header { padding: 1.5rem 1rem .75rem; }
  main { padding: 1rem 1rem 2rem; }
  .user { text-align: left; }
}
</style>
</head>
<body>
<header>
<div class="brand">
<img class="brand-logo" src="/static/logo.svg" alt="">
<span class="brand-name">Velociportal</span>
</div>
<div class="user">
<div class="user-name">{{USER_NAME}}</div>
<div class="login">{{USER_LOGIN}}</div>
</div>
</header>
<main>
<div class="grid" id="services" hx-get="/portal" hx-trigger="every 60s" hx-target="#services" hx-select="#services > *" hx-swap="innerHTML">
{{BODY}}
</div>
</main>
<script src="/static/htmx.min.js"></script>
</body>
</html>`
