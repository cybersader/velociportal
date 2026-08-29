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
| `GET` | `/tailnet/-/users` | Resolve exact user IDs, `loginName`, `type`, and `role`; build authoritative per-login Grant-role membership |
| `GET` | `/tailnet/-/devices` | Read device IDs, owners, addresses, names, and tags |

Each request has an independent timeout. The hardened client requires normal certificate and hostname verification, TLS 1.2 or newer, bounded response headers and bodies, no redirects, and no environment proxy.

## Policy response

The policy validator reports `legacy_acl_visibility_v1` for ACL-only input and `network_access_visibility_v1` when accepted Grants participate:

- legacy `acls` `accept` rules remain supported; their destination ports and protocols are not modeled;
- safe network Grants are additive with ACLs and must carry non-empty `src`, `dst`, and `ip` arrays;
- Grant capabilities accept wildcard, port/range, protocol wildcard, and protocol port/range forms; card evidence requires TCP to the exact NPM `forward_port`;
- exact humans, defined groups, `*`, and Users-API-authoritative human-role autogroups may become Grant browser card sources;
- valid tags, IPs, CIDRs, host aliases, `autogroup:tagged`, `autogroup:shared`, and other machine selectors may load but never map to a human browser identity;
- attr-only `nodeAttrs` accept only `*`, individual users, defined groups, tags, and `autogroup:member` targets with the `funnel` attribute, and never authorize cards;
- postures, IP sets, services, non-empty `via`, application capabilities, malformed capabilities, and unknown semantics fail the refresh;
- SSH is not card evidence and is reported separately; and
- empty policy is a complete zero-card snapshot.

Headscale remains legacy-ACL-only. This Tailscale subset is intentionally narrower than the full policy language, and Tailscale remains the enforcement boundary.

## User role and device conversion

User `loginName` is the exact matcher-facing identity because Serve supplies `Tailscale-User-Login` in that form. The complete Users API response is authoritative for Grant-role membership: a user with `type: "member"` receives `autogroup:member` plus `autogroup:<role>` for `owner`, `admin`, `member`, `it-admin`, `network-admin`, `billing-admin`, or `auditor`; the duplicate is removed for the `member` role. The Owner additionally receives `autogroup:admin`, matching the automatic memberships shown by Tailscale. A user with `type: "shared"` receives no human Grant-role selectors.

Role membership is consulted only for Grant sources. It requires exact `loginName` equality and does not case-fold or fall back to local-part, short-login, or bare-login forms. Specialized roles do not imply one another. Membership is never inferred from devices, device ownership, node tags, or `tagOwners`. Legacy ACLs and `nodeAttrs` do not consume this mapping.

Velociportal rejects:

- blank or duplicate user IDs;
- blank or duplicate login names;
- missing, null, non-string, blank, padded, or unsupported user `type` values;
- missing, null, non-string, blank, padded, or unsupported user `role` values;
- an ID that collides ambiguously with another user's login;
- unresolved or ambiguous device owners;
- blank or duplicate device IDs; and
- untagged devices without an owner.

Device owner references may identify a user by API ID or exact login, but that separate mapping must resolve uniquely and is used only to construct node ownership, including supported destination and `autogroup:self` behavior. It never creates Users API role membership. Tagged devices without a human owner are retained for destination tag resolution. Addresses and tags are trimmed, deduplicated, and sorted. Unrelated user profile fields and device posture data are not retained as snapshot state; the posture-related OAuth scope permits the devices read required by the current API boundary but posture conditions are not evaluated.

## Pagination and completeness

The adapter requires complete users and devices responses. It fails the refresh when response bodies or headers indicate pagination, continuation, truncation, or a partial result. It does not publish the first page as though it were complete.

A control-plane failure or NPM failure prevents the entire new snapshot from replacing the previous one.

## Support status

Automated fixtures cover endpoint paths, OAuth token reuse and refresh, concurrent refresh coalescing, one retry after `401`, authoritative user-role membership, separate device-owner mapping, duplicate and partial-response rejection, transport hardening, response limits, and credential redaction.

Published `v0.2.0-rc.4` is immutable at `sha256:30a7567c169836e8ae6bbf6c2280227d403b52c79a760ee57a2764d154fae02d`. Live TrueNAS use confirmed the corrected Owner-to-Admin membership, complete snapshot, Serve, portal health, and 48 Owner cards. Token lifetime, revocation, separate owner mapping, two-identity role isolation, unsupported-policy negatives, and actual reachability still require live acceptance before the adapter can be called supported.
