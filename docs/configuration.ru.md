[Проект](../README.ru.md) · [English](./configuration.md) · **Русский**

# Настройка TrustTunnel

Сначала выполните [шаги установки 1–3](./installation.ru.md): подготовьте приложения, TLS-сертификат и `deploy/.env`. Команды ниже выполняются от `root`, из `/opt/tunnelapanel`.

Выберите **один** способ. Не создавайте `runtime/config` перед импортом существующей установки.

## А. Новый сервер

Создайте каталог конфигурации:

```sh
install -d -m 700 runtime/config
```

Создайте `runtime/config/vpn.toml`:

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

Конфигурация включает HTTP/1 и HTTP/2; метрики доступны только внутри контейнера endpoint. Готовый Compose-файл публикует только TCP, поэтому оставьте QUIC/HTTP/3 выключенным. В этом примере IPv6-маршрутизация отключена.

Создайте `runtime/config/hosts.toml`, заменив домен и пути сертификата своими:

```toml
[[main_hosts]]
hostname = "vpn.example.com"
cert_chain_path = "/etc/letsencrypt/live/vpn.example.com/fullchain.pem"
private_key_path = "/etc/letsencrypt/live/vpn.example.com/privkey.pem"
```

Используйте фактические пути из `certbot certificates`: имя каталога сертификата может отличаться от домена.

Создайте первого **VPN-пользователя** со случайным паролем:

```sh
umask 077
printf '[[client]]\nusername = "user1"\npassword = "%s"\n' "$(openssl rand -hex 24)" > runtime/config/credentials.toml
chmod 600 runtime/config/*.toml
```

Нужен хотя бы один включённый VPN-пользователь. Его конфигурацию подключения можно получить в панели после запуска. Администратор панели — отдельная учётная запись, которая создаётся автоматически при первом запуске.

## Б. Существующая установка TrustTunnel

Сохраните резервную копию действующих настроек, сертификатов и файла сервиса. Используя Python **3.11+**, импортируйте настройки из каталога своей установки:

```sh
python3 scripts/import-native.py --source /opt/trusttunnel --target runtime/config
```

Каталог назначения `runtime/config` **не должен существовать**. Импортёр не меняет исходную установку, сохраняет исходные байты файла пользователей и TLS-пути, переводит внутренний адрес на `0.0.0.0:8443` и добавляет локальные метрики пользователей.

Поддерживается структура `vpn.toml`, `hosts.toml`, `credentials.toml` и необязательный `rules.toml`. Импортёр остановится, если:

- Включён QUIC или уже есть секция метрик.
- Пути пользователей или правил отличаются от стандартных относительных имён либо их абсолютных путей внутри исходного каталога.
- Файлы сертификата/ключа отсутствуют или находятся вне `/etc/letsencrypt`.
- Нет VPN-пользователей либо настройки адреса и путей файлов невозможно заменить однозначно.

В этих случаях нужна ручная адаптация. Сохраните нестандартные параметры протоколов, правила фильтрации и дополнительные ограничения соединений пользователей. Endpoint должен слушать `0.0.0.0:8443`; файл пользователей должен находиться внутри `/data/config`; сертификаты должны быть доступны через подключённый каталог `/etc/letsencrypt`. Настройте метрики как в примере нового сервера выше, не создавая дублирующую секцию. Поддерживаемые параметры описаны в [справочнике TrustTunnel 1.1.0](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/CONFIGURATION.md).

Парсер пользователей TrustTunnel 1.1.0 чувствителен к виду кавычек. Сохраняйте исходные байты файла при миграции: замена типа TOML-кавычек может изменить фактический пароль.

## Продолжение установки

Вернитесь к [шагу 4: запуск контейнеров](./installation.ru.md#4-запустите-контейнеры). При миграции проверьте VPN-соединение существующего клиента, прежде чем отключать автозапуск прежнего сервиса.
