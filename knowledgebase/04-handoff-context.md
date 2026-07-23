# 04 — Handoff Context

> Hot context for whoever picks this up next. Read this first when work starts.
> (Design docs 00–03 hold the stable reasoning; this holds the current state.)

## Current stage

**Sprint 1 complete. Core implementation exists and is tested.** The codebase has
a working Go binary with all major subsystems implemented: API clients (Headscale +
NPM), background polling cache, identity middleware, ACL-to-service matching engine,
and a server-rendered portal with htmx auto-refresh.

58 unit and integration tests pass with `-race`. CI runs on push/PR via GitHub
Actions (vet + test + build + Docker verify).

## What's already decided (don't relitigate — see 02)

- Visibility layer **only**; complements IdPs (Authentik/Authelia), never replaces.
- Single container, `FROM scratch`, target TrueNAS Scale.
- Data sources: Headscale `/api/v1/policy` + NPM `/api/nginx/proxy-hosts`. No config DB.
- Identity via Tailscale headers; headers trusted only from the proxy path.
- Stack: **Go 1.22 + stdlib HTTP + htmx**, static binary, zero external deps.
- Server-side authorization before render; card-hiding is UX, not enforcement.

## What's implemented

- **Headscale client** (`headscale.go`): fetches policy (huJSON-aware), users, nodes.
  Unwraps gRPC-gateway envelopes. Structured slog logging.
- **NPM client** (`npm.go`): JWT auth with auto-reauth on 401. Fetches proxy hosts
  and access lists. Structured logging.
- **Cache** (`cache.go`): background goroutine on `time.Ticker`, atomic pointer swap,
  per-request context timeouts. Partial failure keeps stale data.
- **Identity middleware** (`auth.go`): trusts `Tailscale-User-*` headers only from
  `TRUSTED_PROXY_CIDR`. Logs identity extraction at debug level.
- **ACL matcher** (`matcher.go`): resolves user → groups, matches against ACL rules.
  Supports exact IPs, CIDRs, tags (resolved to node IPs), Policy.Hosts aliases,
  `autogroup:internet`, `autogroup:self`. IPv6-safe `stripPort`.
- **Portal handler** (`handlers.go`): renders per-user service cards. Scheme
  allowlist (http/https only). Favicon. Request logging with timing.
- **Deployment**: multi-stage Dockerfile (non-root, scratch), docker-compose.yml,
  .env.example, GitHub Actions CI, Makefile with run/docker/test targets.

## Test coverage

| File | Tests | Status |
|---|---|---|
| matcher.go | matcher_test.go | Thorough (IP, CIDR, tags, hosts, autogroup, IPv6, tagOwners) |
| auth.go | auth_test.go | Core threat model (trusted/untrusted/spoofed/missing) |
| main.go (config) | config_test.go | Required env, defaults, invalid inputs |
| headscale.go | headscale_test.go | httptest (policy/huJSON/users/nodes/errors/auth header) |
| npm.go | npm_test.go | httptest (auth flow, token reuse, 401 reauth, access lists) |
| handlers.go | handlers_test.go | Full flow (per-user visibility, XSS, scheme, empty cache) |
| cache.go | — | **Untested** (refresh, ticker, partial failure) |

## What to do next

1. **Test against real APIs.** Deploy with a real Headscale + NPM instance. The
   huJSON parsing and ACL matching were built from API docs — real data will reveal
   edge cases in field shapes, user identifier formats, and the ACL↔proxy-host join.
2. **Online/offline status indicators.** Cards have the `Online` field from NPM's
   `meta.nginx_online` but no visual indicator in the template.
3. **Light mode CSS.** Template declares `color-scheme: light dark` but only has dark
   styles. Add a `prefers-color-scheme: light` media query.
4. **Service metadata config.** Neither API provides icons or descriptions. Design a
   small mounted config file for decoration (icons, display names, categories) that
   does not become a second permission model.
5. **Cache tests.** `cache.go` has zero test coverage — test refresh, partial failure
   behavior, and the ticker goroutine.
6. **Docker Compose deployment example validation.** Verify the compose file works
   end-to-end with real services.
7. **Health check dashboard.** The inline `/healthz` works but could surface more
   detail (per-upstream status, last error, cache age).

## Key questions still open

- **ACL ↔ proxy-host join refinement:** current matcher uses `ForwardHost` (the NPM
  backend IP). Real deployments may need domain-name-based matching or NPM
  `access_list_id` as a secondary signal. Prototype against real data.
- **`autogroup:internet` semantics:** currently treated as match-all (`*`). In real
  Tailscale it means exit-node traffic to the public internet. May over-grant
  visibility for policies that use it.
- **Headscale vs Tailscale SaaS:** primary target is self-hosted Headscale (Bearer
  auth). Tailscale SaaS support (OAuth, 1h token) deferred.

## Pointers

- Vision + comparison table: `README.md`
- Why / problem statement: `knowledgebase/00-concept-source.md`
- API specifics + snippets: `knowledgebase/01-api-research.md`
- Locked decisions: `knowledgebase/02-design-decisions.md`
- Similar tools + reusable clients: `knowledgebase/03-prior-art.md`
- Deep research report: `knowledgebase/05-deep-research.md`
