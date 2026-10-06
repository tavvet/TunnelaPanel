# Deployment record — 6 October 2026

Historical record for one deployment. For general installation and maintenance
instructions, start with the [project documentation](../../README.md).

The initial migration used version 0.1.0; version 0.2.0 now runs at
`/opt/tunnelapanel` on `82.40.58.67` (Ubuntu 24.04, amd64), with public HTTPS for
the panel. Delivery uses a source-and-binary archive plus SHA-256; there is no Git
checkout or Go toolchain requirement on the server. `RELEASE` records the source
commit packaged with the binaries.

## Running services

| Service | Endpoint | State |
| --- | --- | --- |
| TrustTunnel 1.1.0 | `dman.pancom.pw:443`, TCP | Compose `tunnelapanel`, service `endpoint` |
| TunnelaPanel | `127.0.0.1:8080`, TCP | Compose `tunnelapanel`, service `panel` |
| Panel HTTPS | `https://dman.pancom.pw:8443` | Compose `https`, nginx 1.30.5 |
| family-proxy | UDP 443 | Existing systemd service, PID 776 unchanged |
| Amnezia AWG | UDP 47222 | Existing `amnezia-awg` container, not restarted |

The native TrustTunnel service is stopped and disabled. `/opt/trusttunnel` and
its service file are retained for rollback. A root-only pre-migration backup of
the native installation, service and Let's Encrypt tree is stored at
`/root/trusttunnel-before-panel.Oyz5HK/native-and-certificates.tar.gz`.

## Verified

- 15 Go tests, including race detection; `go vet ./...`; browser JavaScript syntax.
- Staging on loopback 18443/18080 while native TCP 443 remained active.
- Administrator login, CSRF session, user list, private metrics, logs, certificates
  and generated QR image.
- Existing client TOML export exactly matches the native endpoint's export.
- Temporary user creation, apply/restart, actual proxy authentication and HTTPS
  forwarding, disable/reject, then deletion. The original user's fields remain
  identical after all operations.
- Production forwarding with the existing user's credentials after cutover.
- Public endpoint reached from outside the server: HTTP/2, valid TLS certificate,
  and HTTP 407 for unauthenticated requests.
- Certbot deploy hook forwards HUP through the supervisor; successful TLS reload
  appears in endpoint logs and a fresh TLS handshake succeeds. Current certificate
  expires 18 December 2026 at 11:12:04 UTC. No new certificate was issued.

The traffic check uses HTTP/1 proxy forwarding, with separate HTTP/2 negotiation
and public HTTP/2 request checks. QUIC is intentionally disabled for this
installation, preserving the existing UDP 443 owner.

## Access

Open [https://dman.pancom.pw:8443](https://dman.pancom.pw:8443), username `admin`.
The existing administrator password is unchanged. If it has not yet been
changed in Settings, retrieve the initial password with:

```sh
ssh root@82.40.58.67 'cat /opt/tunnelapanel/runtime/panel/bootstrap-password'
```

Change it under Settings after signing in. No passwords, private keys or real
VPN configurations are stored in this repository. Persistent runtime state is
stored only on the server under `/opt/tunnelapanel/runtime`.

See [the original runbook](operations-2026-10-06.md) for this deployment's updates, backups and rollback. Changing
users restarts TrustTunnel and interrupts current VPN connections; the panel
remains available independently over HTTPS.

## HTTPS deployment

- Release 0.2.0 packages source commit `de26d5b`.
- The existing domain, DNS record and certificate were reused. No new certificate
  was issued. Certbot's standalone HTTP-01 renewal still has port 80 available.
- nginx publishes TCP 8443 and forwards to the panel over a dedicated Docker
  network `172.30.80.0/29`. The fixed proxy address `172.30.80.3` is the panel's
  only trusted forwarding source. External client IPs are confirmed in audit.
- Browser cookies have Secure enabled. Normal administration now uses HTTPS;
  loopback HTTP 8080 remains private for health checks and recovery.
- The Certbot deploy hook validates nginx configuration and reloads both TLS
  consumers. Its manual invocation succeeded. An ACME renewal dry-run was not
  needed because the certificate challenge method was unchanged.
- Panel race tests and 8 hook regression tests passed. Actual HTTPS login,
  Secure cookies, dashboard/users/logs/certificates/QR, unchanged client export
  and authenticated VPN forwarding passed the production smoke check.
- External HTTPS and TrustTunnel connections both negotiated HTTP/2 and verified
  the certificate; a browser login at the new domain reached the dashboard.
- TrustTunnel and Amnezia container start times and family-proxy PID remained
  unchanged throughout deployment and the TLS reload. nginx runs without
  container restarts; its workers run under the image's nginx UID 101.

Pre-HTTPS configuration and renewal hook backup:
`/root/tunnelapanel-before-https.MmhuIe/config-and-hook.tar.gz`.
The previous panel image is retained as `tunnelapanel-panel:0.1.2`; the previous
0.1.2 release archive remains in `/root`. The original native migration backup
listed above is also retained.
