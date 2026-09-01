# Nginx Proxy Manager API

Velociportal uses Nginx Proxy Manager (NPM) as its current service catalog.

## Runtime calls

1. `POST /api/tokens` with `identity` and `secret` to obtain a JWT.
2. `GET /api/nginx/proxy-hosts` with that Bearer token.
3. On `401`, authenticate once more and retry the request.

The current polling cache does **not** fetch access lists.

## Proxy-host fields used

```json
{
  "id": 12,
  "domain_names": ["grafana.example.com"],
  "forward_scheme": "http",
  "forward_host": "10.0.0.20",
  "forward_port": 3000,
  "access_list_id": 3,
  "enabled": true,
  "meta": { "nginx_online": true }
}
```

- `enabled` and the presence of at least one domain determine whether the record can become a card.
- The first valid concrete `domain_names` entry becomes the automatic card name and URL host. A wildcard may appear earlier without taking precedence.
- A wildcard-only host remains visible after policy matching but is non-clickable until it has a concrete NPM name or an explicit service-metadata URL.
- Adding the real concrete hostname to the same NPM proxy host is the preferred correction because NPM then remains the browser-name source of truth. Creating a duplicate proxy host can create duplicate cards.
- `forward_scheme` becomes the automatic card URL scheme after an HTTP/HTTPS allowlist check. Exact-ID service metadata may override only presentation fields: browser target/display name in v1, plus category/order in v2.
- `forward_host` is the current access-rule destination join key.
- `forward_port` is required for exact TCP capability matching when a Tailscale Grant supplies card evidence. Legacy ACL ports remain unmodeled.
- For an explicitly configured health target, `forward_scheme`, `forward_host`, and `forward_port` also define the direct backend probe target. Presentation-metadata URLs never participate.
- `meta.nginx_online` is retained only as NPM route/configuration state in diagnostics. It is not backend application health and never creates a health label.
- `access_list_id` is parsed but not used in visibility matching.

## Optional presentation metadata

A strict versioned JSON file can override the displayed name or browser URL for an existing NPM proxy-host ID. Metadata is applied only after policy matching and cannot create a card, enable a host, change `forward_host`/`forward_port`, or grant visibility. Unknown IDs produce only a count in diagnostics.

Source priority is:

1. A concrete hostname on the existing NPM proxy host.
2. An explicit operator metadata URL/name when NPM cannot represent the desired browser target.
3. Future DNS/Tailscale observations as non-authoritative suggestions only.

Velociportal never invents a wildcard-derived URL.

## Access-list endpoint

The client still contains a `GET /api/nginx/access-lists` helper, but the runtime cache and matcher do not call or use it. NPM access lists remain an independent enforcement/configuration layer.

## Transport security

NPM authentication sends a reusable password. Plain HTTP is accepted only for the exact canonical private Docker alias or same-host/loopback compatibility routes; production uses `http://npm.velociportal.internal:81` on `velociportal-upstreams`. Use HTTPS with a valid certificate for every other location.

!!! warning "No scoped Velociportal token"
    NPM uses user credential login rather than a purpose-built read-only API key. Use a dedicated account with the least permissions that still permit proxy-host listing, protect the password, and verify its effective privileges in your NPM version.
