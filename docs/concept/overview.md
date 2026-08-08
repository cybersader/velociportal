# Overview

Velociportal is an **identity-aware visibility layer** for a self-hosted Headscale tailnet. It reads a supported subset of the Headscale policy, discovers services from Nginx Proxy Manager (NPM), and renders a filtered portal for a human identity supplied by a trusted proxy.

## What it is

- One static Go binary in one minimal Docker container.
- A read-only Headscale and NPM API consumer.
- An in-memory snapshot refreshed on a ticker.
- A server-rendered portal enhanced with an embedded copy of htmx.

## What it is not

- **Not an auth gateway.** It performs no login, OIDC, SAML, MFA, or session management.
- **Not a reverse proxy.** Backend service requests do not pass through Velociportal.
- **Not an ACL editor.** It does not write Headscale policy or NPM configuration.
- **Not a complete Tailscale policy engine.** It currently evaluates legacy `acls` only.

## Inputs

=== "Headscale"

    Velociportal fetches:

    - `GET /api/v1/policy` — `groups`, `tagOwners`, `acls`, and `hosts`
    - `GET /api/v1/node` — node IPs, owners, and tags used for destination resolution

    Grants, SSH rules, posture, and application capabilities are not evaluated.

=== "NPM"

    Velociportal authenticates through `POST /api/tokens`, then reads `GET /api/nginx/proxy-hosts` as the service catalog. NPM access lists are not used in current visibility decisions.

=== "Request identity"

    The runtime accepts only:

    ```text
    Tailscale-User-Login: alice@example.com
    Tailscale-User-Name: Alice
    Tailscale-User-Profile-Pic: https://...
    ```

    The login header is trusted only when the request source is inside `TRUSTED_PROXY_CIDR`.

## Authentication, authorization, visibility

| Concern | Owner | Velociportal's role |
|---|---|---|
| Authentication | Tailscale/Headscale login and optional IdP | Consumes an asserted identity; does not authenticate |
| Authorization | Headscale ACLs, reverse proxy, backend app | Reads policy to predict visibility; does not enforce |
| Visibility | Velociportal | Renders a filtered service index |

!!! danger "A hidden card is not a security boundary"
    A user can still type a service URL directly. The ACL, proxy, and application must reject unauthorized traffic independently.

See [Known Limitations](../reference/known-limitations.md) for the exact matcher and integration boundary.
