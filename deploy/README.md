# Deployment bundle

This directory is the production, Compose-native deployment source for TrueNAS Custom App YAML, Dockge, Dokploy, and ordinary Docker Compose.

It runs exactly one Velociportal container from a published immutable image and uses `pull_policy: always` so a tagged deployment consults the registry instead of silently reusing a cached local tag. It does not build source or deploy Headscale, NPM, Tailscale, PKI, an identity gateway, or `headscale-ops`.

Requirements:

- Docker Compose **2.33.1 or newer** for `env_file.format: raw` and service-network `gw_priority`.
- Docker Engine **28.0 or newer** for localhost-published ports to remain unreachable from hosts on the same L2 network segment.
- An immutable image tag or digest that has actually been published and verified.
- A root/UI project name distinct from the repository workflow's `velociportal` project; direct use defaults to `velociportal-production`, while TrueNAS uses the Application Name field with a short-form include wrapper.
- Existing Headscale and NPM apps attached through UI-managed settings to `velociportal-upstreams` with exact aliases `headscale.velociportal.internal` and `npm.velociportal.internal`.
- Headscale port `8080` exposed only to attached containers and never LAN-published.
- Existing NPM trusted HTTPS for pre-tailnet Headscale clients and HTTPS-only `headscale-ops`, with WebSocket/upgrade preservation, separate operator/runtime keys, safe header logging, and tested NPM backups.
- Tailnet-routable service destinations, plus an ACL rule allowing intended users to reach the Tailscale Serve node on port `8081`.

Files:

- `compose.yaml` — portable one-service base stack with no bind mounts.
- `compose.private-ca.yaml` — optional bind-mount overlay for the public root certificate of a private certificate authority.
- `stack.env.example` — non-secret Compose interpolation values for the base stack.
- `velociportal.env.example` — credentialed application settings using raw quoted values.
- `tailscale-serve.json.example` — declarative tailnet-only HTTP Serve route for the existing Tailscale app.
- `policy.hujson.example` — two-user/two-service legacy ACL seed that also permits both users to reach the Serve node on port `8081`.

## Network shape

The base service attaches to exactly two networks:

- `default` is the fixed-subnet ingress bridge. Its gateway is the expected trusted source for host-loopback Tailscale Serve traffic, and the application remains published only as `127.0.0.1:18080:8080`. Its `gw_priority: 1` keeps it as Velociportal's preferred default route.
- `upstreams` has the stable Docker name `velociportal-upstreams`. It is a normal private user-defined bridge with `gw_priority: 0`, so Headscale and NPM retain the outbound NAT and Docker DNS they require when TrueNAS selects it as their only rendered service network.

A normal bridge does not publish container ports to the LAN. It permits attached containers to communicate and provides outbound NAT; LAN reachability still requires an explicit host publication or separately configured direct routing. Keep untrusted containers off the bridge and confirm the final LAN-negative checks.

TrueNAS catalog renderer library 2.3.4 replaces a service's implicit app-default network when any UI-managed network is selected. Attach the existing Headscale and NPM containers to `velociportal-upstreams` one at a time through their UI-managed settings, then verify outbound DNS and application health before continuing. Give them the aliases `headscale.velociportal.internal` and `npm.velociportal.internal`, respectively. Those aliases are private Docker DNS names; do not publish them in DNS or use recurring `docker network connect` shell commands. Set Headscale's host port mode to None/Expose only after its private alias and NPM HTTPS control path both pass, so container port `8080` is never LAN-published in the accepted final state.

A separately managed Compose stack can join the network with an external declaration such as:

```yaml
networks:
  velociportal-upstreams:
    external: true
    name: velociportal-upstreams
```

Then assign the appropriate alias under that upstream service's network attachment. Do not add Headscale or NPM as services in this bundle.

## Base and private-CA overlay

The base stack requires no CA file and mounts no host path:

```bash
docker compose --env-file stack.env -f compose.yaml up --detach --wait
```

Use `compose.private-ca.yaml` only when an upstream certificate chains to a private public root that is not already in the image trust store. Supply the readable public certificate path only for that overlay:

```bash
VELOCIPORTAL_CA_FILE=/absolute/host/path/rootCA.pem docker compose --env-file stack.env -f compose.yaml -f compose.private-ca.yaml up --detach --wait
```

The overlay modifies only the existing Velociportal service. It adds one read-only bind at `/etc/ssl/certs/velociportal-private-ca.crt`, refuses to create a missing source path, and adds no service, network, or port. Never mount a CA private key, leaf private key, directory, or combined private-key bundle.

Follow the [TrueNAS Quickstart](../docs/guides/truenas-scale.md) rather than deploying these examples without completing the NPM control-proxy, key separation, private-network, identity, LAN-negative, restart, backup/restore, join, and reachability gates. The base stack requires no manual CA lifecycle; the certificate on the canonical NPM Headscale endpoint comes from the operator's existing automated NPM lifecycle. If a pre-tailnet client does not trust it, stop rather than disabling verification.

No public image, `headscale-ops` artifact, or support claim is assumed until tagged releases are published and anonymously verified and real acceptance passes. Never substitute `latest`, a NAS source build, disabled verification, or recurring NAS shell operations.
