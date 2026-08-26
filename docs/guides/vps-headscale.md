# VPS Options for Headscale

Running Headscale on a VPS is optional. It separates the coordination server from NAS reboots, but it adds cost, provider dependency, and a second system to secure and back up.

**Prices checked: 2026-07-29.** Prices, taxes, IPv4 charges, stock, discounts, and contract terms change frequently. Follow the cited provider page through checkout before ordering.

!!! note "How to read these recommendations"
    “Privacy,” “reliability,” and “trust” are not objective guarantees. The notes below distinguish provider-published policy from operational facts. Cryptocurrency payment does not make an account anonymous, and a GDPR/Icelandic jurisdiction does not replace encryption, backups, or legal review.

A small Headscale deployment is usually comfortable with 1 vCPU, 1 GB RAM, and modest storage. Monitor your own node count, logs, DERP usage, and database growth rather than treating that as a fixed requirement.

## Cost-focused options

| Provider | Entry option checked | Location/payment notes | Qualification |
|---|---|---|---|
| [Hetzner Cloud](https://docs.hetzner.com/general/infrastructure-and-availability/price-adjustment/) | CX23: €5.49/month before VAT and public IPv4 | EU cloud locations include Germany and Finland; [payment methods](https://docs.hetzner.com/general/billing-and-account-management/billing-at-hetzner/payment-overview/) vary by country and do not include cryptocurrency | Strong price/spec ratio. Select an EU location deliberately and include VAT plus IPv4 in the real total. Hetzner publishes [GDPR/DPA and data-location details](https://docs.hetzner.com/general/company-and-policy/data-protection-at-hetzner/); these are provider commitments, not an uptime or privacy guarantee. |
| [netcup VPS Lite](https://www.netcup.com/de/server/vps-lite) | VPS pico G11s: €1.84/month including 19% German VAT; 1 vCPU, 1 GB RAM, 30 GB SSD | Nuremberg; official [payment methods](https://www.netcup.com/en/helpcenter/documentation/general/payment-methods) include bank transfer, PayPal, card, and SEPA | Very low advertised cost, but confirm term, availability, and country-specific VAT at checkout. netcup's [privacy policy](https://www.netcup.com/en/contact/data-privacy) describes GDPR processing and payment providers. |

## Providers with explicit cryptocurrency or privacy positioning

| Provider | Entry option checked | Payment/policy sources | Qualification |
|---|---|---|---|
| [1984 Hosting](https://1984.hosting/product/pricelist/?l=en) | VPS #1: €8.72/month; 1 CPU, 1 GB RAM, 25 GB disk | [FAQ](https://1984.hosting/faq/) lists card, PayPal, Bitcoin, and Monero; crypto is unavailable to users appearing to be in Iceland. [GDPR policy](https://1984.hosting/GDPR/) describes collected account data and says card data is handled by processors rather than stored by 1984. | Iceland location and provider-published civil-liberties positioning may be relevant to some operators. Read the actual policies; do not convert marketing posture into an anonymity claim. |
| [BuyVM](https://buyvm.net/kvm-vps/) | KVM 512 reference price: $10/month; 512 MB RAM. The official page currently reports its locations out of stock. | [Why BuyVM](https://www.buyvm.net/why-buyvm/) lists several cryptocurrencies including Bitcoin and Monero; [terms](https://buyvm.net/tos/) say crypto payments are non-refundable and services must follow location law. [Privacy policy](https://buyvm.net/privacy-policy/) contains provider claims about disclosure and data deletion. | Treat the listed price as a reference until direct stock returns; the page currently redirects prospective buyers toward a third party. The old $3.50/month recommendation is unsupported, and 512 MB may be tight once the OS and monitoring are included. |
| [Njalla](https://njal.la/pricing/) | €15/month; 1 core, 1.5 GB RAM, 15 GB disk | [FAQ](https://njal.la/faq/) lists Bitcoin and Monero among payment options; [terms](https://njal.la/tos/) describe account data and generally final payments | Njalla markets a privacy-intermediary model. That does not mean the operator “knows nothing” about every customer or that legal disclosure is impossible. Read the terms before treating it as an anonymity control. |
| [OrangeWebsite](https://orangewebsite.com/hosting/vps) | €29.90 month-to-month; €22.40/month effective on a three-year term | [Terms](https://orangewebsite.com/docs/tos.php) describe BitPay/CoinPayments, possible KYC, non-refundable VPS/crypto payments, and separate backup responsibility. [Privacy policy](https://orangewebsite.com/docs/privacy-policy.php) states the provider's Icelandic-court-order policy. | Expensive for Headscale alone. Longer-term headline pricing increases lock-in; compare the month-to-month price and backup add-ons, not only the discounted effective rate. |

## Selection checklist

Prioritize these over headline CPU counts:

1. **Location and latency.** Pick a region reasonably close to the users and devices that contact Headscale.
2. **Stable DNS and address plan.** Decide whether you need paid public IPv4 or can reliably operate dual-stack/IPv6.
3. **Backup export.** You need to move Headscale configuration, database, and key material off the VPS provider.
4. **Recovery path.** Document how to restore onto a different provider and update DNS.
5. **Provider terms.** Check suspension, refund, abuse, payment, KYC, and renewal rules.
6. **Access security.** Use SSH keys, host updates, a firewall, and a valid HTTPS configuration for Headscale.
7. **Monitoring.** Alert on Headscale API/TLS reachability and backup freshness.

## Outage expectations

A Headscale outage does not necessarily terminate established peer traffic immediately, but it prevents or delays coordination work such as joins, policy distribution, and key updates; connectivity can degrade as cached state expires. Do not describe the entire tailnet as instantly down or fully unaffected. Test the behavior of your Headscale and DERP topology.

Tailscale's general control/data-plane explanation: [What happens if the coordination server is down?](https://tailscale.com/docs/reference/coordination-server-down).

## Backups

Headscale's documented standard layout puts configuration under `/etc/headscale` and data under `/var/lib/headscale`. Back up both directories or your configured equivalents. See the [Headscale upgrade guide](https://headscale.net/stable/setup/upgrade/).

- Use an application-consistent SQLite backup or stop the service before copying live database files.
- Store a copy outside the VPS account/provider.
- Encrypt backups that contain private keys or credentials.
- Test a restore periodically.

Return to the [TrueNAS Quickstart](truenas-scale.md).
