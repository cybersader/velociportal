# 03 — Prior Art

> Similar tools and what we take from each. Velociportal is a **complement** to these,
> not a replacement — the differentiator is that **the Tailscale ACL drives dashboard
> visibility**, so there's no separate visibility layer to maintain.

## Identity providers (auth — the layer we sit on top of)

| Tool | Type | Portal | ACL-driven | Takeaway |
|------|------|:---:|:---:|----------|
| **Authentik** | Full IdP | Yes | No | Closest full-featured alternative: app portal + OIDC/SAML + forward-auth for NPM. But permissions live in Authentik, managed **separately** from Tailscale ACLs. This is exactly the duplication we remove — and the IdP we most expect users to pair with. |
| **Authelia** | Auth middleware | No | No | Lightweight forward-auth, per-subdomain rules, no portal (pair with Dashy/Homepage). Permissions in Authelia config, not the network ACL. Good enforcement partner. |
| **Keycloak** | Enterprise IdP | Account console | No | Very powerful, heavy; overkill for homelabs. Separate permission system. |
| **Zitadel** | Modern IdP | Yes | No | Lighter, API-first Keycloak alternative; still a separate permission system. |

**Lesson:** every IdP maintains its own permission model. We deliberately don't —
we read the ACL you already wrote. We integrate with these for auth, we don't
compete on it.

## Dashboards (visibility — the layer we replace/automate)

| Tool | Portal model | Takeaway |
|------|--------------|----------|
| **Organizr** | User/group visibility per tab, auth proxy for NPM | Proves per-user tab visibility is wanted — but groups are curated by hand. |
| **Dashy** | YAML-driven, role-based section/item visibility | Nicely automatable config, but visibility rules live in YAML, **not** your ACL. |
| **Homepage** | Widget-rich, popular | Great UX bar to match; limited per-user access control. |
| **Cloudflare Access** | Zero-Trust app launcher | Identity-aware per-app launcher — the UX we emulate, but it's not self-hosted (requires Cloudflare). |

**Lesson:** all of these make you author a visibility config that duplicates access
policy. Velociportal's wedge is deriving that config from the ACL automatically.

## Ecosystem clients we can reuse (avoid reinventing)

- **Headscale (Go):** `github.com/hibare/headscale-client-go` — `.Policy().Read()`,
  `.Users().List()`, `.Nodes().List()`. Server ships generated types in `gen/go`.
  Admin UIs Headplane / headscale-admin consume the same REST API.
- **Tailscale (Go):** official `github.com/tailscale/tailscale-client-go/v2`
  (API-key + OAuth). `dimer47/tailscale-cli` covers ~85 v2 endpoints + an MCP server.
- **NPM:** `eighteen73/nginx-proxy-manager-api` (PHP),
  `Darker-Ink/Nginx-Proxy-Manger-API`, and MCP server
  `VeryBigSad/nginx-proxy-manager-mcp` (tools: `list_proxy_hosts`, `get_host_report`,
  …). No official docs — lean on the instance's `/api/schema` and the web UI Audit
  Log for real payloads.

**Lesson:** the read side of both APIs is already wrapped by community clients — we
can borrow shapes/patterns even if we write our own thin Go clients (per D5, keeping
deps minimal).
