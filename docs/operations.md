# Migration and operations

This runbook describes the initial installation on `root@82.40.58.67` and later
release updates. Production TrustTunnel is currently native under
`/opt/trusttunnel`; the new stack lives under `/opt/tunnelapanel`.

## Existing services and constraints

| Service | Port | Migration action |
| --- | --- | --- |
| Native TrustTunnel 1.1.0 | TCP 443 | Preserve settings, then replace with the endpoint container |
| family-proxy | UDP 443 | Leave running and unchanged |
| Amnezia | UDP 47222 | Leave running and unchanged |
| TunnelaPanel | Loopback TCP 8080 | Access through SSH |

TrustTunnel uses HTTP/1 and HTTP/2. Do not enable QUIC or publish UDP 443 in this
deployment. Container port 8443 avoids privileged-port capabilities. Both
containers run as UID 0 with all Linux capabilities dropped, no-new-privileges,
and a read-only root filesystem. They share a mode-0600 control socket. Only the
endpoint has access to endpoint configuration and certificates.

## Prepare a release

Build locally; the server does not need a Go toolchain or a Git checkout:

```sh
VERSION=initial ./scripts/package.sh
scp dist/tunnelapanel-initial-linux-amd64.tar.gz \
    dist/tunnelapanel-initial-linux-amd64.tar.gz.sha256 root@82.40.58.67:/root/
```

On the server, check the checksum, create the installation directory, and unpack:

```sh
cd /root
sha256sum -c tunnelapanel-initial-linux-amd64.tar.gz.sha256
install -d -m 700 /opt/tunnelapanel
tar -xzf tunnelapanel-initial-linux-amd64.tar.gz -C /opt/tunnelapanel
cd /opt/tunnelapanel
cp deploy/env.example deploy/.env
install -d -m 700 runtime runtime/state runtime/panel
docker compose -f deploy/compose.yml build
```

The endpoint image downloads the official pinned TrustTunnel archive and checks
its SHA-256. Image building can happen while the native service remains active.
Keep the old release archive and Docker images until the new release is verified.

## Back up the native installation

Before changing configuration or stopping the native service, record:

- `systemctl cat trusttunnel` and the enabled/running state of that unit;
- configuration file names and the service working directory;
- listening TCP/UDP ports and the running Docker container list;
- the certificate paths, Certbot timer, and the existing renewal deploy hook;
- ownership and permissions of the existing configuration.

Make a root-only timestamped backup of `/opt/trusttunnel`, the TrustTunnel systemd
unit and its drop-ins, and the existing Certbot TrustTunnel hook. Preserve the
entire Let's Encrypt tree in the server backup system, including account data and
the `live/` symlinks. Do not copy production passwords or private keys into the
Git repository or release archives.

Do not assume that the certificate paths point directly to regular files:
`/etc/letsencrypt/live/dman.pancom.pw/` normally links into `archive/`. Compose
mounts the entire `/etc/letsencrypt` tree read-only to keep renewal working.

## Import configuration

For this installation, the checked importer creates a new configuration directory
and refuses to overwrite one that already exists:

```sh
python3 scripts/import-native.py --source /opt/trusttunnel --target runtime/config
```

It preserves original credential bytes and rejects custom paths, an existing
metrics section, or QUIC settings that need separate review.

Copy the native TOML files to `runtime/config/`, retaining every custom option and
all users. Adapt these values in the copied configuration only:

- the endpoint listens on `0.0.0.0:8443` inside its container;
- credentials and rules paths resolve under `/data/config`, or use paths relative
  to that working directory;
- certificate and key paths continue to resolve under `/etc/letsencrypt`;
- metrics listen on `127.0.0.1:1987` inside the endpoint container, with
  `per_client_metrics = true` for the users/status view;
- keep the original HTTP/1 and HTTP/2 settings and leave QUIC disabled.

Preserve optional credential fields `max_http2_conns` and `max_http3_conns`. The
upstream 1.1.0 credential parser has unusual TOML string handling: changing quote
style can change effective passwords. Keep imported credential text/values
compatible and check exports against a known existing account.

The endpoint has no dedicated config-check command. Client export checks some
parsing and file access, but does not fully validate listener startup. Verify a
staged endpoint before replacing the production service.

## Stage and verify

Start a separate Compose project on a loopback test port, using the imported
configuration. The same Compose file can be used as follows while production
continues to serve TCP 443:

```sh
TT_BIND_IP=127.0.0.1 TT_PORT=18443 PANEL_PORT=18080 \
  docker compose -p tunnelapanel-stage -f deploy/compose.yml up -d
```

Use an SSH forward to reach the staged panel and tunnel endpoint:

```sh
ssh -L 18080:127.0.0.1:18080 -L 18443:127.0.0.1:18443 root@82.40.58.67
```

Check panel login, user import, certificates, endpoint logs, the metrics health
endpoint, and HTTP/2 TLS negotiation. For a client traffic test, use a temporary
client config that connects to `127.0.0.1:18443` through the SSH forward while
retaining the original TLS hostname. Check a real tunneled HTTP request; an open
TCP port or a successful TLS handshake alone does not prove VPN authentication
and forwarding work.

The stage project uses the same runtime bind directories, so stop it before
starting production and do not run both panels concurrently against the same
state files. Staged administrative user edits change the copied container
configuration; they do not change the native installation.

```sh
docker compose -p tunnelapanel-stage -f deploy/compose.yml down
```

Keep TCP 443 and UDP 443 ownership checks separate. They belong to different
services on this host.

## Cut over

After the staged connection check passes:

1. Stop the native `trusttunnel` systemd service.
2. Start production with `docker compose -f deploy/compose.yml up -d`.
3. Check container health, logs, the panel, and a real connection using an existing
   account and the existing public address.
4. Verify `family-proxy` and Amnezia are still running on their original UDP ports.
5. Disable the native TrustTunnel service so that it cannot take TCP 443 on reboot.
6. Update the Certbot TrustTunnel deploy hook as described below.

Existing client credentials, TLS hostname, public address and TCP port are
preserved. Clients will reconnect during the brief service interruption.

## Panel access

```sh
ssh -N -L 8080:127.0.0.1:8080 root@82.40.58.67
```

Open `http://127.0.0.1:8080` in the browser and sign in as `admin`. Retrieve the initial password directly
from `/opt/tunnelapanel/runtime/panel/bootstrap-password` over the administrator's
SSH session. Do not paste that file into logs, issue reports, or repository files.
Change the administrator password in the panel after the initial sign-in; doing
so removes the bootstrap-password file.

The cookie Secure attribute defaults to false for this loopback HTTP access. If
an HTTPS reverse proxy is added later, set `PANEL_COOKIE_SECURE=true` and restart
the panel. Add the proxy's public hostname to `PANEL_ALLOWED_HOSTS`; it accepts a
comma-separated list without scheme or port. Keep the panel loopback binding
unless a separate access design is implemented.

## Certificate renewal

Keep the existing Certbot certificate, timer and challenge method. The supplied
`deploy/certbot-deploy-hook.sh` checks the currently active backend: if the
production Compose endpoint is running, it sends HUP to the supervisor, which
forwards it to TrustTunnel for TLS-only reload. Otherwise it invokes
`systemctl try-restart trusttunnel`, which restarts an active native service
without starting an intentionally stopped one. Docker inspection errors make the
hook fail visibly.

The hook ignores renewals for other lineages. Its default lineage is
`/etc/letsencrypt/live/dman.pancom.pw`; edit the installed default if the certificate
name changes. `TT_CERT_LINEAGE` and `TT_PANEL_DIR` can override the defaults when
the hook is invoked manually.

Back up and install the hook after production cutover:

```sh
cd /opt/tunnelapanel
hook_backup=$(mktemp -d /root/trusttunnel-hook.XXXXXX)
cp -p /etc/letsencrypt/renewal-hooks/deploy/trusttunnel.sh "$hook_backup/"
install -m 755 deploy/certbot-deploy-hook.sh \
  /etc/letsencrypt/renewal-hooks/deploy/trusttunnel.sh
RENEWED_LINEAGE=/etc/letsencrypt/live/dman.pancom.pw \
  /etc/letsencrypt/renewal-hooks/deploy/trusttunnel.sh
```

Inspect endpoint logs and the certificate served by a fresh TLS connection after
the manual hook invocation. User changes still require an endpoint restart; HUP
reloads only TLS settings. A renewal dry-run may exercise ACME challenges;
preserve the existing challenge configuration and other port owners.

## Roll back the migration

Stop the new stack first so TCP 443 becomes free:

```sh
cd /opt/tunnelapanel
docker compose -f deploy/compose.yml down
systemctl enable --now trusttunnel
```

The supplied Certbot hook automatically falls back to the active native service;
the original hook can also be restored from its backup. Confirm the native
endpoint works with an existing client. Leave new runtime data in place for diagnosis.
If users were changed in the panel after migration, those changes are in the new
configuration only; reconcile them deliberately before rolling back. The native
directory is intentionally preserved during migration.

## Later updates and backups

Back up `runtime/config`, `runtime/state`, `runtime/panel`, `deploy/.env` and the
certificate tree. These files contain credentials. For a consistent snapshot,
stop the panel while copying its state and avoid concurrent apply operations.

Before unpacking an update, retain the previous release archive and image tag.
Unpack the new source and binaries into `/opt/tunnelapanel`; `runtime/` and `.env`
are absent from the package, so they remain in place. Build images, then recreate
services with Compose. Check the migration notes before any data format change.

Useful commands from `/opt/tunnelapanel`:

```sh
docker compose -f deploy/compose.yml ps
docker compose -f deploy/compose.yml logs --tail=100 endpoint
docker compose -f deploy/compose.yml logs --tail=100 panel
```

Traffic counters currently reset when the endpoint restarts. The `/clients`
metrics response contains one aggregate IP per user, not a complete connection
audit or a durable billing record. Preserve this distinction in operational use.

## Upstream references

- [Configuration and TLS reload](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/CONFIGURATION.md)
- [Client export CLI](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/endpoint/src/main.rs)
- [Credential parser](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/lib/src/settings.rs)
- [Per-user metrics](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/lib/src/metrics.rs)
- [Release verification](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/VERIFY_RELEASES.md)
