# Overview

Velociportal is an **identity-aware service dashboard** for self-hosted tailnets. It reads what your infrastructure already knows — Headscale ACLs, Nginx Proxy Manager (NPM) proxy hosts, and the identity headers Tailscale Serve injects — and renders a per-user portal showing only the services that user can actually reach.

!!! important "Velociportal complements your IdP — it does not replace it"
    Velociportal performs **no authentication**. It does not issue tokens, run OIDC, or block requests. Your IdP (Authentik, Authelia, Keycloak, Tailscale SSO, etc.) still owns login. Velociportal is a read-only **visibility layer** that answers one question: *"Given who you are, what can you see?"*

## What it is

A single Go binary in a single Docker container. It polls three data sources on an interval, correlates them, and serves a filtered dashboard over your tailnet. That's the whole job.

- **Not** a reverse proxy — requests never flow through it.
- **Not** an auth gateway — it can't grant or deny access.
- **Not** a config tool — it reads state, it doesn't write it.

## The three data sources

=== "Headscale ACL"

    The REST API at `/api/v1` (Bearer API key) exposes the ACL policy in Tailscale's huJSON format — `groups`, `tagOwners`, `acls`, and `grants`. Velociportal parses this to learn **who is allowed to talk to what**.

    ```bash
    curl -H "Authorization: Bearer $HS_API_KEY" \
      https://headscale.example.com/api/v1/policy
    ```

=== "NPM proxy hosts"

    NPM exposes the list of proxied services (domains, upstreams, TLS state). This is the **service catalog** — the set of things a portal could link to.

    !!! warning "NPM has no read-only API token"
        NPM only supports credential-based JWT: `POST /api/tokens` with an email and password returns a short-lived token. There is no scoped read-only key. Give Velociportal a **dedicated NPM user** with the minimum role, and treat those credentials as secrets.

    ```bash
    curl -X POST https://npm.example.com/api/tokens \
      -H "Content-Type: application/json" \
      -d '{"identity":"velociportal@example.com","secret":"..."}'
    ```

=== "Tailscale identity headers"

    When a request comes in over **Tailscale Serve**, Tailscale injects headers identifying the caller:

    ```
    Tailscale-User-Login:       alice@example.com
    Tailscale-User-Name:        Alice
    Tailscale-User-Profile-Pic: https://...
    ```

    Velociportal uses these to know **who is looking**, then filters the catalog against the ACL.

    !!! note "Serve only, humans only"
        These headers exist **only** for human users over tailnet Serve. They are **not** present for tagged devices, and **not** present over Funnel (public internet). Portals therefore work for logged-in humans on the tailnet — by design.

## Authentication vs Authorization vs Visibility

These are three separate concerns. Velociportal owns only the third.

| Concern | Question | Owned by | Velociportal's role |
|---|---|---|---|
| **Authentication** | Who are you? | IdP (OIDC/SSO), Tailscale login | Consumes the resulting identity header — never authenticates |
| **Authorization** | Are you allowed through? | Headscale ACL, NPM, forward-auth | Reads the ACL to predict access — never enforces it |
| **Visibility** | What can you see? | **Velociportal** | Renders a per-user dashboard of reachable services |

!!! danger "Visibility is not enforcement"
    A service hidden from a user's portal is **not** protected. If the ACL or proxy still allows the request, the user can reach it by typing the URL directly. Velociportal shapes the *view*, not the *boundary*. Keep enforcing access in Headscale and your proxy.

## Architecture

```text
                    ┌──────────────────────────────┐
                    │        Velociportal           │
   Headscale API ──▶│  (Go + templ + htmx)          │
   /api/v1 (Bearer) │                                │
                    │  1. poll ACL   (who ↔ what)    │
   NPM API ────────▶│  2. poll hosts (service list)  │
   /api/tokens JWT  │  3. correlate + cache          │
                    │                                │
                    │         renders ▼              │
                    └──────────────┬────────────────┘
                                   │  HTML (filtered per user)
                                   │
   Human user ──▶ Tailscale Serve ─┘
   over tailnet   (injects Tailscale-User-* headers)
                        │
                        └─▶ identity used to filter the view
```

Data flows **into** Velociportal from Headscale and NPM, and identity flows in via Serve headers. Nothing flows *through* it to your services.

## What it does NOT do

- **No SSO / OIDC / login screen** — authentication stays with your IdP and Tailscale.
- **No request proxying or blocking** — it is not in the data path; it cannot allow or deny traffic.
- **No ACL or NPM writes** — read-only; it never mutates your policy or proxy config.
- **No Funnel / public exposure** — it relies on tailnet Serve identity headers, so it is a tailnet-internal tool.
- **No coverage for tagged devices** — no identity header means no personalized portal.

!!! tip "The one-line summary"
    Your IdP decides *who you are*. Your ACL and proxy decide *what you can reach*. Velociportal just shows you *what's there* — a friendly, filtered front door built from state you already maintain.