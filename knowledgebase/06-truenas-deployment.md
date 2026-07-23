# 06 — TrueNAS Scale Deployment Guide

> Practical, follow-along guide for running Velociportal and its dependencies on
> TrueNAS Scale. For the *why* see `00-concept-source.md`; for locked constraints see
> `02-design-decisions.md`. This doc is the *how*.

Velociportal is a **visibility layer, not an auth layer**. Everything below assumes
your Tailscale ACL and your IdP/forward-auth still do the actual enforcement.
Velociportal only decides which service cards render for each user.

---

## 1. Architecture overview

The recommended topology **splits the control plane from the NAS**. Headscale (the
coordination server) lives on a cheap always-on VPS; NPM, Velociportal, and your
actual services live on TrueNAS Scale at home. Every node — including the NAS — runs
a Tailscale client enrolled in that Headscale.

**Why split it:** Headscale is the brain of your tailnet. If it goes down, new nodes
can't authenticate and key exchanges stall. If you run Headscale *on* the NAS and the
NAS reboots (updates, power blip, a scrub gone long), you can lose your whole overlay
network right when you need it to get back in. A $3–5/mo VPS keeps the control plane
up independently of home hardware. Existing peer-to-peer connections often survive a
brief control-plane outage, but you never want your only way back in to depend on the
box that just rebooted.

```
        Internet
           │
   ┌───────┴────────────────────────┐
   │  VPS  ($3-5/mo)                 │      Hetzner CX11 / Oracle Cloud
   │  ┌──────────────────────────┐  │      free tier / DigitalOcean droplet
   │  │ Headscale control server │  │
   │  │  - SQLite DB             │  │  ← control plane ONLY
   │  │  - /api/v1/policy (ACLs) │  │     (keep it off the NAS)
   │  └──────────────────────────┘  │
   └───────┬────────────────────────┘
           │  Tailscale (WireGuard mesh, coordinated by Headscale)
           │
   ┌───────┴────────────────────────────────────────────┐
   │  HOME — TrueNAS Scale                                │
   │                                                     │
   │   Tailscale client (NAS enrolled in Headscale)      │
   │        │                                            │
   │   ┌────┴─────┐   ┌───────────────┐   ┌───────────┐  │
   │   │   NPM    │──▶│ Velociportal  │   │ Services  │  │
   │   │ proxy    │   │ (this app)    │   │ (apps,    │  │
   │   │ hosts    │◀──│ reads NPM +   │   │  media,   │  │
   │   │ /api/... │   │ Headscale API │   │  etc.)    │  │
   │   └──────────┘   └───────────────┘   └───────────┘  │
   └─────────────────────────────────────────────────────┘
```

Velociportal reaches Headscale over the tailnet (or its public URL) and reaches NPM
locally. It never needs to be internet-facing itself.

---

## 2. Prerequisites

Before deploying Velociportal, have these in place:

- **TrueNAS Scale** with container support (the "Apps" service / Docker enabled).
- **A Headscale instance** reachable from the NAS. Run it on a VPS per the split
  above. Setup is out of scope here — follow the
  [Headscale docs](https://headscale.net/stable/) (the `Running` and
  `Reverse proxy` sections). You want it behind TLS at a stable URL, e.g.
  `https://headscale.example.com`.
- **NPM (Nginx Proxy Manager)** already running — on TrueNAS or elsewhere — with at
  least one proxy host defined. Velociportal reads its proxy-host list; it does not
  create or manage proxy hosts.
- **Tailscale client on the TrueNAS machine**, enrolled in your Headscale (`tailscale
  up --login-server https://headscale.example.com`). This is what injects identity
  headers via Tailscale Serve later.
- **A Headscale API key**, generated on the VPS:

  ```bash
  headscale apikeys create --expiration 90d
  ```

  Copy the printed key — it is shown only once. This becomes `HEADSCALE_API_KEY`.
- **NPM admin credentials** (email + password) for an account that can read proxy
  hosts. These become `NPM_EMAIL` / `NPM_PASSWORD`.

---

## 3. Deploy Velociportal on TrueNAS Scale

There is **no published image yet**, so both paths below build from source. Pick one.

### The 8 environment variables

Every deployment is configured by the same 8 vars (see `.env.example` for the
canonical list and inline comments):

| Var | Required | Purpose |
|---|---|---|
| `HEADSCALE_URL` | yes | Headscale base URL, e.g. `https://headscale.example.com` |
| `HEADSCALE_API_KEY` | yes | Bearer key from `headscale apikeys create` |
| `NPM_URL` | yes | NPM base URL, e.g. `http://npm:81` or `http://<nas-ip>:81` |
| `NPM_EMAIL` | yes | NPM admin email |
| `NPM_PASSWORD` | yes | NPM admin password |
| `LISTEN_ADDR` | no | Bind address (default `127.0.0.1:8080`) — see below |
| `POLL_INTERVAL` | no | Upstream poll cadence (default `30s`) |
| `TRUSTED_PROXY_CIDR` | no | CIDR allowed to set identity headers (default `127.0.0.1/32`) — see below |

#### `TRUSTED_PROXY_CIDR` — the security-critical one

This is the range of source IPs Velociportal will **trust to have set the
`Tailscale-User-*` identity headers**. Requests from any other source get their
identity headers ignored (treated as anonymous). Set it to match *where your proxy
sits relative to the container*:

- **Behind Tailscale Serve** (Serve injects the headers): use the Tailscale CGNAT
  range `100.64.0.0/10`, or better, the exact Tailscale IP of the node running Serve
  as a `/32`.
- **Behind NPM on the same Docker network:** use the Docker bridge range
  `172.17.0.0/16` (the compose file's default), or tighten to NPM's container IP.
- **Bare-metal behind a local proxy on the same host:** `127.0.0.1/32`.

Tighter is safer. Prefer a `/32` of the actual proxy once you know its address.
Spoofed identity headers are the core threat model — this var is the gate.

#### `LISTEN_ADDR` — Docker vs bare metal

- **In a container:** bind to the container interface, `0.0.0.0:8080`. The app's
  default of `127.0.0.1:8080` would make the published port unreachable from outside
  the container. The compose file sets `0.0.0.0:8080` for exactly this reason. This
  is safe *because* the container is isolated and `TRUSTED_PROXY_CIDR` still gates
  identity — but do not publish the port onto the LAN; keep it behind your proxy.
- **Bare metal behind a local reverse proxy:** keep `127.0.0.1:8080` so only the
  local proxy can reach it. Never bind `0.0.0.0` directly on the LAN.

### Option A — TrueNAS "Custom App" (container image)

TrueNAS Scale's **Apps → Custom App** wizard deploys a single container image. Since
there is no published image, you must first build one and make it available to the
NAS:

1. **Build and push to a registry** (from a machine with the repo + Docker):

   ```bash
   docker build -t <registry>/velociportal:latest .
   docker push <registry>/velociportal:latest
   ```

   Use a private registry, or your NAS's local registry, or `docker save` /
   `docker load` the image onto the NAS directly. (If you'd rather not run a
   registry, use **Option B** instead — it builds on the NAS.)

2. In TrueNAS: **Apps → Discover Apps → Custom App**. Fill in:
   - **Image repository / tag:** `<registry>/velociportal` / `latest`.
   - **Environment variables:** add all 8 from the table above (at minimum the 5
     required, plus `LISTEN_ADDR=0.0.0.0:8080` and your `TRUSTED_PROXY_CIDR`).
   - **Port forwarding:** container port `8080` → a host port (e.g. `8080`). Only
     expose this to your proxy, not the open LAN.
   - **Security:** run as non-root (the image already runs as uid 65534 / nobody),
     enable "read-only rootfs" if offered — the `scratch` image needs no writable
     dirs.
3. Deploy. Watch the app logs for the startup lines described in §4.

> Store `HEADSCALE_API_KEY` and `NPM_PASSWORD` as TrueNAS app secrets / env, never in
> the image. Per design decision D2, secrets come from env or a mounted file only.

### Option B — docker compose on TrueNAS (recommended while there's no image)

This builds the image on the NAS and needs no registry. It uses the repo's
`docker-compose.yml` and `.env.example` as-is.

1. Put the repo on the NAS (a dataset you can reach over SSH or SMB), then:

   ```bash
   cd /path/to/velociportal
   cp .env.example .env
   ```

2. Edit `.env` and fill in the required values:

   ```ini
   HEADSCALE_URL=https://headscale.example.com
   HEADSCALE_API_KEY=<key from: headscale apikeys create>
   NPM_URL=http://<nas-ip-or-container>:81
   NPM_EMAIL=admin@example.com
   NPM_PASSWORD=<npm admin password>

   # Container: bind to the container interface, not loopback.
   LISTEN_ADDR=0.0.0.0:8080
   POLL_INTERVAL=30s
   # Match this to where your proxy sits (see §3). Docker bridge default:
   TRUSTED_PROXY_CIDR=172.17.0.0/16
   ```

3. Build and start:

   ```bash
   docker compose up -d --build
   ```

   The compose file already sets `read_only: true`, `no-new-privileges`, and
   restart policy. It publishes `8080:8080` — adjust the host side if 8080 is taken.

4. Check it's serving from cache:

   ```bash
   docker compose logs -f velociportal
   ```

> **Health check note:** the `FROM scratch` image has no shell/curl/wget, so an
> exec-style Docker `HEALTHCHECK` can't run inside it. Probe `/healthz` from NPM or an
> external monitor instead (see §5). The compose file ships that block commented out.

---

## 4. Connecting the pieces

Three things must line up: the proxy that injects identity, the ACL that defines who
sees what, and verification that both are wired correctly.

### 4a. Tailscale Serve → Velociportal (identity injection)

Tailscale Serve is the clean way to get trusted identity headers. It **strips any
incoming `Tailscale-*` headers** and injects its own (`Tailscale-User-Login`,
`Tailscale-User-Name`, `Tailscale-User-Profile-Pic`) based on the authenticated
tailnet identity of the caller — which is exactly the anti-spoofing property
Velociportal relies on.

On the NAS (which is enrolled in Headscale), proxy Serve to the published
Velociportal port:

```bash
# Serve HTTPS on the tailnet, forwarding to the local Velociportal container port.
tailscale serve --bg --https=443 http://127.0.0.1:8080
```

Then set `TRUSTED_PROXY_CIDR` to the source range Serve presents to the container. If
Serve and the container share the host, that's the loopback/Docker path the request
takes — verify with the "untrusted source" check in §6 and tighten to a `/32` once
you see the real source IP in the logs.

If you use **NPM instead of Serve** to front Velociportal, NPM must be behind the
Tailscale identity layer and forward the `Tailscale-*` (or `X-Webauth-*`) headers
through. In that case trust NPM's container/host IP in `TRUSTED_PROXY_CIDR`. Do not
trust a proxy that doesn't itself strip and re-inject identity — otherwise a client
could spoof the headers.

### 4b. Headscale ACL policy — define groups and access

Velociportal reads groups and ACL rules from `GET /api/v1/policy`. Define your groups
and access rules in the Headscale policy so there's something to correlate against.
Example policy (huJSON):

```jsonc
{
  "groups": {
    "group:family": ["alice@example.com", "bob@example.com"],
    "group:admins": ["alice@example.com"],
  },
  "tagOwners": {
    "tag:media": ["group:admins"],
  },
  "acls": [
    // Family can reach the media server host.
    { "action": "accept", "src": ["group:family"], "dst": ["tag:media:*"] },
    // Admins can reach everything.
    { "action": "accept", "src": ["group:admins"], "dst": ["*:*"] },
  ],
}
```

Apply it on the VPS:

```bash
headscale policy set -f policy.hujson
```

Velociportal resolves each requesting user to their groups, then for each NPM proxy
host decides whether an ACL rule grants that user a path to it. Only matching services
render as cards. (See D6 in `02-design-decisions.md` for the matching logic.)

### 4c. Verify it works

- **Logs on startup** should show Velociportal reaching both upstreams and populating
  the cache — a successful Headscale policy fetch and NPM proxy-host fetch, then
  request lines with timing. Structured slog output; set debug level to see identity
  extraction per request.
- **`/healthz`** should return `200` once the cache is warm (it returns `503` while
  empty or stale — older than 3× `POLL_INTERVAL`).
- **Hit the portal through Serve/NPM** as a real user. You should see only the cards
  that user's ACL groups can reach. Log in as a second user in a different group and
  confirm the card set differs.
- **Confirm identity is trusted:** a request *through the proxy* should be attributed
  to the logged-in user; a request straight to the container port (bypassing the
  proxy) should be treated as anonymous / rejected, proving `TRUSTED_PROXY_CIDR` is
  doing its job.

---

## 5. Resilience tips

- **Keep Headscale on the VPS.** Reiterating §1: the control plane must survive NAS
  reboots. Home hardware reboots for updates and scrubs; the tailnet brain shouldn't
  go with it.
- **Back up the Headscale SQLite DB.** Headscale's state (nodes, pre-auth keys,
  policy) lives in its SQLite database — typically `/var/lib/headscale/db.sqlite`
  (check your config's `database.sqlite.path`). Also back up `config.yaml` and the
  noise/private keys directory. Automate it, e.g. daily on the VPS:

  ```bash
  # /etc/cron.daily/headscale-backup (make executable)
  sqlite3 /var/lib/headscale/db.sqlite ".backup '/root/backups/headscale-$(date +\%F).sqlite'"
  find /root/backups -name 'headscale-*.sqlite' -mtime +14 -delete
  ```

  Ship those backups off-box (rsync/restic to the NAS or object storage) so a dead
  VPS is a quick restore, not a rebuild.
- **NAS reboot behavior.** When the NAS reboots, its Tailscale client reconnects to
  the still-running Headscale on the VPS and rejoins the mesh; Velociportal restarts
  (restart policy) and repopulates its in-memory cache on first poll. Nothing on the
  NAS is a single point of failure for the tailnet, because the control plane isn't
  there.
- **Velociportal itself is stateless.** It holds only an in-memory cache — no DB, no
  volumes to back up. Losing the container loses nothing; it re-derives everything
  from the two APIs on restart.
- **Monitoring.** Point an external uptime monitor at `/healthz`. A `200` means the
  cache is warm and both upstreams are (or recently were) reachable; `503` means the
  cache is empty or stale and warrants a look at the logs.

---

## 6. Troubleshooting

**403 "untrusted source"**
The request's source IP isn't inside `TRUSTED_PROXY_CIDR`, so Velociportal refuses to
honor its identity headers. Find the real source IP in the logs and set
`TRUSTED_PROXY_CIDR` to cover it (ideally as a `/32`). Common cause: assuming the
Docker bridge `172.17.0.0/16` but the proxy actually arrives on a different Docker
network or over the Tailscale `100.64.0.0/10` range.

**401 "no identity"**
The request reached Velociportal from a trusted source but carried **no**
`Tailscale-User-*` headers. Your proxy isn't forwarding/injecting them. If using
Tailscale Serve, confirm the Serve config points at the right port and the caller is
authenticated on the tailnet. If using NPM, ensure the identity layer sits *in front*
of NPM and NPM passes the headers through.

**Empty portal (page renders, zero cards)**
Identity resolved fine, but the ACL matcher found no service the user's groups can
reach — usually the ACL↔proxy-host join isn't matching. The matcher keys on the NPM
proxy host's `ForwardHost` (backend IP) against ACL rule targets. Check that your ACL
`dst` values actually correspond to the proxy hosts' forward targets (or the relevant
tags/CIDRs). Enable debug logging to see which hosts matched which rules. See the
open "ACL ↔ proxy-host join" question in `04-handoff-context.md` — real data may need
domain-name or `access_list_id` matching.

**Stale cache / old data / `/healthz` returns 503**
An upstream API is unreachable, so the cache can't refresh (Velociportal keeps serving
the last good data, then goes stale). Verify `HEADSCALE_URL` and `NPM_URL` are correct
and reachable *from the container* (DNS, tailnet routing, NPM listening on the
expected port). Check `HEADSCALE_API_KEY` hasn't expired (`headscale apikeys list` on
the VPS) and `NPM_EMAIL`/`NPM_PASSWORD` still authenticate. The logs will name which
upstream failed.

---

## Related docs

- `00-concept-source.md` — problem statement and the "why."
- `01-api-research.md` — Headscale + NPM API endpoints and auth details.
- `02-design-decisions.md` — locked constraints (single container, data sources,
  identity model).
- `04-handoff-context.md` — current implementation state and open questions.
- Repo root: `docker-compose.yml`, `.env.example`, `Dockerfile`.
