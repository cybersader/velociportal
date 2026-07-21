```markdown
# Reference Architectures

Velociportal is a thin authorization layer that sits behind your reverse proxy and alongside your tailnet control plane. It does not care much which specific proxy or control plane you run — it works with several common combinations.

!!! note "Velociportal complements your IdP"
    Velociportal does **not** replace your identity provider. Authentication still happens at your IdP (Authelia, Authentik, Pocket ID, etc.). Velociportal reads the identity your IdP asserts and decides what tailnet-fronted services that user may reach.

## Supported stacks

| Architecture | Notes |
|---|---|
| [Headscale + NPM](headscale-npm.md) | Primary, most tested. Self-hosted control plane + Nginx Proxy Manager. |
| [Tailscale SaaS + NPM](tailscale-npm.md) | Managed control plane from Tailscale, same NPM front door. |
| [Headscale + Caddy](headscale-caddy.md) | Alternative reverse proxy with automatic TLS. |
| [Headscale + Traefik](headscale-traefik.md) | Alternative reverse proxy with label-driven routing. |

Each guide includes the full `docker-compose.yml` and shows exactly how Velociportal connects to that stack (e.g. `headscale.example.com`, `npm.example.com`).

!!! tip "Start here"
    New to Velociportal? Use **Headscale + NPM** — it is the reference implementation the other guides build on.
```