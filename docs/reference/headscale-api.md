# Headscale API

Velociportal talks to your [Headscale](https://headscale.net) control server over its gRPC-gateway REST API to read policy, users, and nodes. It uses this data to make access decisions at the reverse proxy — it does **not** modify your tailnet.

!!! note "Velociportal complements your IdP"
    Velociportal is not an identity provider. It reads Headscale's users, nodes, and ACL policy to enforce access at the edge. Your existing IdP (OIDC, etc.) still authenticates humans. Velociportal sits alongside it, not in place of it.

## Authentication

All requests use a Bearer API key. Generate one on the Headscale host:

```bash
headscale apikeys create --expiration 90d
```

Pass it in the `Authorization` header:

```bash
curl -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  https://headscale.example.com/api/v1/node
```

!!! tip "Configuration"
    Set these in Velociportal's environment:

    === "Docker Compose"

        ```yaml
        environment:
          HEADSCALE_URL: https://headscale.example.com
          HEADSCALE_API_KEY: ${HEADSCALE_API_KEY}
        ```

    === "Shell"

        ```bash
        export HEADSCALE_URL=https://headscale.example.com
        export HEADSCALE_API_KEY=hskey-api-xxxxxxxx...
        ```

!!! warning "Keys expire"
    API keys created with `--expiration` stop working at that time. Rotate before expiry or Velociportal loses access and falls back to default-allow (see below).

## Endpoints

### GET /api/v1/policy

Returns the ACL policy in [huJSON](https://github.com/tailscale/hujson) (JSON with comments and trailing commas). Velociportal parses this to map users and tags to services.

```bash
curl -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  https://headscale.example.com/api/v1/policy
```

```json
{
  "policy": "{\n  // groups reference Headscale users\n  \"groups\": {\n    \"group:admin\": [\"alice@\"]\n  },\n  \"tagOwners\": {\n    \"tag:proxy\": [\"group:admin\"]\n  },\n  \"acls\": [\n    {\n      \"action\": \"accept\",\n      \"src\": [\"group:admin\"],\n      \"dst\": [\"tag:proxy:443\"]\n    }\n  ]\n}\n",
  "updatedAt": "2026-07-21T10:12:04Z"
}
```

The `policy` field is a string containing the raw huJSON document. Decode and parse it before use.

!!! info "Default-allow when no policy is set"
    If Headscale has no policy configured, this endpoint returns an empty or error response. Velociportal treats "no policy" as **default-allow** — every authenticated node can reach every service. This is the opposite of Tailscale's default-deny. Set an explicit policy to lock things down.

### GET /api/v1/user

Lists Headscale users. Velociportal matches these against `src` entries and groups in the policy.

```bash
curl -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  https://headscale.example.com/api/v1/user
```

```json
{
  "users": [
    {
      "id": "1",
      "name": "alice",
      "createdAt": "2026-06-01T09:00:00Z"
    },
    {
      "id": "2",
      "name": "bob",
      "createdAt": "2026-06-14T14:22:10Z"
    }
  ]
}
```

### GET /api/v1/node

Lists nodes with their tags. Velociportal uses `forcedTags` (and `validTags`) to resolve `tag:*` destinations in the policy — for example routing `tag:proxy` to `npm.example.com`.

```bash
curl -H "Authorization: Bearer $HEADSCALE_API_KEY" \
  https://headscale.example.com/api/v1/node
```

```json
{
  "nodes": [
    {
      "id": "7",
      "name": "npm-proxy",
      "user": { "id": "1", "name": "alice" },
      "ipAddresses": ["100.64.0.7", "fd7a:115c:a1e0::7"],
      "forcedTags": ["tag:proxy"],
      "validTags": ["tag:proxy"],
      "online": true,
      "lastSeen": "2026-07-21T10:11:58Z"
    }
  ]
}
```

!!! tip "Tags drive routing"
    A node tagged `tag:proxy` serving `npm.example.com` is only reachable by policy `src` entries whose `dst` includes `tag:proxy`. Untagged nodes fall back to their owning user.

## Divergences from Tailscale

Headscale implements a subset of Tailscale's policy model. Velociportal accounts for these gaps:

| Feature | Tailscale | Headscale |
|---|---|---|
| `srcPosture` (device posture in ACLs) | Supported | **Unsupported** — ignored if present |
| IP sets (`ipsets`) | Supported | **Unsupported** |
| Default behavior with no policy | Default-deny | **Default-allow** |

!!! warning "Do not rely on posture checks"
    If your policy uses `srcPosture`, Headscale silently ignores it and Velociportal cannot enforce it. Do not treat device-posture rules written for Tailscale as active on Headscale. Use tags and users for segmentation instead.

!!! danger "Default-allow is a footgun"
    Because Headscale defaults to allow-all with no policy, a misconfigured or unreachable policy endpoint means Velociportal opens access rather than closing it. Always define an explicit `acls` block, and monitor Velociportal's policy-fetch health.