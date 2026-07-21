# 05 — Deep Research Report

> Adversarially verified research from a 103-agent deep-research workflow.
> Each finding below survived 3-vote adversarial verification (need 2/3 refutations to kill).

## Summary

Self-hosted dashboards do not integrate with Nginx Proxy Manager (NPM) or Headscale/Tailscale through a single official "identity-aware discovery" API; instead a complementary visibility layer stitches together three separate, well-documented surfaces. NPM exposes a full REST API (base /api) secured by RSA-signed JWTs obtained from POST /api/tokens using admin email+password, with readable proxy-host endpoints and access-list endpoints (GET /api/nginx/access-lists[/:id]) gated by access_lists.view/manage permissions — so both service configs and ACL definitions are pullable programmatically. Headscale mirrors Tailscale: a /api/v1 REST API (Swagger-documented, Bearer-token auth) plus a huJSON ACL policy file (src/dst/ip, legacy ACLs and recommended Grants) whose structure is identical to Tailscale's, so ACL parsing tooling transfers. Per-user identity comes from Tailscale Serve, which injects Tailscale-User-Login/Name/Profile-Pic headers for authenticated human users (NOT tagged devices, and NOT over public Funnel), which a reverse proxy like NGINX can forward downstream (e.g. as X-Webauth-*). The simplest architecture is therefore a read-only sidecar that authenticates to NPM and Headscale, reads proxy hosts + access lists + ACLs to compute which services a user may reach, and renders a per-user dashboard keyed off the Serve-injected identity header — complementing, not replacing, the IdP. Existing precedents (Dashly, npm-docker-sync, Erreur32's NPM bash API) prove each piece is scriptable, though none combine all layers.

## Verified Findings

### 1. NPM authentication is credential-based JWT: POST /api/tokens with admin email/password returns an RS256 (RSA-2048) signed JWT used as Authorization: Bearer <token>, with token refresh supported.

**Confidence:** high
**Votes:** claims 14,16,20,21,22 merged (all 3-0)

**Evidence:** NPM has no persistent pre-generated API-token concept; the only auth path is POSTing {identity, secret} (email+password of an existing NPM account) to /api/tokens, which returns an RSA-signed JWT (backend/models/token.js: ALGO='RS256'; config.js generates a 2048-bit RSA keypair to /data/keys.json). getTokenFromEmail handles login, getFreshToken handles refresh. Both the Erreur32 bash wrapper (API_USER/API_PASS, auto-refreshes tokens) and npm-docker-sync (NPM_EMAIL/NPM_PASSWORD) confirm credential-based auth in practice.

**Sources:**
- https://github.com/Erreur32/nginx-proxy-manager-Bash-API
- https://github.com/Redth/npm-docker-sync
- https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/5.2-authentication
- https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/9.7-users-api

### 2. NPM proxy host configurations are fully readable and CRUD-scriptable via its REST API at /api (e.g. /api/nginx/proxy-hosts).

**Confidence:** high
**Votes:** claims 13,17 merged (both 3-0)

**Evidence:** Erreur32's bash API wraps read endpoints as --host-list (all), --host-list-full (JSON), --host-search domain, --host-show [ID] (full config) against base http://[NGINX_IP]:[NGINX_PORT]/api. npm-docker-sync independently proves write access: it watches Docker events, parses container labels into proxy configs, and creates/updates/removes NPM proxy hosts via the API (SHA256 change detection). Together they demonstrate proxy-host configs are readable and mutable programmatically — the core data a discovery layer needs.

**Sources:**
- https://github.com/Erreur32/nginx-proxy-manager-Bash-API
- https://github.com/Redth/npm-docker-sync

### 3. NPM access lists (ACLs) are readable via GET /api/nginx/access-lists (all) and GET /api/nginx/access-lists/:id (one), with POST/PUT/DELETE for management; reads require the access_lists.view permission and writes require access_lists.manage.

**Confidence:** high
**Votes:** claims 15,18,19 merged (15,18 are 3-0; 19 is 2-1). REFUTED sub-claim: the ?expand=owner,items,clients,proxy_hosts query param was NOT confirmed (1-2).

**Evidence:** NPM's own OpenAPI schema (develop branch) defines getAccessLists (GET /nginx/access-lists) and getAccessList (GET /nginx/access-lists/{listID}), both requiring permission access_lists.view, plus POST/PUT/DELETE requiring access_lists.manage (backend/lib/access/*.json + access.js AJV validation). Erreur32's wrapper exposes --access-list, --access-list-show [ID], and full CRUD, plus per-host ACL enable/disable — confirming ACL definitions are pullable to compute per-user visibility. Note the code uses a single 'access_lists' permission key with view/manage levels rather than literal dot-scopes (cosmetic).

**Sources:**
- https://deepwiki.com/NginxProxyManager/nginx-proxy-manager/9.6-access-lists-api
- https://github.com/Erreur32/nginx-proxy-manager-Bash-API

### 4. Existing tools auto-populate dashboards/proxy state from NPM, but none use an official read-only discovery API and none combine NPM+Headscale+identity; approaches vary from direct SQLite reads to API automation.

**Confidence:** high
**Votes:** claims 0,1 (Dashly, 3-0) merged with 16,17 context

**Evidence:** Dashly auto-populates a dashboard by reading NPM's SQLite database file directly via NGINX_DB_PATH (bind-mounted), requiring NO NPM credentials — filesystem access is the documented sole mechanism, updating whenever domains change. npm-docker-sync instead drives the NPM API from Docker labels. These are the closest precedents to 'auto-populate a dashboard from NPM proxy hosts,' proving feasibility, but each covers only the NPM layer — no existing project reads ACLs AND Headscale user-to-node mappings to build per-user dashboards.

**Sources:**
- https://hub.docker.com/r/lklynet/dashly
- https://github.com/lklynet/dashly
- https://github.com/Redth/npm-docker-sync

### 5. Headscale exposes a REST API at /api/v1 (Swagger-documented at /swagger) authenticated with an HTTP Bearer API key (Authorization: Bearer <API_KEY>), created via 'headscale apikeys create'.

**Confidence:** high
**Votes:** claims 2,3 merged (both 3-0)

**Evidence:** headscale.net/stable/ref/api/ states verbatim the endpoint is /api/v1, docs at /swagger, and auth is 'HTTP Bearer authentication by sending the API key with the Authorization: Bearer <API_KEY> header.' Corroborated by the upstream OpenAPI spec (/api/v1/apikey etc.) and DeepWiki. This gives programmatic access to users and nodes for user-to-node mapping.

**Sources:**
- https://headscale.net/stable/ref/api/
- https://github.com/juanfont/headscale/blob/main/gen/openapiv2/headscale/v1/headscale.swagger.json

### 6. Headscale uses Tailscale's exact huJSON ACL policy file format (acls/groups/hosts/tagOwners/grants; src/dst/ip constructs), supporting both legacy ACLs and modern Grants (Grants recommended), so tooling built against Tailscale ACL structure parses Headscale policies.

**Confidence:** medium
**Votes:** claim 5 (3-0) + claim 4 (2-1, medium due to inferential 'tooling applies' breadth)

**Evidence:** headscale.net/stable/ref/policy/ states 'Headscale uses the same huJSON based file format as Tailscale' and 'We recommend the use of Grants since ACLs are considered legacy.' Policy examples show src/dst/ip (e.g. src alice@, dst autogroup:internet, ip *). Structural parsers transfer, but feature/semantic parity is NOT complete: srcPosture/device-posture unsupported, IP sets unsupported, partial autogroup support, wildcard '*' resolves to CGNAT/ULA ranges, and an omitted policy defaults to allow-all — so ACL *meaning* must be interpreted carefully even when structure parses.

**Sources:**
- https://headscale.net/stable/ref/policy/
- https://tailscale.com/docs/features/access-control/grants

### 7. Tailscale Serve injects per-user identity headers — Tailscale-User-Login (email), Tailscale-User-Name (display name), Tailscale-User-Profile-Pic — into proxied tailnet requests, so backends identify the requesting user; this is the identity primitive for per-user dashboards.

**Confidence:** high
**Votes:** claims 6,8 merged (both 3-0)

**Evidence:** Official Serve docs: 'When you use Serve to proxy traffic to a local service... it adds a few Tailscale identity headers' — Tailscale-User-Login (alice@example.com), Tailscale-User-Name (Alice Architect), Tailscale-User-Profile-Pic. Confirmed by the tailscale-dev/id-headers-demo repo. Serve strips spoofed identity headers, so services should listen on localhost.

**Sources:**
- https://tailscale.com/docs/features/tailscale-serve
- https://tailscale.com/docs/concepts/tailscale-identity
- https://tailscale.com/kb/1312/serve

### 8. Serve identity headers are populated ONLY for human users, never for tagged devices, and are NOT included on public Funnel traffic — so identity-aware per-user dashboards work only over tailnet Serve, not Funnel, and only for user (not machine) traffic.

**Confidence:** high
**Votes:** claims 7,9 merged (both 3-0)

**Evidence:** Docs state verbatim 'Identity headers are populated only for users, not tagged devices' and 'Funnel traffic, which is publicly available, does not include identity headers.' Funnel exposes anonymous public users with no tailnet identity; tagged devices need app-capability headers instead (see FR tailscale/tailscale#11723). This bounds the architecture: the identity-aware layer must be reached over Serve, and machine/tagged access won't carry a user identity.

**Sources:**
- https://tailscale.com/docs/concepts/tailscale-identity
- https://tailscale.com/docs/features/tailscale-serve

### 9. A reverse proxy can consume/forward the Tailscale identity downstream: NGINX + tailscale nginx-auth (auth_request to unix:/run/tailscale.nginx-auth.sock) emits Tailscale-User/Login/Name/Tailnet/Profile-Picture headers, which NGINX forwards to apps as X-Webauth-* (e.g. Grafana auth.proxy with header_name=X-WebAuth-User, header_property=username).

**Confidence:** high
**Votes:** claims 10,11,12 merged (all 3-0)

**Evidence:** tailscale nginx-auth uses the NGINX auth_request directive proxying to a UNIX socket (/run/tailscale.nginx-auth.sock, deliberately UNIX to avoid network leakage), decorating requests with Tailscale-User (user@host), Tailscale-Login, Tailscale-Name, Tailscale-Tailnet, Tailscale-Profile-Picture; its documented config re-emits them as X-Webauth-* via auth_request_set + proxy_set_header. Grafana's auth.proxy (header_name=X-WebAuth-User, header_property=username, whitelist=127.0.0.1 to block spoofing) is a concrete downstream consumer. This is the header-forwarding pattern a visibility layer would reuse — but note nginx-auth is marked experimental/'not in the latest module version'.

**Sources:**
- https://pkg.go.dev/tailscale.com/cmd/nginx-auth
- https://tailscale.com/blog/grafana-auth
- https://grafana.com/docs/grafana/latest/setup-grafana/configure-security/configure-authentication/auth-proxy/

## Caveats

Time-sensitivity and source-quality notes: (1) The tailscale nginx-auth tool is officially marked experimental and 'not in the latest version of its module' — the integration mechanism is accurate but the tool may be unmaintained; treat as a pattern, not a supported product. (2) Several NPM endpoint/permission details rest on DeepWiki (a secondary AI-generated wiki), though each was independently corroborated against NPM's own OpenAPI schema and backend source on GitHub, raising effective confidence. (3) Claim 19 (access_lists.view/manage) and claim 4 (Headscale ACL tooling parity) were 2-1 split votes; the ACL claim holds structurally but Headscale has real feature/semantic divergences from Tailscale (postures, IP sets, wildcard resolution, default-allow) that any ACL interpreter must handle. (4) NPM's server-side JWT expiry enforcement is known to be loose. (5) The '?expand=...' access-list query param that would let a dashboard see which proxy hosts use each list was REFUTED (1-2) — that convenient linkage may not exist, so mapping access-lists to proxy-hosts likely requires cross-referencing both endpoints manually. (6) No existing project was found that combines NPM configs + NPM ACLs + Headscale user-to-node mappings + Tailscale identity headers into one per-user dashboard; the architecture is assembled from independently-verified primitives, not a proven end-to-end reference implementation. (7) NPM auth is credential-only (admin email/password) — there is no scoped read-only API token, so a visibility layer must store admin-equivalent NPM credentials, a security consideration.

