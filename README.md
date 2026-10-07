# TunnelaPanel

**English** · [Русский](README.ru.md)

A self-hosted web panel for managing a [TrustTunnel](https://github.com/TrustTunnel/TrustTunnel) VPN server. Manage users, share connection settings, and monitor your server from a browser.

Built with Go and Docker Compose. No separate database required. The panel opens in English by default; choose Russian in **Settings → Interface → Language**.

## Features

- Create, edit, disable, and delete VPN users; review changes before applying them.
- Share client settings as TOML, connection links, or QR codes.
- View server status, connections, traffic counters, and logs.
- Restart TrustTunnel and reload TLS certificates.
- Administrator login, password management, and an audit log.
- Private access over SSH or optional HTTPS on your domain.

## Get started

You need a Linux server, Docker Compose, a domain, and a TLS certificate. Build from source on the server, or copy a release archive built on another computer.

**[Installation guide →](docs/installation.md)**

## Documentation

| Guide | What it covers |
| --- | --- |
| [Installation](docs/installation.md) | Requirements, build, startup, and first login |
| [TrustTunnel configuration](docs/configuration.md) | A new server or migration from an existing installation |
| [Panel settings](docs/panel-settings.md) | Interface language and where panel preferences are stored |
| [HTTPS and certificates](docs/https.md) | Domain access, reverse proxy, and certificate renewal |
| [Operations](docs/operations.md) | Users, backups, updates, and troubleshooting |
| [Development](docs/development.md) | Architecture, source layout, and checks |

Each guide has an English/Russian language switch at the top.

## Current scope

One TrustTunnel server and one administrator. The stack runs the panel and the TrustTunnel supervisor in separate containers, with an optional nginx container for HTTPS. The panel and supervisor communicate through a Unix socket; the Docker socket is not mounted.

The supplied deployment pins TrustTunnel **1.1.0** and publishes TCP only; QUIC/HTTP/3 is not enabled. Applying user changes briefly restarts the VPN. Traffic counters reset when TrustTunnel restarts.
