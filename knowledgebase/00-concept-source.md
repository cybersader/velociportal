# 00 — Concept Source

> Original concept capture. This is the "why" — read before making design changes.

## The problem

If you self-host with Tailscale/Headscale, you already maintain an ACL policy that
defines **who can reach what** — groups, users, tag owners, and access rules. When
you also run a service dashboard (Homepage, Dashy, Organizr, etc.), you end up
maintaining a **second, parallel permission model**: which links show up for which
user. Two sources of truth that drift apart.

Add a service, and you touch the ACL *and* the dashboard config. Remove a user from
a group, and the dashboard still shows them links they can no longer reach. The
visibility layer is manual busywork that duplicates policy you already wrote.

## The idea

**Your network access policy *is* your dashboard policy.**

Velociportal reads the Tailscale/Headscale ACL policy and the Nginx Proxy Manager
(NPM) service registry, correlates them, and renders a per-user portal that shows
**only the services that user's ACL groups can actually reach**. No separate
visibility config to maintain — the ACL is the single source of truth.

- ACL groups/users  → who exists, what they can access
- NPM proxy hosts   → what services exist, at what domains
- Match             → which users see which service cards

## What it is NOT — it complements IdPs, doesn't replace them

Velociportal is a **visibility layer**, not an auth system. It sits *alongside*
identity providers like **Authentik, Authelia, Keycloak, or Zitadel** — it does not
compete with them.

| Layer | Question | Owner |
|-------|----------|-------|
| Authentication | Who are you? | IdP (Authentik/Authelia/…) + Tailscale identity |
| Authorization / enforcement | Are you allowed through? | IdP forward-auth + Tailscale ACL |
| **Visibility** | **What should you see on the dashboard?** | **Velociportal** |

You still run your IdP for SSO, forward-auth, and actually *blocking* unauthorized
requests. Velociportal only decides which cards render on the portal. Hiding a card
is a UX nicety, **not** a security control — enforcement always stays with the ACL
and the auth middleware.

## When it earns its place

- You already run Tailscale/Headscale **and** NPM.
- You want ACL group membership to drive dashboard visibility automatically.
- You're tired of curating dashboard links that duplicate your ACL policy.
- You want a dashboard that layers cleanly on top of an existing IdP.
- You deploy on TrueNAS Scale (primary target environment).
