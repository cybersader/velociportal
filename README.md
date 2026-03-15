# Velociportal

> **This is an idea placeholder / concept repo.** No code exists yet — this README captures the vision for a potential future project.

Identity-aware service dashboard that integrates Headscale/Tailscale ACLs with Nginx Proxy Manager to dynamically generate per-user application portals.

Users only see and access services they're authorized for based on their Tailscale identity and ACL group membership. Your network access policy **is** your dashboard policy — no separate visibility layer to maintain.

**Velociportal complements identity providers (IdPs) like Authentik, Keycloak, or Authelia — it doesn't replace them.** An IdP handles *authentication* (who are you?) and *authorization* (are you allowed?). Velociportal handles *visibility* (what should you see?). It reads your existing Tailscale ACL policy to decide which services to show each user on their dashboard. You'd still use an IdP for SSO, forward-auth, and access enforcement — Velociportal just makes sure the dashboard reflects what the network already permits.

## Overview

Velociportal is a self-hosted, identity-aware service dashboard that bridges **Headscale/Tailscale** and **Nginx Proxy Manager (NPM)**. It reads your Tailscale ACL policy and NPM proxy host configurations to automatically generate per-user application portals — each user only sees the services their ACL group grants access to.

No manual dashboard curation. No duplicate visibility rules. Your ACLs are the single source of truth for what appears on each user's portal. Pair it with an IdP like Authentik for authentication and SSO — Velociportal handles the dashboard layer on top.

## How It Works

```
┌─────────────────┐     ┌──────────────────┐
│  Headscale /    │     │  Nginx Proxy     │
│  Tailscale ACLs │     │  Manager API     │
└────────┬────────┘     └────────┬─────────┘
         │                       │
         │  ACL groups/users     │  Proxy hosts/services
         │                       │
         ▼                       ▼
    ┌────────────────────────────────┐
    │         Velociportal           │
    │                                │
    │  1. Fetch ACL policy (groups,  │
    │     users, access rules)       │
    │  2. Fetch NPM proxy hosts      │
    │     (services, domains)        │
    │  3. Match: which groups can    │
    │     reach which services       │
    │  4. Generate per-user portal   │
    │     showing only authorized    │
    │     services                   │
    └────────────────────────────────┘
                  │
                  ▼
    ┌────────────────────────────────┐
    │     User Dashboard Portal      │
    │                                │
    │  User A sees: App1, App3       │
    │  User B sees: App1, App2, App4 │
    │  User C sees: App2             │
    └────────────────────────────────┘
```

1. **Fetch ACL Policy** — Reads Tailscale/Headscale ACL definitions to understand which users and groups exist and what they can access
2. **Fetch NPM Proxy Hosts** — Queries NPM's REST API (`/api/nginx/proxy-hosts`) to discover all registered services and their domains
3. **Match Access** — Correlates ACL rules with proxy hosts to determine which users/groups can reach which services
4. **Generate Portal** — Renders a per-user dashboard showing only the services that user is authorized to access

## Planned Features

- **Auto-discovery** — Automatically pulls service list from NPM's API, no manual config
- **ACL-driven visibility** — Dashboard entries filtered by Tailscale ACL group membership
- **Per-user portals** — Each user sees only what they're allowed to access
- **Headscale API integration** — Native support for self-hosted Headscale control plane
- **Tailscale identity headers** — Identify users via Tailscale HTTPS identity
- **Real-time sync** — Watches for ACL and NPM changes, updates dashboards automatically
- **TrueNAS deployment** — Designed to run as a container on TrueNAS Scale

## Tech Stack

| Component | Role |
|-----------|------|
| **Headscale / Tailscale** | Network identity, ACL policy, user/group definitions |
| **Nginx Proxy Manager** | Reverse proxy, service registry (via API) |
| **TrueNAS Scale** | Container hosting platform |

## Alternatives & Comparison

Velociportal is **not a replacement** for any of the tools below — it's a **complement** that adds ACL-driven dashboard visibility on top of your existing stack. The key differentiator: your network ACL policy drives what appears on the dashboard, so you don't maintain a separate visibility layer. You'd still use an IdP for auth and an auth middleware for access enforcement.

| Solution | Type | Built-in Portal | Self-Hosted | ACL-Driven Dashboard | Notes |
|----------|------|:---:|:---:|:---:|-------|
| **Velociportal** | Dashboard + ACL bridge | Yes | Yes | Yes | Reads Tailscale ACLs + NPM directly — single source of truth |
| **Authentik** | Full IdP | Yes | Yes | No | Powerful IdP with app portal, OIDC/SAML, forward-auth for NPM. Closest full-featured alternative, but you manage permissions in Authentik separately from Tailscale ACLs |
| **Authelia** | Auth middleware | No | Yes | No | Lightweight forward-auth with per-subdomain rules. No portal — pair with Dashy/Homepage. Permissions managed in Authelia config, not your network ACLs |
| **Keycloak** | Enterprise IdP | Yes (account console) | Yes | No | Very powerful but heavy. OIDC/SAML, user federation. Overkill for most homelabs |
| **Zitadel** | Modern IdP | Yes | Yes | No | Lighter Keycloak alternative, API-first, growing homelab adoption. Still a separate permission system |
| **Organizr** | Dashboard | N/A | Yes | No | User/group visibility per tab, auth proxy for NPM. Simple but no ACL integration — you manage groups manually |
| **Dashy** | Dashboard | N/A | Yes | No | YAML-driven, role-based section/item visibility. Automatable config, but permissions are in the YAML, not your ACLs |
| **Homepage** | Dashboard | N/A | Yes | No | Popular, widget-rich, but limited per-user access control |
| **Cloudflare Access** | Zero Trust | Yes (app launcher) | No | No | Identity-aware per-app access with built-in launcher. Not self-hosted — requires Cloudflare |

### When Velociportal would add value

- You already use Tailscale/Headscale and NPM
- You want your network ACL groups to drive dashboard visibility automatically
- You're tired of manually curating dashboard links when your ACLs already define who can access what
- You want a dashboard layer that complements your existing IdP (Authentik, Authelia, etc.)
- You're running on TrueNAS Scale

### What Velociportal does NOT do (use an IdP for these)

- **Authentication / SSO** — Use Authentik, Keycloak, or Authelia to handle login and identity
- **Access enforcement** — Use forward-auth middleware to actually block unauthorized requests
- **OIDC/SAML federation** — Velociportal reads ACLs for visibility, not for auth protocol support

Velociportal sits alongside these tools, not instead of them.

## Roadmap

- [ ] Core service: read NPM proxy hosts via API
- [ ] Core service: read Headscale ACL policy via API
- [ ] ACL-to-service matching engine
- [ ] Per-user portal rendering
- [ ] Tailscale identity header authentication
- [ ] Real-time sync (watch for ACL/NPM changes)
- [ ] Web UI with service cards and search
- [ ] Docker Compose deployment config
- [ ] TrueNAS Scale app catalog entry
- [ ] Authentik/Authelia integration (optional, for SSO alongside ACL-based visibility)
- [ ] Custom service metadata (icons, descriptions, categories)
- [ ] Health checks for proxied services

## Project Status

**This is an idea placeholder.** No code has been written yet. This repo exists to capture the concept and gauge interest. If you find this idea useful or want to collaborate, open an issue or star the repo.

## License

TBD
