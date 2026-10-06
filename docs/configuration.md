[Project](../README.md) · **English** · [Русский](./configuration.ru.md)

# TrustTunnel configuration

Follow [installation steps 1–3](./installation.md) first: prepare the applications, a TLS certificate, and `deploy/.env`. Run the commands below as `root` from `/opt/tunnelapanel`.

Choose **one** method. Do not create `runtime/config` before importing an existing installation.

## A. New server

Create the configuration directory:

```sh
install -d -m 700 runtime/config
```

Create `runtime/config/vpn.toml`:

```toml
listen_address = "0.0.0.0:8443"
credentials_file = "credentials.toml"
ipv6_available = false
allow_private_network_connections = false
tls_handshake_timeout_secs = 10
client_listener_timeout_secs = 600
connection_establishment_timeout_secs = 30
tcp_connections_timeout_secs = 604800
udp_connections_timeout_secs = 300

[forward_protocol]
direct = {}

[listen_protocols.http1]
upload_buffer_size = 32768

[listen_protocols.http2]
initial_connection_window_size = 8388608
initial_stream_window_size = 4194304
max_concurrent_streams = 1000
max_frame_size = 16384
header_table_size = 65536

[metrics]
address = "127.0.0.1:1987"
per_client_metrics = true
```

This configuration enables HTTP/1 and HTTP/2, with metrics available only inside the endpoint container. The supplied Compose file publishes TCP only, so leave QUIC/HTTP/3 disabled. IPv6 forwarding is disabled in this example.

Create `runtime/config/hosts.toml`, replacing the domain and certificate paths with your own:

```toml
[[main_hosts]]
hostname = "vpn.example.com"
cert_chain_path = "/etc/letsencrypt/live/vpn.example.com/fullchain.pem"
private_key_path = "/etc/letsencrypt/live/vpn.example.com/privkey.pem"
```

Use the actual paths reported by `certbot certificates`; the certificate directory name may differ from the domain.

Create the first **VPN user** with a random password:

```sh
umask 077
printf '[[client]]\nusername = "user1"\npassword = "%s"\n' "$(openssl rand -hex 24)" > runtime/config/credentials.toml
chmod 600 runtime/config/*.toml
```

At least one enabled VPN user is required. You can obtain this user's connection configuration from the panel after startup. The panel administrator is a separate account, created automatically on first launch.

## B. Existing TrustTunnel installation

Back up the active configuration, certificates, and service definition. With Python **3.11+**, import from your installation directory:

```sh
python3 scripts/import-native.py --source /opt/trusttunnel --target runtime/config
```

The target directory `runtime/config` **must not exist**. The importer leaves the original installation untouched, preserves user credential bytes and TLS paths, changes the internal listener to `0.0.0.0:8443`, and adds local per-user metrics.

The supported layout is `vpn.toml`, `hosts.toml`, `credentials.toml`, and an optional `rules.toml`. The importer stops if:

- QUIC is enabled or a metrics section already exists.
- Credentials or rules use paths other than the standard relative filenames or their absolute paths inside the source directory.
- Certificate/key files are missing or outside `/etc/letsencrypt`.
- There are no VPN users, or the listener/file settings cannot be updated unambiguously.

These cases need manual adaptation. Preserve custom protocol options, filtering rules, and optional user connection limits. The endpoint must listen on `0.0.0.0:8443`; credentials must resolve inside `/data/config`; certificate paths must be accessible through the mounted `/etc/letsencrypt` tree. Configure metrics as in the new-server example above, without adding a duplicate section. See the [TrustTunnel 1.1.0 configuration reference](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/CONFIGURATION.md) for supported settings.

TrustTunnel 1.1.0 has quote-sensitive credential parsing. Keep existing credential bytes during migration; changing TOML quote style can change the effective password.

## Continue installation

Return to [step 4: start the containers](./installation.md#4-start-the-containers). During migration, verify an existing client's VPN connection before disabling the old service's autostart.
