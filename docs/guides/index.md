# Choose an architecture

Start with the only implemented adapter pair. Velociportal is a **visibility layer**: it reads policy and service metadata, renders a filtered dashboard, and stays out of backend service traffic.

!!! note "Current support boundary"
    **Headscale + Nginx Proxy Manager (NPM)** is implemented and fixture-tested. The approved TrueNAS architecture uses Tailscale HTTP Serve for browser identity, existing NPM HTTPS for the pre-tailnet Headscale control path, and a named private Docker bridge with required egress for runtime upstream calls. Real acceptance is still pending.

## Architecture cards

<div class="grid cards" markdown>

-   :material-check-decagram-outline: **Headscale + NPM**

    <span class="vp-chip vp-chip--supported">Implemented architecture</span>
    <span class="vp-chip vp-chip--validation">Real acceptance pending</span>

    Reads Headscale policy and nodes, discovers services from NPM, and matches supported legacy ACL destinations against NPM `forward_host`. Existing NPM also provides the trusted HTTPS Headscale endpoint needed before clients join the tailnet; runtime Velociportal bypasses that proxy over `velociportal-upstreams`.

    [Use the approved architecture →](headscale-npm.md)

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

    Follow the [TrueNAS Quickstart](truenas-scale.md). It is the single linear, UI-managed journey. It does not require a source build or recurring NAS shell.

=== "I need native Headscale HTTPS"

    Read [Optional native Headscale TLS](private-tls.md). It keeps verified HTTPS and the private-CA overlay available without making private PKI canonical or adding a PKI service.

=== "I want Headscale off the NAS"

    Read [VPS options for Headscale](vps-headscale.md), then return to the security and validation boundaries. Remote Headscale locations require verified HTTPS.

=== "I need another adapter"

    Do not translate planned pages into configuration that the runtime does not support. New adapters require an explicit data model, fixtures, safe failure behavior, and real API validation.

## Responsibility map

| Layer | Owns | Does not delegate to Velociportal |
|---|---|---|
| Headscale | Network policy and tailnet coordination | Actual authorization remains enforced here |
| Existing NPM control proxy | Trusted pre-tailnet Headscale HTTPS, certificate lifecycle, upgrade forwarding | It is not portal identity and runtime Velociportal bypasses it |
| Tailscale HTTP Serve | Human identity assertion and header sanitization | Source trust cannot be inferred from a header name alone |
| NPM service catalog | Proxy-host metadata and application routing | Access lists are not current visibility inputs |
| Velociportal | Supported policy-to-card visibility prediction | No login, traffic proxying, or request enforcement |
| Backend application | Application authorization and data access | A hidden card is not a backend security control |

!!! tip "Recommended route"
    Use **Headscale + NPM**, follow the **TrueNAS Quickstart**, and treat the first installation as a release-candidate acceptance exercise. No public support claim is warranted until the full worksheet passes.
