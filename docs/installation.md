[Project](../README.md) · **English** · [Русский](./installation.ru.md)

# Installation

## Prerequisites

- A Linux server (`amd64` or `arm64`) with root access. Deployment has been tested on Ubuntu 24.04, `amd64`.
- [Docker Engine with Compose](https://docs.docker.com/engine/install/ubuntu/) on the server.
- [Go 1.25 or newer](https://go.dev/doc/install) and Git on the build machine. Building a release archive also requires Bash.
- A domain pointing to the server and a valid TLS certificate. The supplied deployment mounts `/etc/letsencrypt`; step 2 covers obtaining a certificate.
- An available TCP port for the VPN, normally `443`. Public HTTPS access to the panel additionally uses `8443/tcp`.
- OpenSSL on the server for a new VPN configuration, or Python **3.11+** to import an existing installation. The checks below use `curl`.

Replace `vpn.example.com` with your domain and `SERVER_IP` with your server's IP address. Unless stated otherwise, run server commands as `root` from `/opt/tunnelapanel`.

## 1. Build the applications

Choose **one** build method. Both produce the Linux binaries required by the Dockerfiles.

### A. Build on the server

Run on the Linux server:

```sh
git clone https://github.com/tavvet/TunnelaPanel.git /opt/tunnelapanel
cd /opt/tunnelapanel

mkdir -p dist
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" -o dist/panel ./cmd/panel
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/endpoint-agent ./cmd/endpoint-agent
```

### B. Build an archive on another computer

This method does not require Go or Git on the server. On your build machine:

```sh
git clone https://github.com/tavvet/TunnelaPanel.git
cd TunnelaPanel
VERSION=local GOARCH=amd64 ./scripts/package.sh
scp dist/tunnelapanel-local-linux-amd64.tar.gz \
    dist/tunnelapanel-local-linux-amd64.tar.gz.sha256 root@SERVER_IP:/root/
```

For an ARM64 server, use `GOARCH=arm64` and replace `amd64` in the filenames. The archive contains source and compiled Linux binaries; runtime settings and credentials are excluded.

On the server, verify and extract it:

```sh
cd /root
sha256sum -c tunnelapanel-local-linux-amd64.tar.gz.sha256
install -d -m 700 /opt/tunnelapanel
tar -xzf tunnelapanel-local-linux-amd64.tar.gz -C /opt/tunnelapanel
cd /opt/tunnelapanel
```

`package.sh` places the binaries **inside the archive**. Extract it before building the Docker images; running the packaging script alone does not create `dist/panel` and `dist/endpoint-agent` in your checkout.

### Continue with either method

On the server:

```sh
install -d -m 700 runtime runtime/state runtime/panel
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
```

Leave `runtime/config` absent until you choose a configuration method in step 3. The TrustTunnel binary is downloaded during the Docker image build; its version and archive SHA-256 are pinned in [the Dockerfile](../deploy/endpoint.Dockerfile).

## 2. Prepare the TLS certificate

Reuse an existing certificate if you have one. Otherwise, configure DNS first, then obtain a certificate with Certbot. For Ubuntu with port 80 available:

```sh
apt-get update
apt-get install -y certbot
certbot certonly --standalone -d vpn.example.com
```

[Standalone validation](https://eff-certbot.readthedocs.io/en/stable/using.html#standalone) requires TCP port 80 to be reachable from the Internet, including during renewal. If a web server already owns this port, use the Certbot validation method appropriate for that server.

Find the actual certificate paths:

```sh
certbot certificates
```

You will need `fullchain.pem` and `privkey.pem`. The certificate directory name may differ from your domain. Compose mounts the entire `/etc/letsencrypt` tree so that `live/` symlinks into `archive/` keep working.

## 3. Set deployment options and configure TrustTunnel

Edit `deploy/.env` and **replace** `TT_PUBLIC_ADDRESS` with your public VPN address. For initial access through an SSH tunnel, use:

```ini
TT_PUBLIC_ADDRESS=vpn.example.com:443
TT_BIND_IP=0.0.0.0
TT_PORT=443
PANEL_PORT=8080
PANEL_COOKIE_SECURE=false
PANEL_ALLOWED_HOSTS=
PANEL_TRUSTED_PROXIES=
```

`TT_PORT` is the external VPN port. If you change it, use the same port in `TT_PUBLIC_ADDRESS`. The internal listener in `vpn.toml` remains `0.0.0.0:8443`.

Check that `PANEL_PROXY_SUBNET` in `.env` does not overlap your existing Docker or VPN networks. If necessary, change the subnet and choose `PANEL_PROXY_IP` inside it. Leave the optional HTTPS profile disabled for this initial setup.

Now follow [TrustTunnel configuration](./configuration.md) to **create a new configuration or import your existing installation**, then return to step 4. The endpoint needs valid configuration and at least one VPN user before it can start.

## 4. Start the containers

Validate the Compose file and build the images:

```sh
docker compose -f deploy/compose.yml config --quiet
docker compose -f deploy/compose.yml build
```

If you are migrating a running TrustTunnel, stop the old instance **after building the images** to release its TCP port. For a native installation with the systemd unit named `trusttunnel`:

```sh
systemctl stop trusttunnel
```

Check your actual service name. Skip that command for a new server. Start the containers:

```sh
docker compose -f deploy/compose.yml up -d
docker compose -f deploy/compose.yml ps
curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://127.0.0.1:8080/healthz
```

Allow the selected VPN TCP port in the server/provider network rules. The panel check should return `ok`; it does not verify a VPN connection. On first startup, the panel can respond before the endpoint finishes its readiness check. Allow a few seconds, then refresh the overview and confirm that TrustTunnel is running before managing users.

After a migration, test an existing VPN client. Once it connects successfully, disable the old service's autostart so it cannot take the container's port after a reboot. For the `trusttunnel` unit:

```sh
systemctl disable trusttunnel
```

Keep the old files for rollback. To return to the native service, first stop the new containers to release the port. See [operations](./operations.md) for backups and maintenance.

## 5. Sign in

On your own computer, open an SSH tunnel and leave it running:

```sh
ssh -N -L 8080:127.0.0.1:8080 root@SERVER_IP
```

Open [http://127.0.0.1:8080](http://127.0.0.1:8080). The username is **`admin`**. Read the generated initial password on the server:

```sh
cat /opt/tunnelapanel/runtime/panel/bootstrap-password
```

Change it under **Settings → Administrator password** using **Change password**. This removes the initial-password file. To issue VPN access, open **Users**, prepare your changes, select **Apply**, and get a user's configuration, connection link, or QR code. Applying user changes restarts TrustTunnel and interrupts current connections.

The interface starts in English. To use Russian, choose it in **Settings → Interface → Language** and save. This also changes the login page language without restarting the VPN. See [panel settings](./panel-settings.md).

For public access to the panel, continue with [HTTPS setup](./https.md).

**Complete [certificate renewal setup](./https.md#certificate-renewal), even if you only access the panel through SSH. TrustTunnel itself needs the renewed certificate to be reloaded.**
