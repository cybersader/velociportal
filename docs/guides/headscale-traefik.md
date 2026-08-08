# Headscale + Traefik

!!! danger "Planned — not implemented"
    Velociportal currently discovers services only from Nginx Proxy Manager. It has no Traefik routers/services API client, Docker-label parser, configuration variables, or adapter tests.

A future Traefik adapter would need to:

1. Read enabled routers and services from a protected Traefik API.
2. Parse `Host(...)` rules, including multiple hosts and compound expressions.
3. Map routers to upstream destinations without treating routing labels as authorization.
4. Validate the resulting service-to-Headscale join against real data.

Traefik may front the current application as an external reverse proxy, but the current runtime still requires trusted `Tailscale-User-*` headers and still uses NPM as its service catalog.

Use [Headscale + NPM](headscale-npm.md) for the supported architecture and read [Known Limitations](../reference/known-limitations.md).
