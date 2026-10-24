[Project](../README.md) · **English** · [Русский](operations.ru.md)

# Operations

Run server commands as `root` or through `sudo`, from `/opt/tunnelapanel`. Adjust paths and ports if your installation differs.

## Users and administration

Changes in **Users** are saved as a draft. Prepare the changes, then select **Apply** to apply them together or **Discard changes** to discard the draft. Applying changes restarts TrustTunnel, briefly disconnects VPN clients and resets traffic counters. At least one enabled user must remain. Client configurations, connection links and QR codes are available for applied, enabled users.

A manual VPN restart uses the applied settings; it does not apply the draft. TLS certificate reload is a separate operation. Configure automatic [certificate renewal and reload](https.md#certificate-renewal), including when you access the panel only through SSH.

Change the initial administrator password under **Settings → Administrator password** using **Change password**. A successful password change removes `runtime/panel/bootstrap-password` and invalidates existing sessions. Sessions are held in memory, so restarting the panel also requires signing in again. VPN user passwords and the administrator password are separate.

Sign-in allows 10 attempts per client address every 15 minutes (IPv6 addresses are grouped by /64), and at most two password checks run at once, plus one slot reserved for addresses that signed in successfully within the last 30 days (kept in memory until the panel restarts); during a flood the panel answers that the server is busy. `audit.jsonl` records refused sign-ins as `login_rate_limited` and `login_busy`, at most once per address and window or once per minute. Failed sign-ins are recorded up to 60 per minute; the rest are counted in one `login_failed_suppressed` record. The log rotates at 2 MiB and keeps five older copies, `audit.jsonl.1` to `audit.jsonl.5`.

The interface defaults to English. Choose Russian in **Settings → Interface → Language**; the saved choice applies to the whole panel, including login. Saving it does not restart TrustTunnel or change its draft. See [panel settings](panel-settings.md).

## Persistent data and backups

| Path | Contents |
| --- | --- |
| `runtime/config/` | TrustTunnel settings, TLS paths and active VPN credentials |
| `runtime/state/state.json` | Applied users and the draft, including passwords of disabled users |
| `runtime/state/backups/` | Credentials and agent state saved before each apply; the 20 most recent are kept |
| `runtime/state/apply.json` | Recovery journal during an apply; recovered automatically after an interrupted operation |
| `runtime/panel/` | Administrator authentication data, initial password until changed, `settings.json` with the interface language, and `audit.jsonl` with up to five rotated copies |
| `deploy/.env`, `deploy/nginx.conf`, `deploy/certbot-deploy-hook.sh` | Deployment settings and local customizations |
| `/etc/letsencrypt/` | Certificates, private keys, renewal settings and installed deploy hooks |

Keep these files and their backups accessible only to the server administrator. `runtime/` and apply backups contain VPN passwords. Store backups outside the release directory and do not commit them to Git.

Back up the whole `runtime/` directory while no administrative changes are being made. Keep `runtime/config/` and `runtime/state/` from the same snapshot, and include `runtime/panel/` to preserve administrator credentials and panel preferences. The automatic apply backups cover only VPN credentials and agent state; also back up the other paths in the table. Container recreation preserves the bind-mounted `runtime/` data.

## Updates

Before an update, keep the previous release archive or give the working Docker images separate tags. Save a current backup of persistent data and deployment settings. Preserve `deploy/.env` and your edits to nginx and the Certbot hook: extracting a new archive can replace `deploy/nginx.conf` and `deploy/certbot-deploy-hook.sh`. Review the installed hook under `/etc/letsencrypt/renewal-hooks/deploy/` when adopting hook changes.

Build the new binaries or unpack the release archive as described in [Installation](installation.md). Reuse your existing `.env` and `runtime/`; do not repeat the first-time configuration or import steps. Docker image builds use the binaries in `dist/`; they do not compile the Go source. For an update affecting only the panel:

```sh
docker compose -f deploy/compose.yml build panel
docker compose -f deploy/compose.yml up -d --no-deps panel
curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://127.0.0.1:8080/healthz
```

These commands recreate only the panel and leave VPN connections running. An endpoint or TrustTunnel update also requires rebuilding and recreating `endpoint`; schedule its brief VPN interruption. Review the release changes before choosing which services to update.

### Upgrading to the hardened deployment

Follow this section once if `deploy/.env` has no `TT_CERT_NAME`: the installation predates the deployment that runs the panel as the unprivileged user `10001`, mounts only one certificate lineage, and keeps the panel off the endpoint's Docker network. Before updating:

```sh
chown -R 10001:10001 runtime/panel
```

Add `TT_CERT_NAME` to `deploy/.env` with the directory name under `/etc/letsencrypt/live/` (see `certbot certificates`); Compose refuses to start without it. If the HTTPS panel uses a different certificate, also set `PANEL_CERT_NAME`. Check that every `cert_chain_path` and `private_key_path` in `runtime/config/hosts.toml` is inside `/etc/letsencrypt/live/<TT_CERT_NAME>/`: the agent refuses to start otherwise. The supplied Compose file mounts one lineage for the endpoint; add further mounts to `deploy/compose.yml` if it needs more. Reinstall the [Certbot hook](https.md#certificate-renewal): it now takes the lineage from `deploy/.env`. Then rebuild and recreate **all** services at once, because the agent creates the socket permissions the new panel needs. Schedule the brief VPN interruption:

```sh
docker compose -f deploy/compose.yml build
docker compose -f deploy/compose.yml up -d
# If the HTTPS proxy is installed:
docker compose -f deploy/compose.yml --profile https up -d https
```

Sign in again afterwards. With Secure cookies, cookie names gain the `__Host-` prefix.

## Troubleshooting

```sh
docker compose -f deploy/compose.yml ps
docker compose -f deploy/compose.yml logs --tail=100 panel endpoint
curl --fail http://127.0.0.1:8080/healthz
```

The default public HTTPS panel port is `8443`; the VPN endpoint uses public TCP `443`. The panel's HTTP port `8080` is published only on `127.0.0.1` for local checks or an SSH tunnel. A successful HTTP health check confirms the panel is responding; test public HTTPS and a VPN client separately. After enabling Secure cookies, sign in through HTTPS.

If `endpoint` exits with `cannot open … from hosts.toml`, that certificate is not in the mounted lineage: correct `TT_CERT_NAME` or the path in `hosts.toml`.

If `panel` exits with `must be owned by the panel user`, run `chown -R 10001:10001 runtime/panel` and start it again.

If `endpoint` keeps restarting, check the TOML files, certificate paths and whether another service occupies the public VPN port. The TrustTunnel process log is shown on the panel's **Logs** page. Log entries and endpoint diagnostics retain their original language regardless of the interface language. For HTTPS proxy errors, also inspect nginx:

```sh
docker compose -f deploy/compose.yml --profile https logs --tail=100 https
```

After migration, manage users through the panel. External changes to `credentials.toml` block applying edits, and a mismatch with saved state prevents agent startup. Identify the external change before choosing a recovery snapshot.

## After a server reboot

All services use `restart: unless-stopped`; Docker normally starts them again after a reboot, except containers explicitly stopped by an administrator. Check all containers, including stopped ones:

```sh
docker compose -f deploy/compose.yml --profile https ps -a
```

An older Certbot hook used `compose kill --signal SIGHUP`. Docker can mark a container as manually stopped even though that signal only reloads its certificate. This prevents the endpoint and HTTPS proxy from starting after a reboot. Replace the [installed hook](https.md#certificate-renewal), preserving your certificate lineage, then start the existing stopped containers to clear the flag:

```sh
docker compose -f deploy/compose.yml start endpoint panel
# If the HTTPS proxy is installed:
docker compose -f deploy/compose.yml --profile https start https
```

These commands preserve configuration and credentials and leave running containers running. Check public HTTPS and an authenticated VPN connection afterward. A responding panel alone does not confirm that the VPN or HTTPS proxy is running.

## Restore configuration and agent state

The agent keeps state in memory, and TrustTunnel loads credentials at startup. Restoring files requires stopping both services and interrupts VPN connections:

```sh
docker compose -f deploy/compose.yml stop panel endpoint
```

Preserve the current directories for recovery, then replace **both complete `runtime/config/` and `runtime/state/` directories from the same consistent snapshot**, retaining ownership and permissions. Do not overlay selected files: a leftover `apply.json` can overwrite the restored state on startup.

Automatic files in `runtime/state/backups/` are JSON records containing credentials and agent state, not ready-to-restore directory snapshots. There is no automatic restore helper for these records.

```sh
docker compose -f deploy/compose.yml up -d --no-deps endpoint panel
docker compose -f deploy/compose.yml ps
```

Sign in again, check the users and endpoint status in the panel, and verify an actual VPN connection before resuming administration.

See [Development](development.md) for local checks and the scope of the migration smoke test.
