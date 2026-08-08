# Alternatives

Velociportal occupies a narrow niche: a **Headscale-ACL-derived service index** backed by Nginx Proxy Manager.

## Comparison

| Solution | Primary role | Per-user portal | Uses Headscale ACLs as visibility input |
|---|---|---:|---:|
| **Velociportal** | Visibility layer | Yes | Yes, legacy `acls` subset |
| Authentik | IdP, SSO, application portal | Yes | No |
| Authelia | Forward-auth and MFA | Limited | No |
| Keycloak | IdP and federation | No service dashboard | No |
| Homepage / Dashy / Homarr | Service dashboard | Usually shared/manual | No |
| Cloudflare Access | Managed access edge and launcher | Yes | No; uses Cloudflare policy |

## When Velociportal fits

- You run **Headscale** and maintain legacy ACL rules.
- NPM is your current service catalog.
- Human viewers reach the portal through a proxy that supplies trustworthy `Tailscale-User-*` headers.
- You want visibility derived from network policy without turning the dashboard into another permission database.

## When another tool fits better

- Use an **IdP** when you need login, MFA, session management, or application enforcement.
- Use a conventional **dashboard** when you want rich widgets and manually curated presentation.
- Use a managed access product when you want a SaaS edge and its own policy model.
- Do not choose Velociportal yet if you require Tailscale SaaS, Grants, Caddy, Traefik, direct Authentik/Authelia headers, or a matcher already validated against your production data.

!!! important "Complement, do not replace"
    A common stack is IdP for authentication, Headscale for network policy, NPM for routing, and Velociportal for the filtered index. Each layer still enforces its own responsibility.
