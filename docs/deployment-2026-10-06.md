# Deployment record — 6 October 2026

Version 0.1.0 is deployed at `/opt/tunnelapanel` on `82.40.58.67` (Ubuntu 24.04,
amd64). Delivery uses a source-and-binary archive plus SHA-256; there is no Git
checkout or Go toolchain requirement on the server. `RELEASE` records the source
commit packaged with the binaries.

## Running services

| Service | Endpoint | State |
| --- | --- | --- |
| TrustTunnel 1.1.0 | `dman.pancom.pw:443`, TCP | Compose `tunnelapanel`, service `endpoint` |
| TunnelaPanel | `127.0.0.1:8080`, TCP | Compose `tunnelapanel`, service `panel` |
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

```sh
ssh -N -L 8080:127.0.0.1:8080 root@82.40.58.67
```

Open `http://127.0.0.1:8080`, username `admin`. Retrieve the initial password with:

```sh
ssh root@82.40.58.67 'cat /opt/tunnelapanel/runtime/panel/bootstrap-password'
```

Change it under Settings after signing in. No passwords, private keys or real
VPN configurations are stored in this repository. Persistent runtime state is
stored only on the server under `/opt/tunnelapanel/runtime`.

See [operations.md](operations.md) for updates, backups and rollback. Changing
users restarts TrustTunnel and interrupts current VPN connections; the panel
remains available independently through SSH.
