# Tailscale Identity Headers

Velociportal reads identity from **Tailscale Serve** headers to know *who* is behind a request. This page covers the headers, how they arrive, and why spoofing is the central thing to defend against.

!!! note "Velociportal complements your IdP — it does not replace it"
    Tailscale (or Headscale) still handles the actual authentication. Users log in through your existing IdP — Google, GitHub, Okta, an OIDC provider, whatever Headscale is wired to. Velociportal only *consumes* the identity Tailscale has already established. It is an authorization/routing layer, not an identity provider.

## The headers

When `tailscale serve` proxies an HTTPS request from a human user on your tailnet, it injects these headers:

| Header | Example value | Description |
| --- | --- | --- |
| `Tailscale-User-Login` | `alice@example.com` | Canonical login / identity. Use this as the stable key. |
| `Tailscale-User-Name` | `Alice Smith` | Display name. Cosmetic — do not authorize on it. |
| `Tailscale-User-Profile-Pic` | `https://.../avatar.png` | Avatar URL. Optional, cosmetic. |

!!! warning "These headers appear only for human users"
    - **Tagged devices** (nodes with `tag:` ACL tags, e.g. CI runners, servers) have **no user identity** and Serve sends **no** `Tailscale-User-*` headers. Do not assume every request carries identity.
    - **Funnel traffic** (public internet via `tailscale funnel`) is **not** on your tailnet, so these headers are **never** set. Treat Funnel requests as anonymous.

## How the headers arrive

Tailscale Serve terminates TLS on the node and adds the identity headers based on the tailnet identity of the source. Critically:

!!! danger "Serve strips inbound spoofed headers"
    `tailscale serve` **removes any client-supplied `Tailscale-User-*` headers** and re-sets them itself. A remote client cannot forge identity *through* Serve. The trust boundary is the Serve process — everything downstream must ensure requests can *only* reach it via Serve.

## The nginx-auth pattern

The common wiring puts an auth service behind `auth_request`. Serve proxies to nginx over a **unix socket**; nginx calls an internal auth endpoint that validates the `Tailscale-User-*` headers and **re-emits them as `X-Webauth-*`** for the upstream app. Re-emitting under a distinct prefix means the app trusts one namespace it controls, not the raw Tailscale headers.

```nginx title="nginx.conf"
server {
    # Serve connects here over a unix socket, not a public TCP port.
    listen unix:/run/velociportal/serve.sock;

    location = /auth {
        internal;
        # Auth service reads Tailscale-User-* and returns X-Webauth-* on 200.
        proxy_pass http://unix:/run/velociportal/auth.sock:/verify;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Original-URI $request_uri;
    }

    location / {
        auth_request /auth;

        # Capture re-emitted identity from the auth subrequest...
        auth_request_set $webauth_user  $upstream_http_x_webauth_user;
        auth_request_set $webauth_name  $upstream_http_x_webauth_name;

        # ...and pass ONLY the trusted X-Webauth-* prefix upstream.
        proxy_set_header X-Webauth-User $webauth_user;
        proxy_set_header X-Webauth-Name $webauth_name;

        proxy_pass http://npm.example.com:81;
    }
}
```

!!! tip "Always clear the incoming X-Webauth-* namespace"
    Explicitly blank any client-supplied `X-Webauth-*` headers at the edge so nothing bypasses the auth subrequest. The only place these get set is the `auth_request_set` block above.

## How Velociportal reads them

Velociportal trusts identity headers **only** when the request originates from a known proxy. Configure the CIDR(s) of your Serve/nginx layer:

=== "docker-compose"

    ```yaml
    services:
      velociportal:
        image: velociportal:latest
        environment:
          # Only accept X-Webauth-* / Tailscale-User-* from these sources.
          TRUSTED_PROXY_CIDR: "127.0.0.1/32,10.0.0.0/8"
        # Prefer binding to the unix socket / internal network only.
    ```

=== "env"

    ```bash
    TRUSTED_PROXY_CIDR="127.0.0.1/32,10.0.0.0/8"
    ```

If a request comes from outside `TRUSTED_PROXY_CIDR`, Velociportal **ignores** any identity headers on it and treats the request as unauthenticated. Set this to the narrowest range that covers your proxy — ideally loopback or a unix socket only.

## Security: header spoofing is the threat model

The entire scheme rests on one invariant: **the app must never be reachable except through the proxy that sets identity.** If an attacker can hit Velociportal (or the upstream app) directly, they can send `Tailscale-User-Login: admin@example.com` and impersonate anyone.

!!! danger "Checklist"
    - Bind the app and Velociportal to a **unix socket or internal-only interface** — never publish the app's port to the host or LAN.
    - Set `TRUSTED_PROXY_CIDR` to the smallest possible range.
    - Let **Serve** strip inbound `Tailscale-User-*`; let **nginx** own the `X-Webauth-*` namespace.
    - Remember: no headers means no identity. Tagged devices and Funnel traffic must be handled as anonymous, not trusted.

!!! note "Reminder"
    None of this authenticates users — Tailscale/Headscale and your IdP already did. Velociportal just decides what an already-authenticated identity is allowed to reach.