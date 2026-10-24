[Project](../README.md) · **English** · [Русский](./https.ru.md)

# HTTPS access

The optional `https` service exposes the panel at **`https://vpn.example.com:8443`** while TrustTunnel stays on `vpn.example.com:443`. Replace the example domain with yours. The same certificate can serve both ports; no additional DNS record is needed.

Complete [installation](./installation.md) first and use an existing valid certificate managed by Certbot. Commands below run as root from `/opt/tunnelapanel`.

## Enable the proxy

In `deploy/nginx.conf`, replace the hardcoded hostname and both certificate paths. The file does **not** substitute a domain from `.env`. Keep the rest of its configuration:

```nginx
server_name vpn.example.com;
ssl_certificate /etc/letsencrypt/live/vpn.example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/vpn.example.com/privkey.pem;
```

Use the actual lineage paths from `certbot certificates`; the directory name may differ from the domain. Only the lineage named by `TT_CERT_NAME` in `.env` is mounted, read-only: its `live/` and `archive/` directories, so that symlinks keep working after renewal. nginx uses the same lineage unless `PANEL_CERT_NAME` in `.env` names a separate one for the panel domain; the certificate paths in `nginx.conf` must belong to the mounted lineage.

Set these values in `deploy/.env`:

```ini
COMPOSE_PROFILES=https
PANEL_COOKIE_SECURE=true
PANEL_ALLOWED_HOSTS=vpn.example.com
PANEL_HTTPS_PORT=8443
PANEL_PROXY_SUBNET=172.30.80.0/29
PANEL_PROXY_IP=172.30.80.3
PANEL_TRUSTED_PROXIES=172.30.80.3
```

`PANEL_ALLOWED_HOSTS` takes a hostname without scheme or port. Check that the Docker subnet does not overlap existing Docker or VPN networks. If you change it, choose a fixed `PANEL_PROXY_IP` inside that subnet and set `PANEL_TRUSTED_PROXIES` to the same address. This allows the panel to use nginx's `X-Real-IP` for login limits and audit records; other peers cannot supply a trusted client address. Without it, every client shares nginx's address and one client's failed logins block everyone; the panel logs a warning at startup in that case.

With `PANEL_COOKIE_SECURE=true`, the panel also sends `Strict-Transport-Security` and names its cookies with the `__Host-` prefix, so browsers accept them only over HTTPS for this exact host.

Allow incoming TCP port `8443` in the server/provider network rules. Validate nginx and recreate only the panel and proxy:

```sh
docker compose -f deploy/compose.yml config --quiet
docker compose -f deploy/compose.yml run --rm --no-deps https -t
docker compose -f deploy/compose.yml up -d --no-deps panel
docker compose -f deploy/compose.yml up -d --no-deps https
curl --fail --retry 5 --retry-connrefused --retry-delay 1 https://vpn.example.com:8443/healthz
```

Sign in over HTTPS with your existing administrator credentials. These commands do not restart the VPN. After enabling Secure cookies, use HTTPS for normal login; HTTP through the SSH tunnel is no longer the supported login path.

| Service | Host port | Purpose |
| --- | --- | --- |
| `endpoint` | `443/tcp` | TrustTunnel VPN |
| `https` | `8443/tcp` | Public HTTPS panel |
| `panel` | `127.0.0.1:8080/tcp` | Loopback HTTP backend |

Keep nginx's `proxy_set_header Host $http_host`: the panel checks the full request origin, including `:8443`. Replacing it with `$host` drops the port and breaks form submissions. Keep the panel's `same-origin` referrer policy and CSRF protection. VPN settings are covered in [TrustTunnel configuration](./configuration.md).

## Certificate renewal

**Configure renewal reloads even when the panel is accessed only through SSH: TrustTunnel itself needs the certificate.** Certbot on the host handles issuance and renewal; the panel does neither.

The hook reads `TT_CERT_NAME` and `PANEL_CERT_NAME` from `deploy/.env`, so it reacts to the same lineages that Compose mounts. If the installation is not in `/opt/tunnelapanel`, save its directory in `deploy/certbot-deploy-hook.sh` itself:

```sh
panel_dir=${TT_PANEL_DIR:-/opt/tunnelapanel}
```

Interactive SSH environment variables are not a persistent configuration for automated Certbot runs. Before migration, inspect existing deploy hooks and back them up: an old hook must not start the previous VPN service after cutover. Keep hooks needed by other certificates/services.

Back up an existing target hook, then install and manually test the new one:

For an update, use the existing hook's installed filename in the commands below (for example, `trusttunnel.sh` instead of `tunnelapanel.sh`). Leaving an old hook alongside the new one makes Certbot run both. Keep backup copies outside the deploy-hook directory.

```sh
install -d /etc/letsencrypt/renewal-hooks/deploy
hook_backup=$(mktemp -d /root/tunnelapanel-hook.XXXXXX)
if [ -e /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh ]; then
  cp -p /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh "$hook_backup/"
fi
install -m 755 deploy/certbot-deploy-hook.sh /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
/etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
```

The hook filters Certbot renewals by lineage, sends HUP to the running endpoint, and validates/reloads nginx if it is running. Before migration or after rollback, it instead restarts an active native `trusttunnel` service without starting a stopped one. A failure is reported to Certbot.

Reloads run inside the containers with `exec`. Do not replace them with `docker kill` or `docker compose kill`: even a non-terminating SIGHUP [can disable automatic startup after a reboot](https://github.com/moby/moby/issues/47792) with `restart: unless-stopped`. If you installed an older hook using `compose kill`, replace the installed copy too and follow the [recovery steps](operations.md#after-a-server-reboot).

Verify the host's automatic Certbot renewal schedule, endpoint logs, and the certificate served by fresh TLS connections on both enabled ports. Preserve the existing ACME challenge method and access needed for renewal. Back up your edited nginx configuration and hook before updates; see [operations](./operations.md).
