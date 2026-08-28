# Alternatives

Velociportal occupies a narrow niche: a **selected-control-plane policy-derived service index** backed by Nginx Proxy Manager.

## Comparison

| Solution | Primary role | Per-user portal | Uses network policy as visibility input |
|---|---|---:|---:|
| **Velociportal** | Visibility layer | Yes | Headscale legacy ACLs or the narrow Tailscale ACL/Grant subset |
| Authentik | IdP, SSO, application portal | Yes | No |
| Authelia | Forward-auth and MFA | Limited | No |
| Keycloak | IdP and federation | No service dashboard | No |
| Homepage / Dashy / Homarr | Service dashboard | Usually shared/manual | No |
| Cloudflare Access | Managed access edge and launcher | Yes | No; uses Cloudflare policy |

## When Velociportal fits

- You run **Headscale** with legacy ACLs or accept the labeled **Tailscale SaaS** safe-Grants preview.
- NPM is your current service catalog.
- Human viewers reach the portal through a proxy that supplies trustworthy `Tailscale-User-*` headers.
- You want visibility derived from network policy without turning the dashboard into another permission database.

## When another tool fits better

- Use an **IdP** when you need login, MFA, session management, or application enforcement.
- Use a conventional **dashboard** when you want rich widgets and manually curated presentation.
- Use a managed access product when you want a SaaS edge and its own policy model.
- Do not choose Velociportal yet if you require supported Tailscale SaaS, general Grants/posture/application semantics, Caddy, Traefik, direct Authentik/Authelia headers, or a matcher already validated against your production data.

!!! important "Complement, do not replace"
    A common stack is IdP for authentication, Headscale for network policy, NPM for routing, and Velociportal for the filtered index. Each layer still enforces its own responsibility.
