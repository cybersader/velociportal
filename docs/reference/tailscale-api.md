# Tailscale API

Velociportal's Tailscale SaaS adapter is OAuth-only and remains labeled **preview** until live acceptance passes.

## Fixed production origin

Production code always uses:

```text
https://api.tailscale.com/api/v2
```

There is no `TAILSCALE_API_URL`, insecure TLS mode, environment-proxy use, redirect following, or configurable test origin in production configuration. Test-only constructors can point fixtures at local servers.

## OAuth client credentials

Required environment variables:

```text
CONTROL_PLANE=tailscale
TAILSCALE_OAUTH_CLIENT_ID=...
TAILSCALE_OAUTH_CLIENT_SECRET=...
```

Velociportal requests an access token from:

```text
POST /oauth/token
```

with the OAuth client-credentials grant and exactly these scopes:

```text
policy_file:read
devices:posture_attributes:read
devices:core:read
users:read
```

No API-key fallback, pre-acquired access-token variable, explicit tailnet variable, or credential-derived API origin exists.

Access tokens:

- remain only in process memory;
- are shared across concurrent callers;
- refresh about five minutes before expiry;
- are replaced once after an API `401`; and
- disappear on restart.

Client IDs, client secrets, rejected access tokens, replacement tokens, and URL-encoded credential forms are redacted from error output. Doctor and validation do not print them.

## Runtime endpoints

Velociportal uses the OAuth credential's `-` tailnet alias:

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/tailnet/-/acl` | Read policy for the selected tailnet |
| `GET` | `/tailnet/-/users` | Resolve exact user IDs and `loginName` values |
| `GET` | `/tailnet/-/devices` | Read device IDs, owners, addresses, names, and tags |

Each request has an independent timeout. The hardened client requires normal certificate and hostname verification, TLS 1.2 or newer, bounded response headers and bodies, no redirects, and no environment proxy.

## Policy response

The policy validator reports `legacy_acl_visibility_v1` for ACL-only input and `network_access_visibility_v1` when accepted Grants participate:

- legacy `acls` `accept` rules remain supported; their destination ports and protocols are not modeled;
- safe network Grants are additive with ACLs and must carry non-empty `src`, `dst`, and `ip` arrays;
- Grant capabilities accept wildcard, port/range, protocol wildcard, and protocol port/range forms; card evidence requires TCP to the exact NPM `forward_port`;
- exact humans, defined groups, and `*` may become browser card sources;
- valid tag, IP, CIDR, host-alias, and supported autogroup sources may load but never map to a human browser identity;
- attr-only `nodeAttrs` accept only `*`, individual users, defined groups, tags, and `autogroup:member` targets with the `funnel` attribute, and never authorize cards;
- postures, IP sets, services, non-empty `via`, application capabilities, malformed capabilities, and unknown semantics fail the refresh;
- SSH is not card evidence and is reported separately; and
- empty policy is a complete zero-card snapshot.

Headscale remains legacy-ACL-only. This Tailscale subset is intentionally narrower than the full policy language, and Tailscale remains the enforcement boundary.

## User and device conversion

User `loginName` is the exact matcher-facing identity because Serve supplies `Tailscale-User-Login` in that form. Device owner references may identify a user by API ID or exact login, but the mapping must resolve uniquely.

Velociportal rejects:

- blank or duplicate user IDs;
- blank or duplicate login names;
- an ID that collides ambiguously with another user's login;
- unresolved or ambiguous device owners;
- blank or duplicate device IDs; and
- untagged devices without an owner.

Tagged devices without a human owner are retained for destination tag resolution. Addresses and tags are trimmed, deduplicated, and sorted. Unrelated user profile fields and device posture data are not retained as snapshot state; the posture-related OAuth scope permits the devices read required by the current API boundary but posture conditions are not evaluated.

## Pagination and completeness

The adapter requires complete users and devices responses. It fails the refresh when response bodies or headers indicate pagination, continuation, truncation, or a partial result. It does not publish the first page as though it were complete.

A control-plane failure or NPM failure prevents the entire new snapshot from replacing the previous one.

## Support status

Automated fixtures cover endpoint paths, OAuth token reuse and refresh, concurrent refresh coalescing, one retry after `401`, owner mapping, duplicate and partial-response rejection, transport hardening, response limits, and credential redaction.

A live tailnet has confirmed OAuth and basic policy/users/devices connectivity, but not token lifetime, revocation, complete safe-Grants behavior, two-identity owner mapping, or reachability. Complete the [Tailscale SaaS live acceptance](../guides/tailscale-saas-npm.md#live-acceptance-required) before calling the adapter supported.
