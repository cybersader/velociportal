# Authentik Integration

Authentik remains useful for SSO, MFA, and forward-auth on the services Velociportal links to. Velociportal remains a visibility layer.

!!! warning "No direct Authentik adapter"
    The current application does not accept `X-authentik-username`, `X-authentik-email`, `X-authentik-groups`, configurable identity headers, or Authentik group synchronization. Older Compose examples using `VP_IDENTITY_HEADER` or `VP_GROUPS_HEADER` are not valid configuration.

## Supported arrangement

- Authentik protects your backend applications through your reverse proxy.
- Headscale remains the network-policy source used for portal visibility.
- Velociportal itself is reached through a trusted path that supplies the supported `Tailscale-User-*` headers.

Authentik's application portal and Velociportal can coexist: Authentik shows applications configured in Authentik, while Velociportal predicts visibility from a limited Headscale ACL model plus NPM proxy hosts.

## Future adapter requirements

A direct adapter would need an explicit, reviewed identity-mapping design. It must not assume that an Authentik group name automatically equals a Headscale group, and it must preserve the anti-spoofing boundary. Until that work exists, do not forward `X-authentik-*` headers and expect Velociportal to use them.

See [IdP integration status](index.md) and [Known Limitations](../reference/known-limitations.md).
