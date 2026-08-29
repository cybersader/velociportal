# Deployment bundle

This directory is the production, Compose-native deployment source for TrueNAS Custom App YAML, Dockge, Dokploy, and ordinary Docker Compose.

It runs exactly one Velociportal container from a published immutable image and uses `pull_policy: always` so a tagged deployment consults the registry instead of silently reusing a cached local tag. It does not build source or deploy Headscale, NPM, Tailscale, PKI, an identity gateway, or `headscale-ops`.

Requirements:

- Docker Compose **2.33.1 or newer** for `env_file.format: raw` and service-network `gw_priority`.
- Docker Engine **28.0 or newer** for localhost-published ports to remain unreachable from hosts on the same L2 network segment.
- An immutable image tag or digest that has actually been published and verified.
- A root/UI project name distinct from the repository workflow's `velociportal` project; direct use defaults to `velociportal-production`, while TrueNAS uses the Application Name field with a short-form include wrapper.
- Exactly one selected control plane: Headscale (supported path) or Tailscale SaaS (labeled preview).
- NPM attached through UI-managed settings to `velociportal-upstreams` with exact alias `npm.velociportal.internal`.
- In Headscale mode, Headscale also attaches with alias `headscale.velociportal.internal`; port `8080` is exposed only to attached containers and never LAN-published.
- In Headscale mode, existing NPM provides trusted HTTPS for pre-tailnet clients and HTTPS-only `headscale-ops`, with WebSocket/upgrade preservation, separate operator/runtime keys, safe header logging, and tested backups.
- In Tailscale mode, a dedicated OAuth client has exactly the documented four read scopes; Headscale and the NPM Headscale control proxy are not required.
- Tailnet-routable service destinations, plus policy permission for intended users to reach the Tailscale Serve node on port `8081`.

Files:

- `compose.yaml` — portable one-service base stack with no bind mounts.
- `compose.private-ca.yaml` — optional bind-mount overlay for the public root certificate of a private certificate authority.
- `compose.service-metadata.yaml` — optional read-only service name/URL metadata overlay with a fixed in-container path and supplemental read group.
- `service-metadata.example.json` — strict version-1 metadata example keyed by an existing NPM proxy-host ID.
- `stack.env.example` — non-secret Compose interpolation values for the base stack.
- `velociportal.env.example` — explicit Headscale application settings using raw quoted values.
- `velociportal.tailscale.env.example` — OAuth-only Tailscale SaaS preview settings with no access token, API URL, or explicit tailnet.
- `tailscale-serve.json.example` — declarative tailnet-only HTTP Serve route for the existing Tailscale app.
- `policy.hujson.example` — two-user/two-service legacy ACL seed that also permits both users to reach the Serve node on port `8081`.

## Network shape

The base service attaches to exactly two networks:

- `default` is the fixed-subnet ingress bridge. Its gateway is the expected trusted source for host-loopback Tailscale Serve traffic, and the application remains published only as `127.0.0.1:18080:8080`. Its `gw_priority: 1` keeps it as Velociportal's preferred default route and provides Tailscale SaaS HTTPS egress in preview mode.
- `upstreams` has the stable Docker name `velociportal-upstreams`. It is a normal private user-defined bridge with `gw_priority: 0`. NPM always uses it; Headscale also uses it in Headscale mode. The bridge preserves outbound NAT and Docker DNS when TrueNAS selects it as an upstream app's only rendered service network.

A normal bridge does not publish container ports to the LAN. It permits attached containers to communicate and provides outbound NAT; LAN reachability still requires an explicit host publication or separately configured direct routing. Keep untrusted containers off the bridge and confirm the final LAN-negative checks.

TrueNAS catalog renderer library 2.3.4 replaces a service's implicit app-default network when any UI-managed network is selected. Always attach NPM to `velociportal-upstreams` through UI-managed settings and give it alias `npm.velociportal.internal`. In Headscale mode, attach Headscale separately, verify outbound DNS and health, and give it alias `headscale.velociportal.internal`; then set its host port mode to None/Expose only after the private alias and NPM HTTPS control path pass. In Tailscale mode, do not add Headscale merely for Velociportal. The aliases are private Docker DNS names; do not publish them in DNS or use recurring `docker network connect` shell commands.

A separately managed Compose stack can join the network with an external declaration such as:

```yaml
networks:
  velociportal-upstreams:
    external: true
    name: velociportal-upstreams
```

Then assign the appropriate alias under that upstream service's network attachment. Do not add Headscale or NPM as services in this bundle.

## Select one provider environment file

Copy exactly one provider example to the path selected by `VELOCIPORTAL_ENV_FILE`:

```text
# Supported Headscale path
cp velociportal.env.example velociportal.env

# Tailscale SaaS preview path
cp velociportal.tailscale.env.example velociportal.env
```

Both files set `CONTROL_PLANE` explicitly. Existing v0.2 Headscale files without the selector still start with a value-free deprecation warning, but v0.3 will require explicit selection. Do not combine both credential families. The setup wizard confirms and atomically removes inactive known keys during a provider switch while preserving unknown keys.

Tailscale mode always uses OAuth client credentials against the fixed verified API origin. It has no access-token variable, API-key fallback, configurable API URL, or explicit tailnet variable.

## Base and optional overlays

The base stack requires no CA or metadata file and mounts no host path:

```bash
docker compose --env-file stack.env -f compose.yaml up --detach --wait
```

Use `compose.private-ca.yaml` only when an upstream certificate chains to a private public root that is not already in the image trust store. Supply the readable public certificate path only for that overlay:

```bash
VELOCIPORTAL_CA_FILE=/absolute/host/path/rootCA.pem docker compose --env-file stack.env -f compose.yaml -f compose.private-ca.yaml up --detach --wait
```

The private-CA overlay modifies only the existing Velociportal service. It adds one read-only bind at `/etc/ssl/certs/velociportal-private-ca.crt`, refuses to create a missing source path, and adds no service, network, or port. Never mount a CA private key, leaf private key, directory, or combined private-key bundle.

Use `compose.service-metadata.yaml` only when an existing NPM proxy host lacks the concrete browser name or URL Velociportal should display. Adding the real concrete hostname to the same NPM proxy host is preferred because NPM remains the service-name source of truth. The metadata file cannot create a card or change policy matching.

```bash
VELOCIPORTAL_SERVICE_METADATA_FILE=/absolute/host/path/velociportal-services.json VELOCIPORTAL_SERVICE_METADATA_GID=950 docker compose --env-file stack.env -f compose.yaml -f compose.service-metadata.yaml up --detach --wait
```

The overlay mounts the file read-only at `/velociportal-services.json`, sets `SERVICE_METADATA_FILE` to that fixed target, refuses to create a missing source, and adds only the supplied numeric supplemental group. On the canonical TrueNAS dataset, preserve directory ownership/mode `950:950`/`0750` and file ownership/mode `950:950`/`0640`; the group is how the non-root container reads the file. Do not loosen or change dataset permissions for this feature. The private-CA and metadata overlays can be stacked by including both files.

Follow the [TrueNAS Quickstart](../docs/guides/truenas-scale.md) rather than deploying these examples without completing the selected provider's credential/control-plane checks plus private-network, identity, LAN-negative, restart, backup/restore, join, and reachability gates. Tailscale remains preview until its dedicated live OAuth and policy acceptance passes. The base stack requires no manual CA lifecycle; the certificate on the canonical NPM Headscale endpoint comes from the operator's existing automated NPM lifecycle. If a pre-tailnet client does not trust it, stop rather than disabling verification.

Published release-candidate images and `headscale-ops` artifacts do not imply a public support claim; real acceptance still must pass. Never substitute `latest`, a NAS source build, disabled verification, or recurring NAS shell operations.
