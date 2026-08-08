# Headscale + Caddy

!!! danger "Planned — not implemented"
    Velociportal currently discovers services only from Nginx Proxy Manager. It has no Caddy admin-API client, Caddyfile parser, route model, configuration variables, or adapter tests.

A future Caddy adapter would need to:

1. Read Caddy's active HTTP routes from a protected local admin API.
2. Extract public hostnames and upstream destinations consistently.
3. Convert them into the same internal service model used by the matcher.
4. Validate the join against real Headscale destinations.

Caddy can still be used as an **external proxy in front of the current app**, but it must not be described as Velociportal's service-discovery source. The runtime also accepts only `Tailscale-User-*` identity headers; Caddy alone does not know the human tailnet identity unless an identity-aware component supplies those headers safely.

Use [Headscale + NPM](headscale-npm.md) for the supported architecture and read [Known Limitations](../reference/known-limitations.md).
