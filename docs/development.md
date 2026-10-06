[Project](../README.md) · **English** · [Русский](./development.ru.md)

# Development

Use **Go 1.25 or newer**, as required by `go.mod`. HTML, CSS and JavaScript are embedded in the panel binary; there is no separate frontend build. Python **3.11+** is required for the configuration importer and migration smoke check. Release packaging also uses Bash and standard archive/checksum tools.

## Components

| Path | Responsibility |
| --- | --- |
| `cmd/panel`, `internal/panel` | Administrator authentication, web pages and request handlers |
| `cmd/endpoint-agent`, `internal/endpoint` | TrustTunnel process supervision, configuration changes and rollback |
| `internal/protocol` | Shared panel/agent API types |
| `deploy/` | Compose, Dockerfiles, nginx and Certbot hook |
| `scripts/` | Release packaging, configuration import and checks |

The panel calls the endpoint agent over a Unix socket in a shared volume. The agent controls its own TrustTunnel child process; neither container receives the Docker socket. Native TOML configuration and atomic JSON state persist under `runtime/`. This single-server, single-administrator design has no separate database service. See [configuration](./configuration.md) and [operations](./operations.md).

## Checks

Run from the repository root:

```sh
go test -race ./...
go vet ./...
python3 scripts/test-certbot-hook.py
```

The race detector needs a supported platform and a C toolchain. Some Go tests create temporary local Unix sockets. The Certbot hook tests use isolated command mocks and do not call Docker or system services.

## Packaging

```sh
VERSION=local GOARCH=amd64 ./scripts/package.sh
```

Use `GOARCH=arm64` for an ARM server. The archive in `dist/` contains source and precompiled Linux binaries, with a separate SHA-256 checksum. It excludes runtime credentials, `.env`, Git history and caches. Unpack it before building Docker images: the Dockerfiles copy the binaries from the archive. Follow [installation](./installation.md) for transfer and deployment; the server does not need Go or Git when using this archive.

## Migration smoke check

`scripts/smoke-test.py` is a server-side migration check, not a required fresh-install step. It needs Python 3.11+, curl, a readable original TrustTunnel installation and its binary, and the still-existing `runtime/panel/bootstrap-password`. Changing the administrator password removes that bootstrap file.

The script compares exported client configuration, checks panel pages and QR output, and exercises authenticated forwarding plus HTTP/2 TLS negotiation. HTTPS access requires `--panel-url` and a valid certificate; use `--help` for arguments. Even its default mode signs in and sends test VPN traffic.

`--exercise-users` creates, applies, disables and deletes a temporary VPN user, restarting the endpoint during the check. Use it only on a separate staging instance. This mode rejects endpoint port `443`, panel port `8080` and HTTPS URLs, but that check does not replace isolating test data and ports. If a run is interrupted, inspect the staged users/configuration before continuing.
