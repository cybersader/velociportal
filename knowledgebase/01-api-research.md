# 01 — API Research

> What we know about the two upstream APIs Velociportal reads. Confirm exact schema
> against your own server's OpenAPI docs — response fields evolve between versions.

## Headscale API (self-hosted control plane)

**Auth:** HTTP **Bearer** token. Create a key with `headscale apikeys create`, then
send `Authorization: Bearer <API_KEY>` on every request. (Distinct from Tailscale
SaaS, which uses HTTP Basic.)

**Modern API (v0.26+/v0.28+):** native REST replacing the old gRPC-gateway. Serves
an OpenAPI 3.1 spec at `/api/v1/openapi` and interactive docs at `/api/v1/docs`
(older builds: `/swagger`). **Always confirm your version's schema there.**

### Key endpoints

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/policy` | Read the ACL policy (huJSON/JSON: `groups`, `tagOwners`, `acls`, `hosts`, `ssh`). **This is the core read.** |
| PUT | `/api/v1/policy` | Update the policy — only when `policy.mode=database` (file mode is API-read-only). We don't need writes. |
| GET | `/api/v1/user` | List users. NOTE: group membership is NOT here — it lives inside the policy document. |
| GET | `/api/v1/node` | List nodes; each has owner user, `forcedTags`/`validTags`, routes. |
| POST | `/api/v1/node/{id}/tags` | Set/replace forced tags on a node. |
| GET | `/api/v1/preauthkey` | List pre-auth keys. |
| GET | `/api/v1/apikey` | List admin API keys. |

### Data-shape notes

- **Policy storage modes:** file (`policy.path`, huJSON, reload required, API
  read-only) *or* database (`policy.mode=database`, required for API writes / web
  UIs). With **no policy defined, the default is allow-all** between nodes.
- **Groups live inside the policy** `groups` object, e.g.
  `"group:dev": ["alice@", "bob@"]`. Groups **cannot nest** other groups.
- **`tagOwners`** maps a tag (e.g. `tag:server`) to users/groups allowed to assign
  it.
- **Stale tags caveat:** Headscale has historically required a service reload for
  newly-added node tags to take effect in ACL evaluation (issue #2389).

```bash
# Read the whole policy — groups, tagOwners, acls all in one document
curl -H "Authorization: Bearer $HS_API_KEY" \
  https://headscale.example.com/api/v1/policy
```

### Clients
- Go: `github.com/hibare/headscale-client-go` (Go 1.26+, Headscale v0.28+):
  `NewClient(baseURL, apiKey, opts)` → `.Policy().Read()`, `.Users().List()`,
  `.Nodes().List()`, `.ApiKeys()`, `.PreAuthKeys()`.
- Server ships generated Go gRPC/REST types under `gen/go`.
- Community: Python wrappers; JS/TS admin UIs (Headplane, headscale-admin).

## Tailscale SaaS API (fallback target, if not self-hosting Headscale)

**Auth:** API key via HTTP **Basic** (`-u tskey-api-xxx:`) OR OAuth2
client-credentials from `POST /api/v2/oauth/token` (token expires after **1 hour** —
cache and refresh). Base: `https://api.tailscale.com/api/v2`.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/tailnet/{tailnet}/acl` | Get ACL (HuJSON, or JSON with `Accept: application/json`). Contains groups + tagOwners. |
| GET | `/tailnet/{tailnet}/devices` | List devices, each with a `tags` array. |
| GET | `/tailnet/{tailnet}/users` | List tailnet users. |
| POST | `/oauth/token` | OAuth2 client-credentials → 1-hour bearer token. |

## Nginx Proxy Manager (NPM) API — the service registry

**No official API docs.** Best references: the instance's own (incomplete) schema at
`GET /api/schema`, the repo's `backend/routes/api`, community Postman collections,
and the web UI Audit Log (records the exact JSON the frontend sends).

**Auth:** JWT. `POST /api/tokens` with `{"identity": email, "secret": password}`
returns `{"token": "<jwt>", "expires": "<ISO>"}`. Send `Authorization: Bearer
<token>` afterward. JWT is RS256-signed.

- **Token lifetime is short** (~24–48h, quirky). `GET /api/tokens` with a valid
  token **refreshes** it — use that instead of re-sending credentials. There are
  **no long-lived API keys**; auth is purely admin-login-for-JWT.
- **2FA:** if TOTP is enabled, `POST /api/tokens` returns a `2fa-challenge`-scoped
  token and you must complete TOTP before getting a `user`-scoped token.

### Key endpoints

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/tokens` | Log in → JWT. |
| GET | `/api/tokens` | Refresh JWT with an existing valid token. |
| GET | `/api/nginx/proxy-hosts` | **List ALL proxy hosts** (JSON array, no pagination). |
| GET | `/api/nginx/proxy-hosts/{id}` | One proxy host. |
| GET | `/api/nginx/access-lists` | List access lists. |
| GET | `/api/nginx/access-lists/{id}` | One access list. |

**`?expand=` inlines foreign keys.** Relevant on proxy-hosts:
`?expand=owner,access_list,certificate`. On access-lists:
`?expand=owner,items,clients,proxy_hosts`. Without it you get raw IDs
(`access_list_id`, `certificate_id`, `owner_user_id`).

### Proxy host fields (what we render as service cards)

`id`, `domain_names[]`, `forward_scheme`, `forward_host`, `forward_port`,
`access_list_id` (0 = none), `certificate_id`, `enabled`, `meta` (incl.
`nginx_online`), `locations[]`, `advanced_config`, plus SSL/HSTS/websocket flags.

### Access lists

Fields: `name`, `satisfy_any`, `pass_auth`, `proxy_host_count`, `items[]` (HTTP
basic-auth users), `clients[]` (`{address: IP/CIDR, directive: allow|deny}`).
**Security:** item passwords are **never** returned — you get a masked `hint`
(`a****`). You can read usernames and IP rules, not plaintext passwords.

**Proxy-host ↔ access-list link:** a proxy host references an access list by
`access_list_id` (0 = none). Resolve names via `expand=access_list` or a separate
access-lists fetch joined on `id`.

```bash
# List all services (expand access-list + cert names inline)
TOKEN=$(curl -s -X POST https://npm.example.com/api/tokens \
  -H 'Content-Type: application/json' \
  -d '{"identity":"admin@example.com","secret":"..."}' | jq -r .token)
curl -H "Authorization: Bearer $TOKEN" \
  'https://npm.example.com/api/nginx/proxy-hosts?expand=access_list,certificate'
```

### Clients / wrappers
- `eighteen73/nginx-proxy-manager-api` (PHP), `Darker-Ink/Nginx-Proxy-Manger-API`,
  and an MCP server `VeryBigSad/nginx-proxy-manager-mcp`.

## Rate limits / caching (both APIs)

Neither product documents meaningful read quotas, and ACL/policy/proxy-host data
changes infrequently. **Poll on an interval (30–60s) and cache in memory** rather
than calling per user-request. For per-request identity (who is calling), prefer
Tailscale Serve identity headers / tailscaled whois (near-zero cost) over hitting a
management API. For Tailscale OAuth, refresh the token before its 1h expiry.
