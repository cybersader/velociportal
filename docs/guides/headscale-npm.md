# Headscale + NPM Reference Architecture

The primary Velociportal deployment. Headscale runs your tailnet, Nginx Proxy Manager (NPM) reverse-proxies your services, and Velociportal reads both APIs to render a per-user dashboard of the services each person can actually reach.

!!! note "Velociportal complements your IdP, it does not replace it"
    Velociportal is a **visibility layer**. It shows users what exists and who can reach it. Authentication and authorization still belong to Headscale ACLs, NPM, and your identity provider. Nothing here is an auth boundary.

## Architecture

```text
                        +---------------------+
                        |     Headscale       |
                        |  (coordination)     |
                        |  /api/v1  (Bearer)  |
                        +----------+----------+
                                   ^
              ACL policy + nodes   |  reads
                                   |
   Tailnet nodes                   |
   +---------+   +---------+   +----+----------------+
   | laptop  |   | phone   |   |   Velociportal      |
   | (user)  |   | (user)  |   |  Go + templ + htmx  |
   +----+----+   +----+----+   |  reads both APIs    |
        |             |        +----+----------------+
        |  Tailscale  |             ^  reads proxy hosts
        |  Serve      |             |  (JWT via login)
        v             v             |
   +----------------------+    +----+----------------+
   |  NPM (reverse proxy) +--->|      NPM API        |
   |  injects headers     |    |  /api/tokens (JWT)  |
   +----------+-----------+    +---------------------+
              |
              v
   backend services (Grafana, etc.)
```

Two data sources, one view:

- **Headscale** tells Velociportal *who* the users and nodes are, and what the ACL policy grants.
- **NPM** tells Velociportal *what* services exist (proxy hosts) and where they route.

Velociportal joins them and renders only the tiles a given user should see.

## Identity flow

The request path injects the caller's identity as trusted headers:

```text
user browser
  -> Tailscale Serve (on the NPM/Velociportal host)
       injects: Tailscale-User-Login
                Tailscale-User-Name
                Tailscale-User-Profile-Pic
  -> NPM (passes headers through to the upstream)
  -> Velociportal (trusts Tailscale-User-Login from a known proxy IP)
```

Velociportal reads `Tailscale-User-Login`, matches it against Headscale users/groups, and filters the service list to what that user can reach.

!!! warning "Header injection only works for humans over tailnet Serve"
    Tailscale Serve injects `Tailscale-User-*` headers **only** for authenticated human users on the tailnet. It does **not** work for:

    - Tagged devices (`tag:server`) — no user identity
    - Traffic over **Funnel** (public internet) — headers are stripped

    Do not expose Velociportal via Funnel. Treat any request lacking a valid header from a trusted proxy as anonymous.

## Docker Compose

All three services on one host. Placeholder values throughout — replace before deploying.

```yaml
services:
  headscale:
    image: headscale/headscale:latest
    container_name: headscale
    command: serve
    restart: unless-stopped
    ports:
      - "8080:8080"          # headscale.example.com
    volumes:
      - ./headscale/config:/etc/headscale
      - ./headscale/data:/var/lib/headscale

  npm:
    image: jc21/nginx-proxy-manager:latest
    container_name: npm
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "81:81"              # npm.example.com admin UI
    volumes:
      - ./npm/data:/data
      - ./npm/letsencrypt:/etc/letsencrypt

  velociportal:
    image: velociportal:latest
    container_name: velociportal
    restart: unless-stopped
    # Bind to loopback only; expose via Tailscale Serve, not a public port.
    ports:
      - "127.0.0.1:3000:3000"
    environment:
      HEADSCALE_URL: "http://headscale:8080"
      HEADSCALE_API_KEY: "${HEADSCALE_API_KEY}"
      NPM_URL: "http://npm:81"
      NPM_EMAIL: "${NPM_EMAIL}"
      NPM_PASSWORD: "${NPM_PASSWORD}"
      TRUSTED_PROXY_CIDR: "172.16.0.0/12"   # docker network / NPM source range
    depends_on:
      - headscale
      - npm
```

## Velociportal configuration

Configuration is entirely environment variables.

=== "Variables"

    | Variable | Example | Purpose |
    |---|---|---|
    | `HEADSCALE_URL` | `http://headscale:8080` | Headscale API base |
    | `HEADSCALE_API_KEY` | `hskey-...` | Bearer key for `/api/v1` |
    | `NPM_URL` | `http://npm:81` | NPM admin API base |
    | `NPM_EMAIL` | `admin@example.com` | NPM login (JWT) |
    | `NPM_PASSWORD` | `changeme` | NPM login (JWT) |
    | `TRUSTED_PROXY_CIDR` | `172.16.0.0/12` | Only accept identity headers from here |

=== ".env file"

    ```bash
    HEADSCALE_URL=http://headscale:8080
    HEADSCALE_API_KEY=hskey-abcdef0123456789
    NPM_URL=http://npm:81
    NPM_EMAIL=admin@example.com
    NPM_PASSWORD=super-secret-admin-password
    TRUSTED_PROXY_CIDR=172.16.0.0/12
    ```

### Generating the Headscale API key

```bash
docker exec headscale headscale apikeys create --expiration 90d
```

Copy the printed key into `HEADSCALE_API_KEY`. Rotate on the expiration you set.

### NPM authentication

!!! danger "NPM has no read-only API token"
    NPM authenticates via `POST /api/tokens` with an email and password, returning a short-lived JWT. There is **no scoped, read-only token**. The credentials you give Velociportal are **admin-equivalent** — they can change proxy hosts, certs, and access lists.

Velociportal exchanges the credentials for a JWT and refreshes it as needed:

```bash
curl -X POST http://npm:81/api/tokens \
  -H "Content-Type: application/json" \
  -d '{"identity":"admin@example.com","secret":"changeme"}'
# -> { "token": "eyJhbGci...", "expires": "..." }
```

## Security notes

!!! warning "Bind Velociportal to the tailnet only"
    Velociportal shows the full service map. Never expose it publicly.

    - Bind the container to `127.0.0.1` (as above) and publish it with `tailscale serve`, or bind it directly to the tailnet interface.
    - Do **not** create a public NPM proxy host or Funnel for it.

=== "Expose via Tailscale Serve"

    ```bash
    tailscale serve --bg --https=443 127.0.0.1:3000
    ```

    This is also what injects the `Tailscale-User-*` headers.

=== "Trusted proxy check"

    Velociportal only trusts `Tailscale-User-Login` when the request source IP falls inside `TRUSTED_PROXY_CIDR`. Requests from anywhere else are treated as anonymous. Set this CIDR to the smallest range that covers NPM / your Serve proxy.

Additional hardening:

- **Treat NPM credentials as admin secrets.** Store them in a secrets manager or restricted `.env` (`chmod 600`), never in the image or git. Rotate if leaked.
- **Scope the Headscale key.** Use short expirations and rotate. The key can read your full ACL policy and node list.
- **Velociportal is read-only by design** — it never writes to Headscale or NPM. If a build offers write features, do not enable them here.
- **Identity is advisory, not enforced.** A user who reaches a backend directly is still gated by Headscale ACLs and NPM. Velociportal hiding a tile is not access control.