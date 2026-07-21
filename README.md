# Velociportal

> **Concept stage — no code yet.** This repo captures the design for a potential future project.

Identity-aware service dashboard that integrates Headscale/Tailscale ACLs with Nginx Proxy Manager to dynamically generate per-user application portals. Your network access policy **is** your dashboard policy.

**[Documentation](https://cybersader.github.io/velociportal/)**

## What it does

Velociportal reads your Tailscale ACL policy and NPM proxy host list, correlates them, and renders a per-user portal — each user sees only the services their ACL groups grant access to. No separate dashboard permissions to maintain.

**It complements IdPs (Authentik, Authelia, Keycloak) — it doesn't replace them.** The IdP handles authentication and access enforcement. Velociportal handles visibility: what shows up on the dashboard.

```
Headscale ACL ──┐
                 ├──▶ Velociportal ──▶ Per-user portal
NPM proxy hosts─┘     (matches ACLs     (alice sees App1, App3)
                        to services)     (bob sees App1, App2, App4)
```

## Reference Architectures

| Architecture | Control Plane | Reverse Proxy | Status |
|---|---|---|---|
| [Headscale + NPM](https://cybersader.github.io/velociportal/guides/headscale-npm/) | Self-hosted | Nginx Proxy Manager | Primary |
| [Tailscale SaaS + NPM](https://cybersader.github.io/velociportal/guides/tailscale-saas-npm/) | Managed | Nginx Proxy Manager | Planned |
| [Headscale + Caddy](https://cybersader.github.io/velociportal/guides/headscale-caddy/) | Self-hosted | Caddy | Future |
| [Headscale + Traefik](https://cybersader.github.io/velociportal/guides/headscale-traefik/) | Self-hosted | Traefik | Future |

## IdP Integrations

Works standalone with Tailscale identity headers, or pair with an IdP for SSO and MFA:

- [Authentik](https://cybersader.github.io/velociportal/integrations/authentik/) — Full IdP with forward-auth
- [Authelia](https://cybersader.github.io/velociportal/integrations/authelia/) — Lightweight auth middleware
- [No IdP](https://cybersader.github.io/velociportal/integrations/no-idp/) — Tailscale identity headers only

## Tech Stack

Single Docker container. Go + templ + htmx. Minimal dependencies. Designed for TrueNAS Scale.

## Roadmap

- [ ] Read NPM proxy hosts via API
- [ ] Read Headscale ACL policy via API
- [ ] ACL-to-service matching engine
- [ ] Per-user portal rendering (Tailscale identity headers)
- [ ] Web UI with service cards
- [ ] Docker Compose deployment
- [ ] Caddy / Traefik adapter support
- [ ] Custom service metadata (icons, descriptions)
- [ ] Health checks for proxied services

## License

TBD
