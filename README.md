# TunnelaPanel

Simple web administration for a single TrustTunnel endpoint: users, connection
exports, service status, logs, and applying credential changes. The first target
is an existing TrustTunnel 1.1.0 installation on Ubuntu 24.04.

The panel and endpoint supervisor run in separate Docker Compose containers.
They communicate over a Unix socket in a shared volume. The supervisor controls
only its TrustTunnel child process; neither container mounts the Docker socket.
The panel binds to server loopback and is accessed over SSH.

Panel data and staged user changes are stored as atomic JSON files on persistent
bind mounts. This single-admin MVP does not need a database service. TrustTunnel
continues to read its native TOML configuration; certificates remain managed by
the server's existing Certbot installation.

## Build a release

With Go installed, from the repository root:

```sh
./scripts/package.sh
```

This cross-compiles the two Go programs for Linux/amd64 and writes an archive and
SHA-256 checksum to `dist/`. Set `VERSION=...` to name a release or `GOARCH=arm64`
to target arm64. The archive contains source and precompiled binaries. Runtime
configuration, credentials, Git history, and build caches are excluded.

Copy the release archive to the server; Git is not required there. See the
[migration and operations runbook](docs/operations.md) before starting Compose.
An existing service already owns the production TCP port until cutover.
The first migration is complete; see the [deployment record](docs/deployment-2026-10-06.md).

## Development checks

```sh
go test -race ./...
go vet ./...
```

Tests run a fake endpoint subprocess and temporary local sockets to exercise
rollback, interrupted transactions, process recovery, password rotation and CSRF.
The server-side `scripts/smoke-test.py` additionally verifies actual TrustTunnel
authentication and forwarding; see the runbook before using its staging-only
user lifecycle option.

## Components

| Component | Purpose |
| --- | --- |
| `cmd/panel` | Administrator login and web interface |
| `cmd/endpoint-agent` | Unix-socket API and TrustTunnel process supervisor |
| `deploy/compose.yml` | Container isolation, persistent mounts, port mapping |
| `scripts/package.sh` | Source-and-binary release archive |

TrustTunnel is pinned to version 1.1.0 and its official archive SHA-256 is checked
during the image build. User changes require an endpoint restart and interrupt
current connections. Traffic counters describe the current endpoint process;
they reset when it restarts. TLS certificates can be reloaded separately.

The current deployment publishes TCP only. UDP 443 is already used by
`family-proxy`; Amnezia uses UDP 47222. Adding TrustTunnel QUIC later requires a
separate explicit port allocation.

## Configuration

Copy `deploy/env.example` to `deploy/.env` on the server. Runtime directories are
under `runtime/` beside `deploy/`; they are not part of a release archive.

| Variable | Default | Purpose |
| --- | --- | --- |
| `PANEL_PORT` | `8080` | Web panel port on server loopback |
| `PANEL_COOKIE_SECURE` | `false` | Enable only when browser access uses HTTPS |
| `PANEL_ALLOWED_HOSTS` | empty | Additional allowed hostnames for an optional reverse proxy |
| `TT_PUBLIC_ADDRESS` | `dman.pancom.pw:443` | Address included in connection exports |
| `TT_BIND_IP` | `0.0.0.0` | Host bind address for TrustTunnel TCP |
| `TT_PORT` | `443` | Host TCP port mapped to container port 8443 |
| `PANEL_IMAGE_TAG` | `local` | Local image tag for a named release |

Initial administrator credentials are generated on first start for `admin`. The password is
stored in `runtime/panel/bootstrap-password` and is never printed to container
logs. Restrict `runtime/` to the server administrator and include it in backups.

The [upstream configuration reference](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/CONFIGURATION.md)
documents supported TrustTunnel settings. Keep custom protocol and filtering
settings when importing an existing installation.
