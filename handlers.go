package main

import (
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type PortalHandler struct {
	cache *Cache
}

func NewPortalHandler(cache *Cache) *PortalHandler {
	return &PortalHandler{cache: cache}
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
	slog.Info("portal request", "login", identity.Login, "cards", len(cards))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderPortal(w, identity, cards); err != nil {
		slog.Error("render portal", "login", identity.Login, "err", err)
	}
	slog.Debug("portal rendered", "login", identity.Login, "duration", time.Since(start))
}

func renderPortal(w io.Writer, id *Identity, cards []ServiceCard) error {
	var body strings.Builder
	rendered := 0
	for _, c := range cards {
		// Scheme allowlist: only render cards whose URL is an http(s) link.
		// A malicious NPM entry could otherwise inject a javascript:/data: URL.
		if !strings.HasPrefix(c.URL, "http://") && !strings.HasPrefix(c.URL, "https://") {
			slog.Warn("skipping card with disallowed URL scheme", "service", c.Name)
			continue
		}

		// The scheme shown on the badge is derived from the (already
		// allowlisted) URL, so it can only ever be "http" or "https".
		scheme := "https"
		if strings.HasPrefix(c.URL, "http://") {
			scheme = "http"
		}

		online := "false"
		if c.Online {
			online = "true"
		}

		fmt.Fprintf(&body,
			`<a class="card" href="%s" data-service="%s" data-online="%s">`+
				`<span class="card-head">`+
				`<span class="status-dot" aria-hidden="true"></span>`+
				`<span class="card-name">%s</span>`+
				`</span>`+
				`<span class="badge">%s</span>`+
				`</a>`,
			html.EscapeString(c.URL),
			html.EscapeString(c.Name),
			online,
			html.EscapeString(c.Name),
			html.EscapeString(scheme),
		)
		rendered++
	}

	if rendered == 0 {
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
  --dot-online: #3fb950;
  --dot-offline: #484f58;
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
    --dot-online: #2ea043;
    --dot-offline: #c2c8d0;
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
  --dot-online: #2ea043;
  --dot-offline: #c2c8d0;
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
  --dot-online: #3fb950;
  --dot-offline: #484f58;
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
.card:hover { border-color: var(--accent); background: var(--card-hover-bg); transform: translateY(-2px); }
.card-head { display: flex; align-items: center; gap: .5rem; min-width: 0; }
.card-name { font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.status-dot { width: 8px; height: 8px; border-radius: 50%; flex-shrink: 0; background: var(--dot-offline); }
.card[data-online="true"] .status-dot { background: var(--dot-online); box-shadow: 0 0 6px var(--dot-online); }
.badge { align-self: flex-start; padding: .1rem .5rem; border-radius: 999px; font-size: .72rem; font-weight: 600; letter-spacing: .02em; text-transform: uppercase; background: var(--badge-bg); color: var(--badge-text); }
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
