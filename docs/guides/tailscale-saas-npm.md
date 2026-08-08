# Tailscale SaaS + NPM

!!! danger "Planned — not implemented"
    Velociportal currently has a Headscale client only. It has no Tailscale SaaS API client, OAuth/API-key configuration, tailnet selector, Grants implementation, or SaaS-specific tests. The `TAILSCALE_*` variables shown in older drafts do not exist.

## Intended differences

A future Tailscale SaaS adapter would need to replace the Headscale calls with Tailscale's API, implement its authentication and tailnet selection, and model policy features that differ from Headscale. NPM discovery and the visibility-only role could remain conceptually similar.

| Area | Current Headscale implementation | Future Tailscale SaaS adapter |
|---|---|---|
| API | `/api/v1/policy`, `/api/v1/node` | Tailscale API endpoints |
| Auth | Headscale Bearer API key | Tailscale-supported API/OAuth credential flow |
| Policy engine | Legacy `acls` subset | Must explicitly decide how to handle ACLs and Grants |
| Identity | `Tailscale-User-*` from a trusted Serve path | Same header family, subject to Serve limits |

## Do not deploy from this page

There is no supported Compose example for Tailscale SaaS today. Use [Headscale + NPM](headscale-npm.md), or contribute a separate adapter with fixtures and real API validation.

See [Known Limitations](../reference/known-limitations.md).
