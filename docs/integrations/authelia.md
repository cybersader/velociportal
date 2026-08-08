# Authelia Integration

Authelia can enforce login, MFA, and per-domain policy at your reverse proxy. Velociportal can provide a separate visibility-only dashboard.

!!! warning "No direct Authelia adapter"
    The current application does not accept `Remote-User`, `Remote-Groups`, or a `TRUST_FORWARD_HEADERS` switch. Older examples showing those variables are not valid configuration.

## Supported arrangement

- Authelia protects backend services and, if desired, the portal URL.
- Headscale ACLs remain the network-policy input used by Velociportal.
- The final request into Velociportal must still carry supported `Tailscale-User-*` headers from a trusted identity path.

Authelia rules and Headscale ACLs remain separate enforcement systems. Similar group names do not create automatic synchronization, and Velociportal does not write either system.

See [IdP integration status](index.md) and [Known Limitations](../reference/known-limitations.md).
