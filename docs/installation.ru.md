[Проект](../README.ru.md) · [English](./installation.md) · **Русский**

# Установка

## Что понадобится

- Linux-сервер (`amd64` или `arm64`) с доступом root. Развёртывание проверено на Ubuntu 24.04, `amd64`.
- [Docker Engine с Compose](https://docs.docker.com/engine/install/ubuntu/) на сервере.
- [Go 1.25 или новее](https://go.dev/doc/install) и Git на машине для сборки. Для сборки архива также нужен Bash.
- Домен, направленный на сервер, и действующий TLS-сертификат. Готовая конфигурация монтирует `/etc/letsencrypt`; получение сертификата описано в шаге 2.
- Свободный TCP-порт для VPN, обычно `443`. Для публичной панели дополнительно нужен `8443/tcp`.
- OpenSSL на сервере для новой конфигурации VPN или Python **3.11+** для переноса существующей установки. В проверках ниже используется `curl`.

Замените `vpn.example.com` своим доменом, а `SERVER_IP` — IP вашего сервера. Если не указано другое, выполняйте серверные команды от `root`, из `/opt/tunnelapanel`.

## 1. Соберите приложения

Выберите **один** способ сборки. Оба подготавливают Linux-бинарники, необходимые Dockerfiles.

### А. Сборка на сервере

На Linux-сервере:

```sh
git clone https://github.com/tavvet/TunnelaPanel.git /opt/tunnelapanel
cd /opt/tunnelapanel

mkdir -p dist
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" -o dist/panel ./cmd/panel
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o dist/endpoint-agent ./cmd/endpoint-agent
```

### Б. Сборка архива на другом компьютере

Этот способ не требует Go и Git на сервере. На компьютере для сборки:

```sh
git clone https://github.com/tavvet/TunnelaPanel.git
cd TunnelaPanel
VERSION=local GOARCH=amd64 ./scripts/package.sh
scp dist/tunnelapanel-local-linux-amd64.tar.gz \
    dist/tunnelapanel-local-linux-amd64.tar.gz.sha256 root@SERVER_IP:/root/
```

Для ARM64-сервера укажите `GOARCH=arm64` и замените `amd64` в именах файлов. Архив содержит исходники и готовые Linux-бинарники; рабочие настройки и пароли в него не входят.

На сервере проверьте и распакуйте архив:

```sh
cd /root
sha256sum -c tunnelapanel-local-linux-amd64.tar.gz.sha256
install -d -m 700 /opt/tunnelapanel
tar -xzf tunnelapanel-local-linux-amd64.tar.gz -C /opt/tunnelapanel
cd /opt/tunnelapanel
```

`package.sh` помещает бинарники **внутрь архива**. Распакуйте его до сборки Docker-образов: сам запуск скрипта упаковки не создаёт `dist/panel` и `dist/endpoint-agent` в клонированном репозитории.

### Продолжение для обоих способов

На сервере:

```sh
install -d -m 700 runtime runtime/state runtime/panel
cp deploy/env.example deploy/.env
chmod 600 deploy/.env
```

Не создавайте `runtime/config` до выбора способа настройки в шаге 3. Сам TrustTunnel загрузится при сборке Docker-образа; версия и SHA-256 архива закреплены в [Dockerfile](../deploy/endpoint.Dockerfile).

## 2. Подготовьте TLS-сертификат

Если сертификат уже есть, используйте его. Иначе сначала настройте DNS, затем получите сертификат через Certbot. Пример для Ubuntu и свободного порта 80:

```sh
apt-get update
apt-get install -y certbot
certbot certonly --standalone -d vpn.example.com
```

[Режим standalone](https://eff-certbot.readthedocs.io/en/stable/using.html#standalone) требует доступного извне TCP-порта 80, в том числе при последующем продлении. Если порт занят веб-сервером, используйте подходящий для него способ проверки Certbot.

Посмотрите фактические пути сертификата:

```sh
certbot certificates
```

Понадобятся `fullchain.pem` и `privkey.pem`. Имя каталога сертификата может отличаться от домена. Compose монтирует весь `/etc/letsencrypt`, чтобы работали ссылки из `live/` в `archive/`.

## 3. Задайте параметры развёртывания и настройте TrustTunnel

Откройте `deploy/.env` и **замените** `TT_PUBLIC_ADDRESS` своим публичным адресом VPN. Для первого входа через SSH-туннель используйте:

```ini
TT_PUBLIC_ADDRESS=vpn.example.com:443
TT_BIND_IP=0.0.0.0
TT_PORT=443
PANEL_PORT=8080
PANEL_COOKIE_SECURE=false
PANEL_ALLOWED_HOSTS=
PANEL_TRUSTED_PROXIES=
```

`TT_PORT` — внешний порт VPN. Если меняете его, укажите тот же порт в `TT_PUBLIC_ADDRESS`. Внутренний адрес в `vpn.toml` остаётся `0.0.0.0:8443`.

Проверьте, что `PANEL_PROXY_SUBNET` из `.env` не пересекается с существующими Docker- и VPN-сетями. При необходимости измените подсеть и выберите `PANEL_PROXY_IP` внутри неё. Для первоначальной настройки оставьте опциональный профиль HTTPS выключенным.

Теперь по инструкции [«Настройка TrustTunnel»](./configuration.ru.md) **создайте новую конфигурацию или импортируйте существующую установку**, затем вернитесь к шагу 4. Для запуска нужны корректные настройки и хотя бы один VPN-пользователь.

## 4. Запустите контейнеры

Проверьте Compose-файл и соберите образы:

```sh
docker compose -f deploy/compose.yml config --quiet
docker compose -f deploy/compose.yml build
```

Если переносите работающий TrustTunnel, **после сборки образов** остановите прежний экземпляр, чтобы освободить TCP-порт. Для нативной установки с systemd unit `trusttunnel`:

```sh
systemctl stop trusttunnel
```

Проверьте настоящее имя своего сервиса. На новом сервере пропустите эту команду. Запустите контейнеры:

```sh
docker compose -f deploy/compose.yml up -d
docker compose -f deploy/compose.yml ps
curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://127.0.0.1:8080/healthz
```

Разрешите выбранный TCP-порт VPN в сетевых правилах сервера/провайдера. Проверка панели должна вернуть `ok`; она не проверяет VPN-соединение. При первом запуске панель может ответить раньше, чем endpoint завершит проверку готовности. Подождите несколько секунд, обновите обзор и убедитесь, что TrustTunnel работает, прежде чем управлять пользователями.

После переноса проверьте существующий VPN-клиент. Если подключение работает, отключите автозапуск прежнего сервиса, чтобы после перезагрузки он не занял порт контейнера. Для unit `trusttunnel`:

```sh
systemctl disable trusttunnel
```

Старые файлы сохраните для отката. Для возврата к нативному сервису сначала остановите новые контейнеры, чтобы освободить порт. Резервное копирование и обслуживание описаны в [отдельной инструкции](./operations.ru.md).

## 5. Войдите в панель

На своём компьютере откройте SSH-туннель и оставьте его работающим:

```sh
ssh -N -L 8080:127.0.0.1:8080 root@SERVER_IP
```

Перейдите на [http://127.0.0.1:8080](http://127.0.0.1:8080). Логин — **`admin`**. Прочитайте сгенерированный начальный пароль на сервере:

```sh
cat /opt/tunnelapanel/runtime/panel/bootstrap-password
```

Смените его в разделе **Settings → Administrator password** («Настройки → Пароль администратора») кнопкой **Change password** («Изменить пароль»). После смены файл начального пароля удаляется. Для выдачи VPN-доступа откройте **Users** («Пользователи»), подготовьте изменения, нажмите **Apply** («Применить») и получите конфигурацию, ссылку подключения или QR-код пользователя. Применение изменений перезапускает TrustTunnel и прерывает текущие соединения.

Первоначально интерфейс отображается на английском. Чтобы перейти на русский, выберите его в **Settings → Interface → Language** и сохраните. Язык страницы входа тоже изменится; VPN не перезапускается. Подробнее — в [настройках панели](./panel-settings.ru.md).

Для публичного доступа к панели перейдите к [настройке HTTPS](./https.ru.md).

**Обязательно настройте [продление сертификата](./https.ru.md#продление-сертификата), даже если пользуетесь панелью только через SSH. Самому TrustTunnel нужно перечитывать обновлённый сертификат.**
