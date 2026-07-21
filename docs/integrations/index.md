# IdP Integrations

Velociportal is a **visibility layer** for your self-hosted stack — it surfaces which services exist, who can reach them, and how they connect. It does **not** issue identities or terminate auth.

!!! important "Velociportal complements your IdP — it does not replace it"
    Run Velociportal with or without an identity provider. On its own it gives you visibility. Paired with an IdP, it inherits **SSO, MFA, and access enforcement** so what you *see* matches what you're *allowed* to reach.

## Integration modes

=== "Authentik"

    Full IdP. Velociportal reads groups and sits behind Authentik forward-auth.

    ```yaml
    # forward-auth to authentik.example.com
    proxy_provider: headscale.example.com
    groups_sync: true
    ```

    Best when you already run Authentik for SSO + MFA.

=== "Authelia"

    Lightweight auth middleware — MFA and access rules without a full IdP.

    ```yaml
    # Authelia guards npm.example.com
    auth_backend: authelia.example.com
    access_control: bypass|one_factor|two_factor
    ```

=== "No IdP"

    Simplest path: trust **Tailscale identity headers** only.

    ```
    Tailscale-User-Login: alice@example.com
    Tailscale-User-Name: Alice
    ```

    No SSO or MFA — least features, zero extra services.

!!! tip
    Start with **No IdP** to explore, then add Authelia or Authentik when you need enforcement.