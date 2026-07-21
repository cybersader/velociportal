# Nginx Proxy Manager API

Velociportal reads proxy hosts and access lists directly from your existing [Nginx Proxy Manager](https://nginxproxymanager.com/) (NPM) instance to discover services and their auth policies. This page documents only the endpoints Velociportal uses.

!!! info "Velociportal complements your IdP — it does not replace it"
    Velociportal reads NPM config to build a service catalog and surface access state. It does **not** issue identities or authenticate users. Your IdP (Authentik, Authelia, Keycloak, etc.) remains the source of truth for authentication. NPM access lists are read-only inputs here.

!!! warning "Admin credentials required"
    NPM has **no scoped or read-only API token**. Velociportal must authenticate with a full admin identity/secret. Use a dedicated admin account and keep the credentials in a secret store, not in plaintext config.

## Authentication

NPM issues a JWT (RS256) from admin credentials. All other calls send it as a `Bearer` token.

=== "curl"

    ```bash
    curl -sX POST https://npm.example.com/api/tokens \
      -H "Content-Type: application/json" \
      -d '{
        "identity": "admin@example.com",
        "secret": "your-admin-password"
      }'
    ```

=== "Response"

    ```json
    {
      "token": "eyJhbGciOi.eyJpZGVudGl0eSI6MX0.RaAb...",
      "expires": "2026-07-22T12:00:00.000Z"
    }
    ```

Export it for the examples below:

```bash
export NPM_JWT="eyJhbGciOi.eyJpZGVudGl0eSI6MX0.RaAb..."
```

!!! note "Token details"
    - Tokens are signed with **RS256** and are short-lived (typically ~1 day).
    - Refresh before expiry with `POST /api/tokens/refresh` (send the current valid token as a Bearer header) rather than re-submitting credentials each time.

    ```bash
    curl -sX GET https://npm.example.com/api/tokens/refresh \
      -H "Authorization: Bearer $NPM_JWT"
    ```

## List proxy hosts

Returns every proxy host NPM manages. Velociportal uses this to enumerate services and map each to its `domain_names` and any attached access list.

```bash
curl -sX GET https://npm.example.com/api/nginx/proxy-hosts \
  -H "Authorization: Bearer $NPM_JWT"
```

=== "Response"

    ```json
    [
      {
        "id": 12,
        "domain_names": ["headscale.example.com"],
        "forward_host": "headscale",
        "forward_port": 8080,
        "access_list_id": 3,
        "enabled": 1,
        "ssl_forced": 1
      },
      {
        "id": 13,
        "domain_names": ["grafana.example.com"],
        "forward_host": "grafana",
        "forward_port": 3000,
        "access_list_id": 0,
        "enabled": 1,
        "ssl_forced": 1
      }
    ]
    ```

An `access_list_id` of `0` means no access list is attached to that host.

!!! note "`?expand=` is unconfirmed"
    NPM accepts an `?expand=` query param on some endpoints (e.g. `?expand=access_list,owner`) to inline related objects. This is **not confirmed to work reliably** for Velociportal's needs — do not depend on it. Fetch access lists separately via the endpoints below.

## List access lists

Returns all access lists. Velociportal cross-references these against the `access_list_id` on each proxy host.

```bash
curl -sX GET https://npm.example.com/api/nginx/access-lists \
  -H "Authorization: Bearer $NPM_JWT"
```

=== "Response"

    ```json
    [
      {
        "id": 3,
        "name": "Internal Only",
        "satisfy_any": 0,
        "pass_auth": 0,
        "proxy_host_count": 4
      },
      {
        "id": 4,
        "name": "Public",
        "satisfy_any": 1,
        "pass_auth": 1,
        "proxy_host_count": 1
      }
    ]
    ```

## Get a specific access list

Fetch one access list by `id`, including its client authorization and IP rules.

```bash
curl -sX GET https://npm.example.com/api/nginx/access-lists/3 \
  -H "Authorization: Bearer $NPM_JWT"
```

=== "Response"

    ```json
    {
      "id": 3,
      "name": "Internal Only",
      "satisfy_any": 0,
      "pass_auth": 0,
      "items": [
        { "id": 9, "username": "ops" }
      ],
      "clients": [
        { "id": 15, "address": "10.0.0.0/8", "directive": "allow" },
        { "id": 16, "address": "0.0.0.0/0",  "directive": "deny" }
      ],
      "proxy_host_count": 4
    }
    ```

- `items` — HTTP basic-auth users defined on the list (usernames only; secrets are never returned).
- `clients` — IP allow/deny rules, evaluated in order.
- `satisfy_any` — `1` = any rule passing grants access; `0` = all must pass.

!!! tip "Read-only usage"
    Velociportal only issues `GET` requests against these endpoints. It never creates or modifies proxy hosts or access lists — your NPM config stays authoritative, and your IdP stays in charge of authentication.