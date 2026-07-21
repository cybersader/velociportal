# Alternatives

Velociportal sits in a crowded space, but it occupies a specific niche: an **identity-aware visibility layer** that reads your existing Headscale/Tailscale ACLs and NPM proxy hosts to render per-user service portals.

!!! important "Velociportal complements IdPs, it does not replace them"
    Velociportal does **not** authenticate users, issue tokens, or enforce access. It reads identity headers that your tailnet already injects and shows each user the services they can reach. Keep Authentik, Authelia, or Tailscale doing auth. Velociportal is the dashboard on top.

## Comparison

| Solution | Type | Built-in Portal | Self-Hosted | ACL-Driven |
|---|---|---|---|---|
| **Velociportal** | Visibility layer | Yes | Yes | Yes (Headscale/Tailscale + NPM) |
| Authentik | IdP / SSO | Yes (app launcher) | Yes | No (own RBAC) |
| Authelia | IdP / auth portal | Partial | Yes | No (access rules) |
| Keycloak | IdP / SSO | No | Yes | No (own RBAC) |
| Zitadel | IdP / SSO | Yes (console) | Yes | No (own RBAC) |
| Organizr | Dashboard | Yes | Yes | No (manual tabs) |
| Dashy | Dashboard | Yes | Yes | No (YAML config) |
| Homepage | Dashboard | Yes | Yes | No (YAML config) |
| Homarr | Dashboard | Yes | Yes | No (manual/UI config) |
| Cloudflare Access | ZTNA / IdP | Yes (app launcher) | No (SaaS) | Yes (own policies) |
| Dashly | Dashboard | Yes | Yes | No (manual config) |

## Identity providers (IdPs)

These handle authentication and authorization. Velociportal reads the identity they establish; it never competes with them.

- **Authentik** — Full IdP with SSO, MFA, and an application launcher; use it to *authenticate* users, then let Velociportal show a portal driven by tailnet ACLs instead of Authentik's static app list.
- **Authelia** — Lightweight forward-auth portal for reverse proxies; great as the auth gate in front of your services, while Velociportal handles the per-user "what can I reach" view.
- **Keycloak** — Enterprise-grade IdP with deep OIDC/SAML support but no service dashboard; pair it for auth and use Velociportal for visibility.
- **Zitadel** — Modern IdP with a management console, but its console lists *organizations and projects*, not your homelab services; Velociportal fills that gap.

!!! note
    None of these read your Headscale ACLs or NPM proxy hosts. They define their own access model. Velociportal deliberately reuses the ACLs you already maintain, so there is one source of truth.

## Dashboards

These render service tiles but are **identity-blind**: everyone sees the same board, or you hand-maintain visibility per user.

- **Organizr** — Tab-based homelab dashboard with its own user system; powerful but you configure tabs and access manually rather than from ACLs.
- **Dashy** — Highly customizable YAML-driven dashboard; beautiful, but the service list is static and shared across all viewers.
- **Homepage** — Fast, YAML-configured dashboard with service widgets; no notion of who is looking at it.
- **Homarr** — Polished UI-driven dashboard with drag-and-drop; still a single shared board, not per-user filtered.
- **Dashly** — Minimal startpage-style dashboard; a launcher, not an access-aware portal.

!!! tip "The key difference"
    A dashboard shows a **fixed list**. Velociportal shows **your list** — computed from what your identity (via `Tailscale-User-Login`) is actually permitted to reach in the Headscale policy and which of those have an NPM proxy host.

## ZTNA / SaaS

- **Cloudflare Access** — Cloud-hosted zero-trust access with an app launcher and per-user policies; excellent if you want a managed edge, but it is not self-hosted and pulls your traffic through Cloudflare. Velociportal keeps everything inside your tailnet.

## When to use Velociportal

- You already run **Headscale or Tailscale** and maintain ACLs (`groups`, `tagOwners`, `acls`/`grants`) in huJSON.
- You expose services through **Nginx Proxy Manager** and want the portal to reflect real proxy hosts.
- You want each human user to see **only the services their identity can reach**, with zero duplicate config.
- You want a **single Go + templ + htmx container** with minimal dependencies, not another stateful platform.
- You already have an IdP for auth and just need the **visibility layer** on top.

## When to use something else

- You need **authentication, MFA, or token issuance** — use an IdP (Authentik, Authelia, Keycloak, Zitadel). Velociportal assumes auth is already solved.
- You want a **shared, pixel-perfect, widget-rich board** for a single admin view — use Homepage, Dashy, or Homarr.
- You are **not on a tailnet** and have no Headscale/Tailscale ACLs — Velociportal has nothing to read.
- Your services are for **tagged devices or exposed via Funnel** — Tailscale Serve does not inject user identity headers there, so per-user portals will not work.
- You want a **fully managed SaaS edge** — use Cloudflare Access.

!!! important "Recommended stack"
    IdP (auth) + Tailscale/Headscale (network + ACLs) + NPM (routing) + **Velociportal (per-user visibility)**. Each layer does one job. Velociportal is the piece that turns your existing ACLs into a portal, and it complements the rest rather than replacing any of them.