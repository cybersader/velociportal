# Authelia Integration

Velociportal **complements** Authelia, it does not replace it. Authelia enforces access; Velociportal shows each user the services they can reach. Use both.

## Division of Labor

| Concern | Owner |
|---|---|
| Forward-auth at the proxy (allow/deny) | Authelia |
| MFA (TOTP, WebAuthn, Duo) | Authelia |
| Per-subdomain access rules | Authelia |
| Session cookies + login portal | Authelia |
| Dashboard: which services a user *sees* | Velociportal |
| Service discovery from Tailscale ACLs / NPM hosts | Velociportal |

Authelia decides who gets through the door. Velociportal draws the map of doors visible to that user, sourced from Headscale/Tailscale ACLs and NPM proxy hosts.

!!! note "Authelia is lighter than Authentik"
    No outpost, no application objects. Authelia is a single forward-auth endpoint plus a YAML config. If you want SSO groups feeding your Velociportal ACL visibility, Authelia's `Remote-Groups` header pairs cleanly with Tailscale groups.

## Authelia Configuration

Define access rules per subdomain in `configuration.yml`. This is Authelia's job, not Velociportal's.

```yaml title="configuration.yml (access_control)"
access_control:
  default_policy: deny
  rules:
    # Velociportal dashboard: any authenticated user, one-factor
    - domain: portal.example.com
      policy: one_factor

    # Headscale admin: require MFA
    - domain: headscale.example.com
      policy: two_factor
      subject:
        - "group:admins"

    # NPM admin UI: MFA + admin group
    - domain: npm.example.com
      policy: two_factor
      subject:
        - "group:admins"

    # Everything else authenticated behind the tailnet
    - domain: "*.example.com"
      policy: one_factor
```

!!! tip "Groups flow both ways"
    Map your Authelia `groups` to Tailscale `group:` names in your Headscale ACL policy. Then a user's SSO group governs both their access (Authelia) and their dashboard visibility (Velociportal) with one source of truth.

## NPM Forward-Auth Setup

For each proxy host in Nginx Proxy Manager, add Authelia forward-auth in the **Advanced** tab.

=== "NPM Advanced (per host)"

    ```nginx
    location /authelia {
        internal;
        proxy_pass http://authelia:9091/api/verify;
        proxy_set_header X-Original-URL $scheme://$http_host$request_uri;
        proxy_set_header Content-Length "";
        proxy_pass_request_body off;
    }

    location / {
        auth_request /authelia;
        auth_request_set $user $upstream_http_remote_user;
        auth_request_set $groups $upstream_http_remote_groups;
        proxy_set_header Remote-User $user;
        proxy_set_header Remote-Groups $groups;

        # Redirect unauthenticated users to the Authelia portal
        error_page 401 =302 https://auth.example.com/?rd=$scheme://$http_host$request_uri;

        proxy_pass http://your-backend:8080;
    }
    ```

=== "Authelia portal host"

    Point `auth.example.com` at the Authelia container in NPM as a normal proxy host (scheme `http`, host `authelia`, port `9091`). Do **not** put forward-auth on the portal itself.

!!! warning "Velociportal reads NPM, it does not gate NPM"
    Velociportal pulls proxy-host definitions from NPM using credential-based JWT (`POST /api/tokens`) purely to build its service list. Access enforcement stays with Authelia's forward-auth. NPM has no scoped read-only token, so give Velociportal a dedicated low-value NPM account.

## Docker Compose

```yaml title="docker-compose.yml"
services:
  authelia:
    image: authelia/authelia:latest
    container_name: authelia
    volumes:
      - ./authelia:/config
    environment:
      - TZ=UTC
    restart: unless-stopped
    networks: [proxy]

  velociportal:
    image: velociportal:latest
    container_name: velociportal
    environment:
      # Headscale ACL / device source
      - HEADSCALE_URL=http://headscale:8080
      - HEADSCALE_API_KEY=${HEADSCALE_API_KEY}
      # NPM host discovery (credential JWT, no scoped token exists)
      - NPM_URL=http://npm:81
      - NPM_EMAIL=${NPM_EMAIL}
      - NPM_PASSWORD=${NPM_PASSWORD}
      # Trust identity headers injected by the proxy
      - TRUST_FORWARD_HEADERS=true
    restart: unless-stopped
    networks: [proxy]

networks:
  proxy:
    external: true
```

!!! note "Identity headers"
    Behind Authelia, Velociportal can read `Remote-User` / `Remote-Groups`. Over Tailscale Serve it reads `Tailscale-User-Login` instead. Serve headers only appear for human users on the tailnet, not tagged devices and not Funnel. Pick whichever your topology provides; both identify the user so Velociportal can filter the dashboard.

## Summary

1. Authelia enforces auth and MFA at NPM via forward-auth.
2. Velociportal reads Tailscale ACLs and NPM hosts to render a per-user dashboard.
3. Share group names between Authelia and your Headscale policy for one source of truth.

Velociportal is a visibility layer. Keep Authelia as your identity provider.