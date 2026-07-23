# CLAUDE.md — Velociportal

## What this project is

Velociportal is a self-hosted, identity-aware service dashboard that bridges **Headscale/Tailscale** and **Nginx Proxy Manager (NPM)**. It reads your Tailscale ACL policy (groups, users, access rules) and NPM's proxy-host list, correlates the two, and renders a per-user portal where each user sees only the services their ACL group grants access to. It is a **visibility layer, not an auth layer**: it complements identity providers (Authentik, Keycloak, Authelia) rather than replacing them. The IdP still handles authentication, SSO, and access enforcement — Velociportal just makes the dashboard reflect what the network already permits, using your ACLs as the single source of truth so there is no separate visibility config to maintain.

## Read before acting

1. `README.md` — the vision/concept doc and current source of truth for scope, the "how it works" data flow, and the alternatives comparison (why this complements an IdP, not replaces it).
2. `knowledgebase/04-handoff-context.md` — hot context: current state, next steps, open questions. Read this first when picking up work.
3. `knowledgebase/` — numbered design docs. Key files:
   - `00-concept-source.md` — the "why" and problem statement
   - `01-api-research.md` — Headscale + NPM API details, endpoints, auth
   - `02-design-decisions.md` — locked decisions (don't reopen without user OK)
   - `03-prior-art.md` — similar tools and lessons
   - `05-deep-research.md` — adversarially verified research report (103-agent deep research)
4. `velociportal.portagenty.toml` — workspace/session config (portagenty). Do not hand-edit unless changing the workspace layout.

## Current stage

**Sprint 1 complete — core implementation exists and is tested.** The codebase has a working Go binary with API clients, ACL matching, identity middleware, and a server-rendered portal. 58 tests pass with `-race`. CI runs on push/PR. See `knowledgebase/04-handoff-context.md` for detailed state. Confirm changes against the hard constraints below and record non-trivial decisions in `knowledgebase/`.

## Hard constraints (locked)

- **Complements IdPs, does not replace them.** Velociportal is a visibility layer, never an auth layer. It does no login, SSO, OIDC/SAML, or request enforcement — those stay with the IdP and forward-auth middleware.
- **Single Docker container.** One static binary in a minimal image (`FROM scratch`/distroless), deployable on TrueNAS Scale. No multi-service compose stack for the app itself.
- **Reads from Headscale API + NPM API only.** No database for service config. Headscale `GET /api/v1/policy` (Bearer API key) supplies groups/tagOwners/acls/users; NPM `GET /api/nginx/proxy-hosts` (JWT from `POST /api/tokens`) supplies services. State is an in-memory cache refreshed on a ticker, not persisted config.
- **Tailscale identity headers for user identification.** Trust `Tailscale-User-Login` (and siblings) ONLY when the request arrives from the trusted Serve/forward-auth proxy; reject/ignore those headers on any other path. Bind to the internal network, never `0.0.0.0` on the LAN. Spoofed identity headers are the core threat model. Do authorization server-side before rendering — never rely on hiding cards in client HTML.
- **Simple over clever — minimal dependencies.** Prefer Go standard library + templ + htmx (server-rendered, no SPA). Two upstream API clients (`net/http` + `encoding/json`) and a background poll goroutine. Add a dependency only when the stdlib genuinely falls short.
- **No AI attribution in commits.** Never add Claude/Anthropic/any AI as co-author or contributor.

## Architecture sketch

```
Headscale API ─ GET /api/v1/policy ─┐
   (groups, users, acls, tagOwners) │
                                     ├─▶ Velociportal
NPM API ─ GET /api/nginx/proxy-hosts┘   • background goroutine polls both on a
   (services, domains, access_list_id)    time.Ticker → in-memory cache (RWMutex)
                                          • per-request: read identity header from
                                            trusted proxy → resolve user's ACL group
                                            → filter cached services to authorized set
                                            → templ renders that subset only
                                                        │
                                                        ▼
                                        Per-user portal (each user sees only
                                        the services their ACL group can reach)
```

Requests are always served from the cache, so a slow or unreachable upstream never blocks the page; upstream calls carry per-request context timeouts. Language default is Go (single static binary); FastAPI + Jinja2 + htmx is the only sanctioned fallback, and only if the maintainer is decisively more comfortable in Python. Rust and Node are out for the primary service.
