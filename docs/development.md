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

The panel calls the endpoint agent over a Unix socket in a shared volume. The agent controls its own TrustTunnel child process; neither container receives the Docker socket. The agent runs as root with primary group `10001` and gives that group access to the socket only (directory `0710`, socket `0660`); the panel runs as `10001:10001`, so changing either user or group in `deploy/compose.yml` breaks the connection. The panel is not on the endpoint's Docker network: VPN client traffic leaves from the endpoint container and must not reach the panel. Native TOML configuration and atomic JSON state persist under `runtime/`. This single-server, single-administrator design has no separate database service. See [configuration](./configuration.md) and [operations](./operations.md).

## Reading and editing the Go code

Start with `cmd/panel/main.go` and `cmd/endpoint-agent/main.go` for startup and shutdown. Within each package, files separate these responsibilities:

| Files | Responsibility |
| --- | --- |
| `panel/server.go`, `panel/middleware.go`, `panel/audit.go` | HTTP routes, request protection and audit logging |
| `panel/auth.go`, `panel/auth_handlers.go` | Credentials, sessions and login/password handlers |
| `panel/pages.go`, `panel/actions.go`, `panel/templates.go` | Page data, form actions and HTML rendering |
| `panel/settings.go`, `panel/i18n.go` | Persistent panel preferences and interface localization |
| `panel/locales/*.json` | Embedded English/Russian interface text and panel messages |
| `endpoint/agent.go`, `endpoint/apply.go` | Process lifecycle and configuration transactions |
| `endpoint/store.go`, `endpoint/credentials.go` | Applied/draft state and TOML encoding |
| `endpoint/process.go`, `endpoint/logs.go` | Child process supervision and log redaction |
| `endpoint/metrics.go`, `endpoint/certificates.go` | Private metrics, certificate inspection and TLS reload |

Paths in this table are relative to `internal/`. Keep Go comments in English and explain constraints, ownership and ordering where they are not obvious. Expand multi-step functions and large literals over several lines before running `gofmt`; the formatter does not wrap long expressions automatically. Comments beside apply/recovery and authentication describe the guarantees a refactor must preserve.

## Checks

Run from the repository root:

```sh
gofmt -w cmd internal
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
