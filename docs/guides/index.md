# Choose an architecture

Start with the only implemented adapter pair, then choose where to run it. Velociportal is a **visibility layer**: it reads policy and service metadata, renders a filtered dashboard, and stays out of backend service traffic.

!!! note "Current support boundary"
    **Headscale + Nginx Proxy Manager (NPM)** is the implemented and fixture-tested adapter pair. Tailscale SaaS, Caddy, and Traefik pages are design notes, not deployable integrations.

## Architecture cards

<div class="grid cards" markdown>

-   :material-check-decagram-outline: **Headscale + NPM**

    <span class="vp-chip vp-chip--supported">Implemented</span>
    <span class="vp-chip vp-chip--validation">Real join validation pending</span>

    Reads Headscale policy and nodes, discovers services from NPM, and matches supported legacy ACL destinations against NPM `forward_host`.

    [Use the supported architecture →](headscale-npm.md)

-   :material-cloud-outline: **Tailscale SaaS + NPM**

    <span class="vp-chip vp-chip--planned">Planned</span>

    No SaaS API client, OAuth/API-key configuration, tailnet selector, or Grants implementation exists.

    [Read the design boundary →](tailscale-saas-npm.md)

-   :material-webhook: **Headscale + Caddy**

    <span class="vp-chip vp-chip--planned">Planned</span>

    No Caddy admin API client, Caddyfile parser, route model, or service-discovery adapter exists.

    [Read the design boundary →](headscale-caddy.md)

-   :material-router-network: **Headscale + Traefik**

    <span class="vp-chip vp-chip--planned">Planned</span>

    No Traefik router/service API client, Docker-label parser, or adapter configuration exists.

    [Read the design boundary →](headscale-traefik.md)

</div>

## Pick your next page

=== "I want to deploy now"

    1. Follow the [guided setup](../getting-started/setup.md).
    2. Use the [TrueNAS SCALE guide](truenas-scale.md) for the canonical NAS deployment.
    3. Read [Known Limitations](../reference/known-limitations.md).
    4. Validate at least two users, direct bypass rejection, every card URL, and the `forward_host` join.

=== "I want Headscale off the NAS"

    Read [VPS options for Headscale](vps-headscale.md), then return to the TrueNAS guide. A VPS can separate coordination from NAS reboots, but it adds cost, another security surface, and another backup domain.

=== "I need another adapter"

    Do not translate planned pages into configuration that the runtime does not support. The current binary reads Headscale and NPM only. New adapters require an explicit model, fixtures, safe failure behavior, and real API validation.

## Responsibility map

| Layer | Owns | Does not delegate to Velociportal |
|---|---|---|
| Headscale | Network policy and tailnet coordination | Actual authorization remains enforced here |
| Identity proxy / Tailscale Serve path | Human identity assertion and header sanitization | Source trust cannot be inferred by a header name alone |
| NPM | Public routing and service metadata | Access lists are not current visibility inputs |
| Velociportal | Supported policy-to-card visibility prediction | No login, traffic proxying, or request enforcement |
| Backend application | Application authorization and data access | A hidden card is not a backend security control |

!!! tip "Recommended route"
    Use **Headscale + NPM**, deploy through the guided path, and treat the first installation as a validation exercise rather than proof of full Tailscale policy parity.
