# TunnelaPanel

Веб-панель для управления собственным сервером [TrustTunnel](https://github.com/TrustTunnel/TrustTunnel): пользователями, подключениями и состоянием VPN. Подходит для личного сервера или небольшого круга пользователей.

Панель работает на вашем сервере в Docker, использует официальный TrustTunnel **1.1.0** и не требует отдельной базы данных. Интерфейс — на русском языке. Для подключения к VPN нужен совместимый клиент TrustTunnel; сама панель служит для администрирования.

## Возможности

- Создание пользователей, смена паролей, включение и отключение доступа, удаление учётных записей.
- Черновик изменений: можно подготовить несколько правок, затем применить их вместе или отменить.
- Выдача клиентской конфигурации TOML, ссылки подключения и QR-кода.
- Состояние TrustTunnel, версия, время работы, текущие подключения и счётчики трафика.
- Просмотр журнала, перезапуск VPN и перечитывание TLS-сертификатов.
- Вход администратора, смена его пароля и журнал административных действий.
- Доступ к панели через SSH-туннель или HTTPS на своём домене.

Сейчас поддерживаются **один сервер и один администратор**. Применение изменений пользователей перезапускает TrustTunnel и кратковременно прерывает VPN-соединения. Счётчики трафика сбрасываются при перезапуске. Готовая Docker-конфигурация публикует только TCP; QUIC/HTTP/3 в ней не включён.

## Как это устроено

| Контейнер | Назначение | Порт на сервере по умолчанию |
| --- | --- | --- |
| `panel` | Веб-интерфейс и авторизация администратора | `127.0.0.1:8080` |
| `endpoint` | TrustTunnel и процесс управления им | `443/tcp` |
| `https` — опционально | nginx с TLS для доступа к панели по домену | `8443/tcp` |

Панель общается с управляющим процессом через Unix-сокет. Docker-сокет в контейнеры не передаётся. Настройки, пользователи и состояние сохраняются в каталоге `runtime/`, сертификаты остаются под управлением Certbot на сервере.

## Что понадобится

- Linux-сервер с архитектурой `amd64` или `arm64` и доступом `root`/`sudo`. Развёртывание проверено на Ubuntu 24.04, `amd64`.
- [Docker Engine с Compose](https://docs.docker.com/engine/install/ubuntu/).
- [Go 1.25 или новее](https://go.dev/doc/install), Git и OpenSSL для установки из исходников по инструкции ниже. Go нужен только при сборке.
- Домен, направленный на сервер, и действующий TLS-сертификат для него. Готовая конфигурация использует каталог `/etc/letsencrypt`.
- Свободный TCP-порт для VPN, обычно `443`. Для публичной панели дополнительно понадобится `8443/tcp`.
- Python **3.11+**, если переносите настройки существующего TrustTunnel.

В примерах `vpn.example.com` — ваш домен, `SERVER_IP` — IP вашего сервера. Замените их своими значениями. Серверные команды ниже выполняются от `root`, из `/opt/tunnelapanel`, если не указано другое.

## Установка

### 1. Получите исходники и соберите приложения

На Linux-сервере:

```sh
git clone https://github.com/tavvet/TunnelaPanel.git /opt/tunnelapanel
cd /opt/tunnelapanel

mkdir -p dist
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" -o dist/panel ./cmd/panel
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/endpoint-agent ./cmd/endpoint-agent

install -d -m 700 runtime runtime/state runtime/panel
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
```

Dockerfiles используют готовые `dist/panel` и `dist/endpoint-agent`, поэтому этот шаг нельзя пропускать при установке из Git. Сам TrustTunnel загрузится при сборке Docker-образа; версия и SHA-256 архива закреплены в Dockerfile.

Если не хотите устанавливать Go и Git на сервер, используйте [сборку архива на другом компьютере](#установка-без-go-и-git-на-сервере).

### 2. Подготовьте сертификат

Если сертификат уже есть, используйте его. Для нового домена сначала настройте DNS, затем получите сертификат через Certbot. Пример для Ubuntu и свободного порта 80:

```sh
apt-get update
apt-get install -y certbot
certbot certonly --standalone -d vpn.example.com
```

[Режим standalone](https://eff-certbot.readthedocs.io/en/stable/using.html#standalone) требует доступного извне TCP-порта 80, в том числе при последующем продлении. Если порт занят веб-сервером, используйте подходящий для него способ проверки Certbot.

Далее понадобятся `fullchain.pem` и `privkey.pem`. Их фактические пути можно посмотреть командой `certbot certificates`: имя каталога сертификата иногда отличается от домена. В контейнер монтируется весь `/etc/letsencrypt`, чтобы работали ссылки из `live/` в `archive/`. После запуска настройте [подхват продлённого сертификата](#продление-сертификата), в том числе при доступе к панели только через SSH.

### 3. Подготовьте настройки TrustTunnel

Выберите **один** из двух вариантов.

#### Вариант А: новый сервер

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

Создайте `runtime/config/hosts.toml`, указав свой домен и реальные пути сертификата:

```toml
[[main_hosts]]
hostname = "vpn.example.com"
cert_chain_path = "/etc/letsencrypt/live/vpn.example.com/fullchain.pem"
private_key_path = "/etc/letsencrypt/live/vpn.example.com/privkey.pem"
```

Создайте первого **VPN-пользователя** со случайным паролем:

```sh
umask 077
printf '[[client]]\nusername = "user1"\npassword = "%s"\n' "$(openssl rand -hex 24)" > runtime/config/credentials.toml
chmod 600 runtime/config/*.toml
```

Его конфигурацию подключения можно будет получить в панели. Учётная запись администратора создаётся отдельно при первом запуске.

#### Вариант Б: перенос существующего TrustTunnel

Сначала сохраните резервную копию действующих настроек, сертификатов и файла сервиса. Импортируйте конфигурацию, указав каталог своей установки:

```sh
python3 scripts/import-native.py --source /opt/trusttunnel --target runtime/config
```

Каталог назначения `runtime/config` **не должен существовать**. Импорт не меняет исходную установку: сохраняет пользователей и TLS-настройки, переводит внутренний порт на `8443` и добавляет локальные метрики.

Импортёр рассчитан на `vpn.toml`, `hosts.toml`, `credentials.toml` и необязательный `rules.toml`. Он остановится, если включён QUIC, уже настроены метрики, используются нестандартные пути файлов или сертификаты вне `/etc/letsencrypt`. Такие установки требуют ручной адаптации, описанной в [справочнике TrustTunnel 1.1.0](https://github.com/TrustTunnel/TrustTunnel/blob/v1.1.0/CONFIGURATION.md).

### 4. Укажите публичный адрес VPN

Откройте `deploy/.env` и **замените** значение `TT_PUBLIC_ADDRESS` своим адресом. Для первого входа через SSH оставьте:

```ini
TT_PUBLIC_ADDRESS=vpn.example.com:443
TT_BIND_IP=0.0.0.0
TT_PORT=443
PANEL_PORT=8080
PANEL_COOKIE_SECURE=false
PANEL_ALLOWED_HOSTS=
PANEL_TRUSTED_PROXIES=
```

`TT_PORT` — внешний порт VPN. Если меняете его, укажите тот же порт в `TT_PUBLIC_ADDRESS`. Внутренний `listen_address` в `vpn.toml` остаётся `0.0.0.0:8443`.

Проверьте, что Docker-подсеть `PANEL_PROXY_SUBNET` из `.env` не пересекается с существующими сетями. При необходимости измените её и выберите `PANEL_PROXY_IP` внутри новой подсети.

### 5. Запустите контейнеры

```sh
docker compose -f deploy/compose.yml config --quiet
docker compose -f deploy/compose.yml build
```

Если переносите работающий TrustTunnel, **после сборки** остановите прежний экземпляр, чтобы освободить TCP-порт. Для типичной установки с systemd это `systemctl stop trusttunnel`; проверьте имя своего сервиса. Затем:

```sh
docker compose -f deploy/compose.yml up -d
docker compose -f deploy/compose.yml ps
curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://127.0.0.1:8080/healthz
```

Откройте выбранный TCP-порт VPN в сетевых правилах сервера/провайдера. При успешном запуске проверка панели вернёт `ok`.

После переноса проверьте подключение существующего VPN-клиента. Если оно работает, отключите автозапуск прежнего сервиса, чтобы после перезагрузки он не занял порт контейнера. Для установки с unit `trusttunnel`:

```sh
systemctl disable trusttunnel
```

Старые файлы пока сохраните для отката. Для возврата к прежнему сервису сначала остановите новые контейнеры, чтобы освободить порт.

### 6. Войдите в панель

На своём компьютере откройте SSH-туннель и оставьте его работающим:

```sh
ssh -N -L 8080:127.0.0.1:8080 root@SERVER_IP
```

Перейдите на [http://127.0.0.1:8080](http://127.0.0.1:8080). Логин — **`admin`**. Начальный пароль сгенерирован автоматически; прочитайте его на сервере:

```sh
cat /opt/tunnelapanel/runtime/panel/bootstrap-password
```

Смените пароль в разделе «Настройки». После смены файл начального пароля удаляется. Для выдачи VPN-доступа откройте «Пользователи», примените нужные изменения и получите конфигурацию, ссылку или QR-код выбранного пользователя.

## HTTPS на своём домене

Панель можно открыть на **`https://vpn.example.com:8443`**, оставив TrustTunnel на `vpn.example.com:443`. Один сертификат подходит для обоих портов. Дополнительная DNS-запись для такого варианта не нужна.

1. В `deploy/nginx.conf` замените `server_name` и оба пути сертификата на свои. Этот файл не подставляет домен из `.env` автоматически.
2. В `deploy/.env` задайте:

```ini
COMPOSE_PROFILES=https
PANEL_COOKIE_SECURE=true
PANEL_ALLOWED_HOSTS=vpn.example.com
PANEL_TRUSTED_PROXIES=172.30.80.3
PANEL_HTTPS_PORT=8443
```

В `PANEL_ALLOWED_HOSTS` указывается имя без схемы и порта. `PANEL_TRUSTED_PROXIES` должен совпадать с `PANEL_PROXY_IP`; если меняли Docker-подсеть, используйте новый адрес.

Проверьте конфигурацию и включите HTTPS:

```sh
docker compose -f deploy/compose.yml run --rm --no-deps https -t
docker compose -f deploy/compose.yml up -d --no-deps panel
docker compose -f deploy/compose.yml up -d --no-deps https
```

Разрешите входящий TCP-порт `8443` и войдите по HTTPS с прежними логином и паролем. SSH-туннель больше не нужен. После включения Secure cookies используйте HTTPS для входа; порт `8080` остаётся доступным только на loopback. Эти команды не перезапускают VPN.

## Продление сертификата

Настройте продление даже при доступе к панели только через SSH: сертификат нужен самому TrustTunnel.

В `deploy/certbot-deploy-hook.sh` замените значение по умолчанию `lineage` на путь своего сертификата, а `panel_dir` — на путь установки, если он отличается от `/opt/tunnelapanel`. Эти значения должны быть сохранены в самом файле: переменные из интерактивной SSH-сессии Certbot при автоматическом продлении не наследует.

Установите hook, предварительно сохранив копию существующего файла с таким именем, если он есть:

```sh
install -d /etc/letsencrypt/renewal-hooks/deploy
install -m 755 deploy/certbot-deploy-hook.sh /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
/etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
```

Hook перечитывает сертификат в TrustTunnel и, если он запущен, nginx. Проверьте, что автоматическое продление Certbot включено. При миграции также проверьте старые deploy hooks: они не должны запускать прежний VPN-сервис. Панель сама сертификаты не выпускает.

## Установка без Go и Git на сервере

На компьютере с Go, Git и Bash, из клонированного репозитория:

```sh
VERSION=local GOARCH=amd64 ./scripts/package.sh
scp dist/tunnelapanel-local-linux-amd64.tar.gz \
    dist/tunnelapanel-local-linux-amd64.tar.gz.sha256 root@SERVER_IP:/root/
```

Для ARM-сервера укажите `GOARCH=arm64` и замените `amd64` в именах файлов. Архив содержит исходники и **готовые Linux-бинарники**; локальные настройки и пароли в него не входят.

На сервере:

```sh
cd /root
sha256sum -c tunnelapanel-local-linux-amd64.tar.gz.sha256
install -d -m 700 /opt/tunnelapanel
tar -xzf tunnelapanel-local-linux-amd64.tar.gz -C /opt/tunnelapanel
cd /opt/tunnelapanel
install -d -m 700 runtime runtime/state runtime/panel
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
```

Продолжите с шага 2 основной инструкции. Docker всё равно нужен на сервере; компилировать приложения повторно не требуется. `package.sh` помещает бинарники внутрь архива — архив необходимо распаковать до сборки Docker-образов.

## Данные и обслуживание

| Путь | Что хранится |
| --- | --- |
| `runtime/config/` | Настройки TrustTunnel и действующие VPN-пользователи |
| `runtime/state/` | Черновик, состояние применения и резервные копии изменений |
| `runtime/panel/` | Данные авторизации администратора и журнал действий |
| `deploy/.env` | Параметры развёртывания |
| `/etc/letsencrypt/` | Сертификаты, закрытые ключи и параметры продления |

Сохраняйте резервные копии этих каталогов и своих правок `deploy/nginx.conf` и hook. `runtime/` содержит пароли VPN-пользователей и должен оставаться доступным только администратору сервера. После миграции меняйте пользователей через панель: внешнее редактирование файла пользователей блокирует применение, чтобы не затереть чужие правки.

Пересоздание контейнеров сохраняет данные в `runtime/`. Сессии администратора хранятся в памяти: после перезапуска панели понадобится войти снова. Перед обновлением сохраняйте рабочую версию, `.env` и изменённые файлы развёртывания; новый архив может заменить `nginx.conf` и hook.

Команды диагностики из каталога установки:

```sh
docker compose -f deploy/compose.yml ps
docker compose -f deploy/compose.yml logs --tail=100 panel endpoint
```

Если панель не открывается, проверьте SSH-туннель или HTTPS-настройки. Если `endpoint` перезапускается, проверьте TOML-файлы, пути сертификатов и занятость внешнего порта. Журнал самого TrustTunnel доступен в разделе «Журнал» панели.

## Разработка

Проект написан на Go; HTML, CSS и JavaScript встроены в бинарник панели. Отдельная сборка фронтенда не нужна.

| Каталог | Содержимое |
| --- | --- |
| `cmd/panel`, `internal/panel` | Веб-интерфейс, авторизация и обработчики |
| `cmd/endpoint-agent`, `internal/endpoint` | Управление процессом TrustTunnel и применение настроек |
| `internal/protocol` | Протокол общения панели с управляющим процессом |
| `deploy` | Docker Compose, Dockerfiles, nginx и Certbot hook |
| `scripts` | Упаковка релиза, импорт настроек и проверки |

Проверки для разработки:

```sh
go test -race ./...
go vet ./...
python3 scripts/test-certbot-hook.py
```

`scripts/smoke-test.py` предназначен для проверки миграции с доступной исходной установкой и ещё существующим `bootstrap-password`. Это не обязательный шаг установки на чистый сервер; режим `--exercise-users` изменяет пользователей и предназначен только для отдельного тестового экземпляра.
