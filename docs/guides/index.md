# Choose an architecture

Select one implemented control-plane adapter plus NPM. Velociportal is a **visibility layer**: it reads policy and service metadata, renders a filtered dashboard, and stays out of backend service traffic.

!!! note "Current support boundary"
    **Headscale + NPM** is the supported implementation path, with real TrueNAS acceptance still pending. **Tailscale SaaS + NPM** is an implemented, fixture-tested preview that uses OAuth and the same legacy ACL visibility subset; it remains preview until live SaaS acceptance passes. One Velociportal process selects one provider.

## Architecture cards

<div class="grid cards" markdown>

-   :material-check-decagram-outline: **Headscale + NPM**

    <span class="vp-chip vp-chip--supported">Implemented architecture</span>
    <span class="vp-chip vp-chip--validation">Real acceptance pending</span>

    Reads Headscale policy and nodes, discovers services from NPM, and matches supported legacy ACL destinations against NPM `forward_host`. Existing NPM also provides the trusted HTTPS Headscale endpoint needed before clients join the tailnet; runtime Velociportal bypasses that proxy over `velociportal-upstreams`.

    [Use the approved architecture →](headscale-npm.md)

-   :material-cloud-outline: **Tailscale SaaS + NPM**

    <span class="vp-chip vp-chip--validation">Implemented preview</span>
    <span class="vp-chip vp-chip--security">Live acceptance pending</span>

    Uses a fixed verified Tailscale API origin, dedicated OAuth client credentials, exact user/device owner mapping, the same `legacy_acl_visibility_v1` boundary, and NPM discovery over `velociportal-upstreams`. Grants and posture remain unsupported and fail closed.

    [Use the preview architecture →](tailscale-saas-npm.md)

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
| Selected control plane | Headscale or Tailscale SaaS policy and tailnet coordination | Actual authorization remains enforced here |
| Existing NPM control proxy | Trusted pre-tailnet Headscale HTTPS, certificate lifecycle, upgrade forwarding in Headscale mode | It is not portal identity; SaaS mode does not need this control proxy |
| Tailscale HTTP Serve | Human identity assertion and header sanitization | Source trust cannot be inferred from a header name alone |
| NPM service catalog | Proxy-host metadata and application routing | Access lists are not current visibility inputs |
| Velociportal | Supported policy-to-card visibility prediction | No login, traffic proxying, or request enforcement |
| Backend application | Application authorization and data access | A hidden card is not a backend security control |

!!! tip "Recommended route"
    Use **Headscale + NPM** for the supported implementation path. Use **Tailscale SaaS + NPM** only as a labeled preview and retain that label until its separate live worksheet passes. Both modes use the same hardened one-service production bundle.
