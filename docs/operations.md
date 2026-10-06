[Project](../README.md) · **English** · [Русский](operations.ru.md)

# Operations

Run server commands as `root` or through `sudo`, from `/opt/tunnelapanel`. Adjust paths and ports if your installation differs.

## Users and administration

Changes in **Users (Пользователи)** are saved as a draft. Prepare the changes, then apply them together or discard the draft. Applying changes restarts TrustTunnel, briefly disconnects VPN clients and resets traffic counters. At least one enabled user must remain. Client configurations, connection links and QR codes are available for applied, enabled users.

A manual VPN restart uses the applied settings; it does not apply the draft. TLS certificate reload is a separate operation. Configure automatic [certificate renewal and reload](https.md#certificate-renewal), including when you access the panel only through SSH.

Change the initial administrator password in **Settings (Настройки)**. A successful password change removes `runtime/panel/bootstrap-password` and invalidates existing sessions. Sessions are held in memory, so restarting the panel also requires signing in again. VPN user passwords and the administrator password are separate.

## Persistent data and backups

| Path | Contents |
| --- | --- |
| `runtime/config/` | TrustTunnel settings, TLS paths and active VPN credentials |
| `runtime/state/state.json` | Applied users and the draft, including passwords of disabled users |
| `runtime/state/backups/` | Credentials and agent state saved before each apply |
| `runtime/state/apply.json` | Recovery journal during an apply; recovered automatically after an interrupted operation |
| `runtime/panel/` | Administrator authentication data, initial password until changed, and `audit.jsonl` |
| `deploy/.env`, `deploy/nginx.conf`, `deploy/certbot-deploy-hook.sh` | Deployment settings and local customizations |
| `/etc/letsencrypt/` | Certificates, private keys, renewal settings and installed deploy hooks |

Keep these files and their backups accessible only to the server administrator. `runtime/` and apply backups contain VPN passwords. Store backups outside the release directory and do not commit them to Git.

Back up `runtime/config/` and `runtime/state/` together while no changes are being applied. The automatic apply backups cover credentials and agent state; also back up the other paths in the table. Container recreation preserves the bind-mounted `runtime/` data.

## Updates

Before an update, keep the previous release archive or give the working Docker images separate tags. Save a current backup of persistent data and deployment settings. Preserve `deploy/.env` and your edits to nginx and the Certbot hook: extracting a new archive can replace `deploy/nginx.conf` and `deploy/certbot-deploy-hook.sh`. Review the installed hook under `/etc/letsencrypt/renewal-hooks/deploy/` when adopting hook changes.

Build the new binaries or unpack the release archive as described in [Installation](installation.md). Reuse your existing `.env` and `runtime/`; do not repeat the first-time configuration or import steps. Docker image builds use the binaries in `dist/`; they do not compile the Go source. For an update affecting only the panel:

```sh
docker compose -f deploy/compose.yml build panel
docker compose -f deploy/compose.yml up -d --no-deps panel
curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://127.0.0.1:8080/healthz
```

These commands recreate only the panel and leave VPN connections running. An endpoint or TrustTunnel update also requires rebuilding and recreating `endpoint`; schedule its brief VPN interruption. Review the release changes before choosing which services to update.

## Troubleshooting

```sh
docker compose -f deploy/compose.yml ps
docker compose -f deploy/compose.yml logs --tail=100 panel endpoint
curl --fail http://127.0.0.1:8080/healthz
```

The default public HTTPS panel port is `8443`; the VPN endpoint uses public TCP `443`. The panel's HTTP port `8080` is published only on `127.0.0.1` for local checks or an SSH tunnel. A successful HTTP health check confirms the panel is responding; test public HTTPS and a VPN client separately. After enabling Secure cookies, sign in through HTTPS.

If `endpoint` keeps restarting, check the TOML files, certificate paths and whether another service occupies the public VPN port. The TrustTunnel process log is shown in the panel's **Log (Журнал)** page. For HTTPS proxy errors, also inspect nginx:

```sh
docker compose -f deploy/compose.yml --profile https logs --tail=100 https
```

After migration, manage users through the panel. External changes to `credentials.toml` block applying edits, and a mismatch with saved state prevents agent startup. Preserve the current files, identify the external change and restore a matching configuration/state pair from backup before resuming.

See [Development](development.md) for local checks and the scope of the migration smoke test.
