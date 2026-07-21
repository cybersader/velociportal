package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const staleAfter = 5 * time.Minute

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

	cards := MatchServices(identity, data)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderPortal(w, identity, cards); err != nil {
		slog.Error("render portal", "login", identity.Login, "err", err)
	}
}

type HealthHandler struct {
	cache *Cache
}

func (h *HealthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data := h.cache.Get()

	switch {
	case data == nil || data.UpdatedAt.IsZero():
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable",
		})
	case time.Since(data.UpdatedAt) > staleAfter:
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":      "stale",
			"last_update": data.UpdatedAt.UTC().Format(time.RFC3339),
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{
			"status":      "ok",
			"last_update": data.UpdatedAt.UTC().Format(time.RFC3339),
			"services":    len(data.ProxyHosts),
			"users":       len(data.Users),
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode json", "err", err)
	}
}

func renderPortal(w io.Writer, id *Identity, cards []ServiceCard) error {
	var body strings.Builder
	if len(cards) == 0 {
		body.WriteString(`<p class="empty">No services are available to your account.</p>`)
	}
	for _, c := range cards {
		fmt.Fprintf(&body,
			`<a class="card" href="%s" data-service="%s"><span class="card-name">%s</span></a>`,
			html.EscapeString(c.URL),
			html.EscapeString(c.Name),
			html.EscapeString(c.Name),
		)
	}

	page := fmt.Sprintf(portalPage,
		html.EscapeString(id.Name),
		html.EscapeString(id.Login),
		body.String(),
	)
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
<title>Velociportal</title>
<style>
:root { color-scheme: light dark; }
* { box-sizing: border-box; }
body { margin: 0; font: 16px/1.5 system-ui, sans-serif; background: #0f1115; color: #e6e6e6; }
header { padding: 2rem 1.5rem 1rem; }
header h1 { margin: 0 0 .25rem; font-size: 1.25rem; }
header .login { color: #9aa4b2; font-size: .9rem; }
main { padding: 1rem 1.5rem 2.5rem; }
.grid { display: grid; gap: 1rem; grid-template-columns: repeat(auto-fill, minmax(200px, 1fr)); }
.card { display: flex; flex-direction: column; gap: .35rem; padding: 1rem 1.1rem; border: 1px solid #232a35; border-radius: 12px; background: #161a22; color: inherit; text-decoration: none; transition: border-color .15s, transform .15s; }
.card:hover { border-color: #3b82f6; transform: translateY(-2px); }
.card-name { font-weight: 600; }
.empty { color: #9aa4b2; }
</style>
</head>
<body>
<header>
<h1>%s</h1>
<div class="login">%s</div>
</header>
<main>
<div class="grid" id="services" hx-get="/portal" hx-trigger="every 60s" hx-target="#services" hx-select="#services > *" hx-swap="innerHTML">
%s
</div>
</main>
<script src="/static/htmx.min.js"></script>
</body>
</html>`
