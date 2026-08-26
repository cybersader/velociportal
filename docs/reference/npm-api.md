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

- `enabled` and the presence of a domain determine whether the record is considered.
- The first `domain_names` entry becomes the card name and URL host.
- `forward_scheme` becomes the card URL scheme after an HTTP/HTTPS allowlist check.
- `forward_host` is the current ACL join key.
- `meta.nginx_online` drives the status indicator.
- `forward_port` is parsed but not used in visibility matching.
- `access_list_id` is parsed but not used in visibility matching.

## Access-list endpoint

The client still contains a `GET /api/nginx/access-lists` helper, but the runtime cache and matcher do not call or use it. NPM access lists remain an independent enforcement/configuration layer.

## Transport security

NPM authentication sends a reusable password. Plain HTTP is accepted only for the exact canonical internal alias or same-host/loopback compatibility routes; production uses `http://npm.velociportal.internal:81` on `velociportal-upstreams`. Use HTTPS with a valid certificate for every other location.

!!! warning "No scoped Velociportal token"
    NPM uses user credential login rather than a purpose-built read-only API key. Use a dedicated account with the least permissions that still permit proxy-host listing, protect the password, and verify its effective privileges in your NPM version.
