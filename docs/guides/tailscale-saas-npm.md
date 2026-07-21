# Tailscale SaaS + NPM Reference Architecture

How to run Velociportal against **Tailscale's managed control plane** (the SaaS at `tailscale.com`) instead of self-hosted Headscale. Nginx Proxy Manager (NPM) integration is unchanged.

!!! info "Velociportal complements your IdP — it does not replace it"
    Velociportal is a **visibility layer**. It reads your Tailscale ACLs and NPM proxy hosts and renders a per-user portal. Authentication, SSO, and identity still come from Tailscale's connected IdP (Google, GitHub, Okta, etc.). Velociportal never issues sessions or holds credentials for your users.

If you're coming from the [Headscale + NPM guide](headscale-npm.md), read that first — this page only covers **what changes** when the control plane is Tailscale SaaS.

## What's different at a glance

| | Headscale (self-hosted) | Tailscale SaaS |
|---|---|---|
| Control plane | Your container | Managed by Tailscale |
| API base | `https://headscale.example.com/api/v1` | `https://api.tailscale.com/api/v2` |
| Auth scheme | `Authorization: Bearer <key>` | HTTP Basic — API key as username, blank password |
| ACL endpoint | `/api/v1/policy` | `/api/v2/tailnet/{tailnet}/acl` |
| Users/devices | `/api/v1/user`, `/api/v1/node` | `/api/v2/tailnet/{tailnet}/devices` |
| Runs in Compose | Yes | No — it's SaaS |
| Identity headers | Tailscale Serve | Tailscale Serve (identical) |
| NPM integration | Identical | Identical |

## Architecture

Because the control plane is hosted, your stack shrinks to just NPM and Velociportal. Velociportal reaches **out** to the Tailscale API over the internet, and **in** to NPM on the local network.

```mermaid
flowchart LR
    user([Human user<br/>on tailnet]) -->|Tailscale Serve<br/>injects identity headers| vp[Velociportal<br/>container]
    vp -->|HTTPS Basic auth<br/>read ACL + devices| ts[(Tailscale SaaS<br/>api.tailscale.com)]
    vp -->|JWT login<br/>read proxy hosts| npm[Nginx Proxy Manager]
    idp([Your IdP<br/>Google / Okta / GitHub]) -.->|SSO / auth| ts
    npm -->|reverse proxy| svc1[Service A]
    npm -->|reverse proxy| svc2[Service B]
```

The dotted line is the part Velociportal never touches: your IdP authenticates users into the tailnet. Velociportal only *reads* the resulting ACL grants.

## Docker Compose

Just two services. No Headscale, no control-plane volumes.

```yaml
services:
  npm:
    image: jc21/nginx-proxy-manager:latest
    restart: unless-stopped
    ports:
      - "80:80"
      - "81:81"     # NPM admin UI
      - "443:443"
    volumes:
      - ./npm/data:/data
      - ./npm/letsencrypt:/etc/letsencrypt

  velociportal:
    image: velociportal:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      # --- Tailscale SaaS ---
      TAILSCALE_API_KEY: ${TAILSCALE_API_KEY}
      TAILSCALE_TAILNET: ${TAILSCALE_TAILNET}    # e.g. "example.com" or "example.org.github"
      # --- NPM ---
      NPM_BASE_URL: http://npm:81
      NPM_EMAIL: ${NPM_EMAIL}
      NPM_PASSWORD: ${NPM_PASSWORD}
    depends_on:
      - npm
```

```bash title=".env"
TAILSCALE_API_KEY=tskey-api-xxxxxxxxxxxx
TAILSCALE_TAILNET=example.com
[email protected]
NPM_PASSWORD=changeme
```

!!! warning "Rotate the Tailscale API key"
    Tailscale API keys (`tskey-api-...`) expire after at most 90 days and grant **full read/write** to your tailnet — there is no read-only scope. Treat it like a root credential: keep it in a secret store, rotate it, and never bake it into an image. Velociportal only reads, but the key itself can do more.

## Config differences

Swap the `HEADSCALE_*` variables for `TAILSCALE_*`:

=== "Tailscale SaaS"

    ```bash
    TAILSCALE_API_KEY=tskey-api-xxxxxxxxxxxx
    TAILSCALE_TAILNET=example.com
    ```

=== "Headscale"

    ```bash
    HEADSCALE_URL=https://headscale.example.com
    HEADSCALE_API_KEY=<headscale-api-key>
    ```

### Finding your tailnet name

The `TAILSCALE_TAILNET` value is the **organization name** shown in the admin console, not a hostname. Common forms:

- Custom domain: `example.com`
- GitHub org login: `example.org.github`
- The literal `-` — an alias for "the default tailnet of the authenticated key" (handy if you don't want to hardcode it)

### API call shape

The auth scheme is the biggest gotcha — Tailscale SaaS uses **HTTP Basic**, with the API key as the username and an empty password:

```bash
# List devices
curl -u "${TAILSCALE_API_KEY}:" \
  "https://api.tailscale.com/api/v2/tailnet/${TAILSCALE_TAILNET}/devices"

# Fetch the ACL policy (HuJSON)
curl -u "${TAILSCALE_API_KEY}:" \
  -H "Accept: application/hujson" \
  "https://api.tailscale.com/api/v2/tailnet/${TAILSCALE_TAILNET}/acl"
```

Contrast with Headscale, which uses `Authorization: Bearer <key>` against `/api/v1`.

## Identity headers (unchanged)

Serving Velociportal over Tailscale Serve works exactly as in the Headscale guide — Serve injects the identity headers Velociportal reads:

```bash
tailscale serve --bg 8080
```

This gives you:

- `Tailscale-User-Login` — e.g. `[email protected]`
- `Tailscale-User-Name` — display name
- `Tailscale-User-Profile-Pic` — avatar URL

!!! danger "Serve identity has hard limits"
    These headers are only injected for **human users reaching Velociportal over the tailnet via Serve**. They are **not** present for:

    - **Tagged devices** (e.g. `tag:ci`) — tagged nodes have no user identity
    - **Funnel** traffic — public internet requests carry no tailnet identity

    Never expose Velociportal over Funnel and trust these headers — an attacker could forge them. Keep it tailnet-only behind Serve.

## ACL feature differences

Velociportal reads the same HuJSON policy format (`groups`, `tagOwners`, `acls`, `grants`) on both platforms, but the managed control plane has features Headscale doesn't fully implement:

!!! note "Behavior may differ between Tailscale SaaS and Headscale"
    - **`grants`** (the newer capability-based rules) are first-class on Tailscale SaaS; Headscale support lags and may parse them differently.
    - **`autogroup:*`** members (`autogroup:member`, `autogroup:admin`, `autogroup:tagged`) resolve against Tailscale's identity system — Headscale supports a smaller subset.
    - **`ssh`** blocks, **posture** conditions, and **grant** app capabilities exist on SaaS but may be absent or partial in Headscale.
    - **Users vs. nodes**: Headscale exposes users and nodes on separate endpoints; Tailscale rolls both into `/devices` plus the ACL's `groups`.

    If your portal shows fewer (or more) services than expected, diff the raw ACL from the API against what Velociportal resolved — the difference is almost always an unsupported ACL construct, not a Velociportal bug.

## NPM integration (unchanged)

Identical to the Headscale guide. Velociportal authenticates to NPM with credential-based JWT and reads proxy hosts:

```bash
# NPM has no read-only token — you POST email/password for a JWT
curl -X POST http://npm:81/api/tokens \
  -H "Content-Type: application/json" \
  -d '{"identity":"'"${NPM_EMAIL}"'","secret":"'"${NPM_PASSWORD}"'"}'
```

!!! tip "Use a dedicated NPM read account"
    Since NPM only offers full credential login (no scoped read-only API token), create a **separate NPM user** for Velociportal so you can rotate or revoke it independently of your admin login.

## Next steps

- Confirm your API key resolves: `curl -u "$TAILSCALE_API_KEY:" https://api.tailscale.com/api/v2/tailnet/$TAILSCALE_TAILNET/devices`
- Serve Velociportal: `tailscale serve --bg 8080`
- Point NPM at your services and let Velociportal render the per-user view

Remember: Velociportal shows *what a user is already entitled to reach* according to Tailscale and NPM. It is a lens on your existing identity and access setup — not a gate, and not a replacement for your IdP.