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

Unmodeled policy features include Grants, SSH rules, posture, protocol fields, and application capabilities.

### `GET /api/v1/node`

Node owner, IP, and tag fields are used to:

- Resolve destination `tag:*` values to node IPs.
- Resolve `autogroup:self` to IPs belonging to the requesting user's nodes.

Node tags are **not** promoted into human source identities.

## API key

Generate a key on the Headscale host:

```bash
headscale apikeys create --expiration 90d
```

Set:

```text
HEADSCALE_URL=https://headscale.example.com
HEADSCALE_API_KEY=...
```

`HEADSCALE_URL` is the base URL, without `/api/v1`.

## Empty policy behavior

Headscale may describe its own no-policy behavior as allow-all, but Velociportal does not mirror that default. An empty parsed policy contains no supported ACL `accept` rule, so the portal renders no cards. This is intentionally fail-closed for visibility.

## Validation caveat

The endpoint shapes are covered by fixtures and `httptest`, not by a documented production Headscale validation run. In particular, verify the exact user-name forms and node tag fields returned by your Headscale version.

See [Known Limitations](known-limitations.md).
