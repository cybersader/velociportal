# No IdP / Tailscale Identity Only

This is the identity mode the current runtime supports: a human tailnet identity arrives through trusted `Tailscale-User-*` headers.

## Headers

| Header | Use |
|---|---|
| `Tailscale-User-Login` | Required matching identity |
| `Tailscale-User-Name` | Optional display name |
| `Tailscale-User-Profile-Pic` | Optional; currently not rendered |

Tagged-device and Funnel requests do not receive human identity headers.

## Safe publication

The repository's Compose example publishes the container only on host loopback:

```yaml
ports:
  - "127.0.0.1:8080:8080"
```

Inside the container, the process listens on `0.0.0.0:8080`; that does not expose it on the LAN because the host publication remains loopback-only.

A host-network Tailscale daemon or another trusted local identity proxy can reach `127.0.0.1:8080`. A sibling bridged container cannot use its own `localhost` to reach that host socket.

## Headscale caveat

Tailscale's automatic HTTPS Serve flow is not currently implemented by Headscale. See [TrueNAS SCALE: Headscale and Tailscale Serve HTTPS](../guides/truenas-scale.md#headscale-and-tailscale-serve-https) for tailnet-only HTTP and identity-proxy options.

!!! danger "Header trust is the security boundary"
    Determine the source address Velociportal actually sees and set `TRUSTED_PROXY_CIDR` narrowly. Do not expose the raw port to the LAN or internet, and do not treat a copied broad CIDR as proof that the proxy path is safe.
