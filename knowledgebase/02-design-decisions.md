# 02 — Design Decisions (Locked)

> Decisions that are settled. Revisit only with a stated reason. "Locked" means
> don't relitigate casually during implementation.

## D1 — Complementary to IdPs; visibility layer only

Velociportal decides **what shows on the dashboard**, nothing more. Authentication,
SSO, and access *enforcement* stay with the IdP (Authentik/Authelia/Keycloak) and
the Tailscale ACL. **Hiding a service card is UX, not security** — the ACL and
forward-auth are the real gate. We never present card-filtering as an access
control.

## D2 — Deployment: single container

One static binary in one container, primary target **TrueNAS Scale**.

- Multi-stage Docker build: a Go builder stage runs `templ generate` +
  `CGO_ENABLED=0 go build`, final stage is `FROM scratch` (or distroless/static)
  holding just the binary + a CA-cert bundle. Smallest, most reproducible artifact,
  near-zero OS attack surface.
- **Secrets** (Headscale API key, NPM credentials) come from **env vars or a mounted
  file** (TrueNAS app config / Docker secret) — never baked into the image or the
  build context.

## D3 — Data sources: Headscale API + NPM API, no config DB

The upstream APIs **are** the source of truth. Velociportal keeps **no separate
database** for service or permission config.

- Groups/users/ACL rules  ← Headscale `GET /api/v1/policy` (parse the JSON).
- Services/domains         ← NPM `GET /api/nginx/proxy-hosts`.
- All state lives in an **in-memory cache**, refreshed on an interval. Anything we
  persist later (custom icons, descriptions, categories) is metadata *decoration*,
  not a second permission model.

**Polling model:** a single background goroutine with a `time.Ticker` refreshes the
cache (guarded by `sync.RWMutex` or an atomic pointer); dashboard requests serve
from cache so a slow/unreachable upstream never blocks the page. Per-request
`context` timeouts on all upstream HTTP calls. Interval 30–60s.

## D4 — Identity via Tailscale headers

User identity comes from **Tailscale identity headers**, not a login form.

- Behind **Tailscale Serve**: `Tailscale-User-Login`, `Tailscale-User-Name`,
  `Tailscale-User-Profile-Pic`. Serve **strips incoming `Tailscale-*` headers**
  before injecting its own (anti-spoofing). Values may be RFC2047 Q-encoded.
- Behind a plain reverse proxy: the tailscaled LocalAPI whois pattern
  (`X-Webauth-User`/`-Login`/`-Tailnet`, à la `tailscale/cmd/nginx-auth`). Headscale
  does **not** emit Serve headers itself — they come from the local tailscaled
  daemon, which works the same against a Headscale control server.
- **Threat model (core):** identity headers are authoritative **only** when the
  request arrives from the trusted proxy. Bind the container to the internal
  Docker/Tailscale network (**never `0.0.0.0` on the LAN**) and
  **reject/ignore `Tailscale-*` headers on any other path**. Spoofed identity
  headers are the primary risk. Use `Tailscale-Tailnet`/`X-Webauth-Tailnet` to
  reject shared-in nodes from other tailnets for admin gating.

## D5 — Tech stack: Go + templ + htmx

From research, the recommended primary stack:

- **Go + `templ` + `htmx`**, compiled to a single static binary with templates and
  `htmx.min.js` embedded via `go:embed`.
- Stdlib for HTTP (Go 1.22+ `ServeMux` with method+path patterns), the two API
  clients (`net/http` + `encoding/json`), and scheduling. Third-party deps limited
  to `templ` (build-time) and a router only if `ServeMux` isn't enough.
- Server-rendered, no SPA. **Authorization happens server-side in Go before
  rendering** — map the user to their allowed service set and let templ render only
  that subset. Never filter in the client (htmx/HTML is fully visible).
- **Fallback:** FastAPI + Jinja2 + htmx **only if** the maintainer is decisively
  more comfortable in Python (accepting a bigger image + Python runtime). Do **not**
  pick Rust/Axum (needless complexity for I/O-bound work) or Node/Deno (reintroduces
  the npm supply chain) as primary.

> Note on the maintainer's usual Rust preference: this is an I/O-bound glue service
> where the Go + templ + htmx ecosystem (single static binary, `go:embed`, tiny
> `scratch` image) is the stronger fit. Go is the deliberate call here.

## D6 — ACL matching approach

Correlate ACL policy with NPM proxy hosts to decide each user's card set:

1. Parse the Headscale policy → resolve `groups` to their member users, note
   `tagOwners` and `acls` rules.
2. Resolve the requesting user (from Tailscale headers) → the set of groups/tags
   they belong to.
3. For each NPM proxy host, determine which groups/tags/users may reach it. Two
   correlation signals:
   - **ACL rules** targeting the service's host/tag/CIDR (the network-level truth).
   - **NPM `access_list_id`** on the proxy host (usernames + IP `allow`/`deny`
     rules) as a secondary signal.
4. A card renders for a user iff the ACL grants that user's identity a path to the
   service. Enforcement still lives in the ACL/forward-auth — this only chooses what
   to show.

**Open detail** (see 04): the exact join between an NPM proxy host and an ACL rule
(match on forward_host/domain vs. tag vs. CIDR) needs prototyping against real data.
