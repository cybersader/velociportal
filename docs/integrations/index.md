# Integrate identity safely

Velociportal complements identity providers, but the current runtime has one fixed identity contract: a trusted source supplies `Tailscale-User-Login`, with optional name and profile-picture headers.

<div class="vp-chip-row" aria-label="Identity integration status">
<span class="vp-chip vp-chip--supported">Supported: Tailscale-User-* contract</span>
<span class="vp-chip vp-chip--security">Required: source CIDR validation</span>
<span class="vp-chip vp-chip--planned">Direct IdP adapters: planned</span>
</div>

## Choose your identity arrangement

<div class="grid cards" markdown>

-   :material-tailwind: **Tailscale identity only**

    <span class="vp-chip vp-chip--supported">Current runtime path</span>

    The canonical Headscale path is the existing Tailscale app with declarative tailnet HTTP Serve. It strips client headers and injects the supported `Tailscale-User-*` values.

    [Use the current identity contract →](no-idp.md)

-   :material-shield-account-outline: **Authentik alongside Velociportal**

    <span class="vp-chip vp-chip--planned">No direct header adapter</span>

    Authentik can provide SSO, MFA, and forward-auth for linked services. Velociportal still needs its supported identity path and does not read `X-authentik-*` headers.

    [Understand the supported arrangement →](authentik.md)

-   :material-lock-check-outline: **Authelia alongside Velociportal**

    <span class="vp-chip vp-chip--planned">No direct header adapter</span>

    Authelia can enforce login and per-domain policy. Velociportal does not read `Remote-User`, `Remote-Groups`, or a trust-forward-headers switch.

    [Understand the supported arrangement →](authelia.md)

</div>

## What can be combined today

=== "Use an IdP for applications"

    This is supported as a layered architecture:

    1. The IdP handles login, MFA, sessions, and application access policy.
    2. Headscale enforces network access.
    3. NPM routes service traffic.
    4. Velociportal renders the filtered index from the supported Headscale policy subset.

    Similar group names across systems do not create synchronization. Each system remains responsible for its own policy.

=== "Protect the portal with an IdP"

    An IdP may protect the portal URL at an outer layer, but the final request into Velociportal must still satisfy the current contract:

    - Source address is inside `TRUSTED_PROXY_CIDR`.
    - `Tailscale-User-Login` is present.
    - Client-supplied identity headers were removed before trusted values were injected.

    A translator from another identity system is outside the current project and must be reviewed as part of the trust boundary.

=== "Send IdP headers directly"

    This is **not implemented**. The runtime does not currently read:

    - Authentik `X-authentik-*`
    - Authelia `Remote-User` or `Remote-Groups`
    - `X-Webauth-*`
    - Configurable identity or group header names

    Do not use older examples containing `VP_IDENTITY_HEADER`, `VP_GROUPS_HEADER`, or `TRUST_FORWARD_HEADERS`; those variables do not exist.

## Fixed header contract

| Header | Required | Current use |
|---|---:|---|
| `Tailscale-User-Login` | Yes | Identity used for supported policy and group matching |
| `Tailscale-User-Name` | No | Display name |
| `Tailscale-User-Profile-Pic` | No | Accepted but not currently rendered |

The correct next step is [Tailscale Identity Headers](../reference/tailscale-headers.md), which diagrams the trusted route and rejected bypass route.
