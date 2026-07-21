# How It Works

Velociportal is a read-only visibility layer. It watches your Headscale ACLs and Nginx Proxy Manager (NPM) proxy hosts, then renders a per-user dashboard showing only the services that user is allowed to reach.

!!! important "Velociportal complements your IdP, it does not replace it"
    Velociportal never issues tokens, never authenticates users, and never gates traffic. Access enforcement stays with your identity provider, Tailscale ACLs, and reverse proxy. Velociportal only *reflects* what those systems already decided. If a user sees a service in their portal, it is because your ACLs already grant it, not the other way around.

## Data flow

```text
                       ┌───────────────────────────────────────────┐
                       │            Velociportal (Go)              │
                       │                                           │
  Headscale API  ──1──▶│  poll ACL policy (groups, users, grants)  │
  (Bearer key)         │                                           │
                       │            ┌──────────────┐               │
  NPM API        ──2──▶│  poll  ──▶ │ in-memory    │ ◀── match  3  │
  (JWT)                │  hosts     │ cache        │               │
                       │            └──────────────┘               │
                       └───────────────▲───────────────┬───────────┘
                                       │               │
   Browser ─── request ──▶ Tailscale Serve ──4── header │
   (human on tailnet)      injects identity            │
                              Tailscale-User-Login       │
                                                        ▼
                                              5  filter + render
                                                 authorized services only
```

### 1. Poll Headscale for ACL policy

Velociportal calls the Headscale REST API at `/api/v1` with a Bearer API key and reads the ACL policy (Tailscale huJSON: `groups`, `tagOwners`, `acls`, `grants`). This tells it which users belong to which groups and which groups may reach which destinations.

=== "Config"

    ```yaml
    headscale:
      url: https://headscale.example.com
      api_key: ${HEADSCALE_API_KEY}   # Bearer token
      poll_interval: 60s
    ```

=== "What it reads"

    ```jsonc
    {
      "groups": {
        "group:eng": ["alice@example.com", "bob@example.com"],
        "group:ops": ["carol@example.com"]
      },
      "acls": [
        { "action": "accept", "src": ["group:eng"], "dst": ["npm.example.com:443"] }
      ]
    }
    ```

### 2. Poll NPM for proxy hosts

NPM has no scoped read-only API token. Velociportal authenticates with credentials (`POST /api/tokens` with email + password) to obtain a JWT, then lists proxy hosts to learn the service catalog: domains, forward hosts, and ports.

!!! warning "NPM credentials are admin-level"
    Because NPM only issues credential-based JWTs, the account you give Velociportal has full NPM access. Use a dedicated NPM user, store the password as a secret, and keep Velociportal bound to localhost (see [Security model](#security-model)).

```yaml
npm:
  url: https://npm.example.com
  identity: velociportal@example.com
  secret: ${NPM_PASSWORD}
  poll_interval: 60s
```

### 3. Match ACL rules to proxy hosts

Velociportal correlates each NPM proxy host (e.g. `grafana.example.com`) with the ACL rules whose destinations resolve to it. The result is a table of `service -> allowed groups`, held in memory.

| Service | Domain | Allowed groups |
|---|---|---|
| Grafana | grafana.example.com | group:eng, group:ops |
| Registry | registry.example.com | group:ops |

### 4. Read identity on request

When a user opens the portal over Tailscale Serve, Serve injects identity headers. Velociportal reads `Tailscale-User-Login`, looks up that user's groups from the cached ACL policy, and prepares to filter.

```text
GET / HTTP/1.1
Tailscale-User-Login: alice@example.com
Tailscale-User-Name: Alice
Tailscale-User-Profile-Pic: https://...
```

!!! danger "Identity headers are only trustworthy behind Tailscale Serve"
    These headers are injected by Serve for **human users over the tailnet**. They are **not** present for tagged devices and **not** injected over Funnel (public internet). Anything can forge an HTTP header, so Velociportal must only ever receive traffic from Serve on localhost. Never expose the raw port.

### 5. Render authorized services

Velociportal intersects the user's groups with the `service -> allowed groups` table and renders only the matching services (Go + templ + htmx). Alice in `group:eng` sees Grafana; she never sees the Registry.

## Caching

Velociportal keeps everything in memory. There is no database.

- Headscale ACLs and NPM hosts are refreshed on independent timers (default 60s).
- Requests are served entirely from the last good cached snapshot, so a slow or down upstream never blocks the portal.
- On startup it does one synchronous poll of each source before serving.
- Restart the container to force a cold reload; there is no persisted state to migrate.

!!! note "Eventual consistency"
    A new proxy host or ACL change appears in the portal within one poll interval, not instantly. Tighten `poll_interval` if you need faster propagation, but mind the load on Headscale and NPM.

## Security model

<a name="security-model"></a>

1. **Trust identity headers only from Tailscale Serve.** Velociportal treats `Tailscale-User-Login` as authoritative *only* because Serve terminates the connection and sets it. Reachable any other way, that header is attacker-controlled.
2. **Bind to localhost.** Run Velociportal on `127.0.0.1:<port>` and put `tailscale serve` in front of it. Do not publish the port with Docker (`-p`) to `0.0.0.0`, and do not use Funnel.
3. **Read-only by design.** Velociportal issues no writes to Headscale or NPM and enforces no access itself. Compromising it leaks *visibility* into your service map, not control of your network.
4. **Secrets as env vars.** Keep the Headscale API key and NPM password out of the image and compose file; inject them at runtime.

=== "docker-compose.yml"

    ```yaml
    services:
      velociportal:
        image: velociportal:latest
        # localhost only — Serve reaches it via the host
        ports:
          - "127.0.0.1:8080:8080"
        environment:
          HEADSCALE_API_KEY: ${HEADSCALE_API_KEY}
          NPM_PASSWORD: ${NPM_PASSWORD}
    ```

=== "Tailscale Serve"

    ```bash
    # Terminate on the tailnet and inject identity headers
    tailscale serve --bg 8080
    ```

!!! important "Enforcement lives upstream"
    Even if a user somehow saw a service they should not, the ACLs and reverse proxy still block the actual connection. Velociportal is a map of the doors you can open, not the lock on any of them.