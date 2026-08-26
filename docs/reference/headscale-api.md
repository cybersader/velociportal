# Headscale API

Velociportal reads Headscale's REST gateway with a Bearer API key.

## Runtime endpoints

### `GET /api/v1/policy`

The response wraps the policy document as a JSON string. Velociportal parses strict JSON first, then a limited huJSON normalization for line comments and trailing commas.

Modeled fields:

- `groups`
- `tagOwners` (parsed, but not used to make a human identity a source tag)
- `acls`
- `hosts`

Unmodeled policy features include Grants, SSH rules, posture, protocol fields, ports, and application capabilities.

### `GET /api/v1/node`

Node owner, IP, and tag fields are used to:

- Resolve destination `tag:*` values to node IPs.
- Resolve `autogroup:self` to IPs belonging to the requesting user's nodes.

Node tags are **not** promoted into human source identities.

## Runtime transport and API key

The canonical production runtime path is direct HTTP over the private named Docker network:

```text
HEADSCALE_URL=http://headscale.velociportal.internal:8080
HEADSCALE_API_KEY=...
```

Configuration accepts Headscale HTTP only for the implementation's exact local/internal allowlist. Other hostnames and addresses require verified HTTPS. The URL is the Headscale base URL without `/api/v1`, a query, or a fragment.

The allowlist checks configuration syntax and host identity; it does not prove that the real route is private. Acceptance must confirm:

- Headscale and Velociportal are attached to `velociportal-upstreams`.
- The Headscale alias is exactly `headscale.velociportal.internal`.
- Headscale port `8080` is `None`/`Expose` only and is not LAN-published.
- Untrusted containers are not attached to the network.

Velociportal uses a dedicated credentialed HTTP client that ignores environment proxy variables, refuses redirects, requires TLS 1.2 or newer when HTTPS is used, and bounds response headers and bodies. There is no certificate-verification bypass.

Headscale v0.29.3 API keys are unscoped administrator credentials. Use a dedicated Velociportal runtime key rather than the workstation operator key.

## Pre-tailnet control and administration path

Brand-new clients need a trusted HTTPS Headscale endpoint before they can join the tailnet. In the canonical TrueNAS architecture, existing NPM terminates that HTTPS endpoint with the operator's existing automated NPM certificate lifecycle and proxies to Headscale over `velociportal-upstreams`.

`headscale-ops` remains workstation-only and HTTPS-only and uses this NPM endpoint. NPM can observe operator Bearer API keys and control traffic, so it is an explicit trust and availability boundary. Preserve WebSocket/upgrade behavior, avoid authorization-header logging, back up NPM configuration and certificates, and stop if the HTTPS certificate is not already trusted. Never disable verification.

The first Headscale API key still requires one controlled local bootstrap because remote key creation already requires a key:

```bash
/ko-app/headscale apikeys create --expiration 24h
```

Use that short-lived key through HTTPS-only `headscale-ops` to create separate operator and Velociportal runtime keys, verify both intended paths, and expire the bootstrap key promptly. Routine administration should not require TrueNAS shell access.

## Optional native HTTPS

Direct native Headscale HTTPS remains an alternative. When it uses a private root, mount only the public CA certificate through the optional Compose overlay. See [Optional native Headscale TLS](../guides/private-tls.md). The base production stack has no CA mount and no PKI service.

## Empty policy behavior

Headscale may describe its own no-policy behavior as allow-all, but Velociportal does not mirror that default. An empty parsed policy contains no supported ACL `accept` rule, so the portal renders no cards. This is intentionally fail-closed for visibility.

## Validation caveat

Endpoint shapes and transport behavior are covered by fixtures and `httptest`, not by a documented production Headscale acceptance run. Verify the exact user-name forms, node tag fields, NPM proxy behavior, and private-network confinement in the deployed version.

See [Known Limitations](known-limitations.md).
