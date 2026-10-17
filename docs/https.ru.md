[Проект](../README.ru.md) · [English](./https.md) · **Русский**

# Доступ по HTTPS

Опциональный сервис `https` открывает панель по адресу **`https://vpn.example.com:8443`**, а TrustTunnel остаётся на `vpn.example.com:443`. Замените пример домена своим. Один сертификат подходит для обоих портов; дополнительная DNS-запись не нужна.

Сначала завершите [установку](./installation.ru.md) и подготовьте действующий сертификат под управлением Certbot. Команды ниже выполняются от root из `/opt/tunnelapanel`.

## Включение прокси

В `deploy/nginx.conf` замените жёстко заданное имя хоста и оба пути сертификата. Файл **не** подставляет домен из `.env`. Остальную конфигурацию сохраните:

```nginx
server_name vpn.example.com;
ssl_certificate /etc/letsencrypt/live/vpn.example.com/fullchain.pem;
ssl_certificate_key /etc/letsencrypt/live/vpn.example.com/privkey.pem;
```

Используйте реальные пути из `certbot certificates`: имя каталога сертификата может отличаться от домена. Весь `/etc/letsencrypt` монтируется только для чтения, чтобы ссылки из `live/` в `archive/` работали после продления.

Укажите в `deploy/.env`:

```ini
COMPOSE_PROFILES=https
PANEL_COOKIE_SECURE=true
PANEL_ALLOWED_HOSTS=vpn.example.com
PANEL_HTTPS_PORT=8443
PANEL_PROXY_SUBNET=172.30.80.0/29
PANEL_PROXY_IP=172.30.80.3
PANEL_TRUSTED_PROXIES=172.30.80.3
```

В `PANEL_ALLOWED_HOSTS` указывается имя без схемы и порта. Проверьте, что Docker-подсеть не пересекается с существующими сетями Docker или VPN. При её изменении выберите фиксированный `PANEL_PROXY_IP` внутри новой подсети и укажите тот же адрес в `PANEL_TRUSTED_PROXIES`. Панель сможет использовать переданный nginx `X-Real-IP` для лимитов входа и аудита; остальные узлы не смогут подставлять доверенный адрес клиента.

Разрешите входящий TCP-порт `8443` в сетевых правилах сервера/провайдера. Проверьте nginx и пересоздайте только панель и прокси:

```sh
docker compose -f deploy/compose.yml config --quiet
docker compose -f deploy/compose.yml run --rm --no-deps https -t
docker compose -f deploy/compose.yml up -d --no-deps panel
docker compose -f deploy/compose.yml up -d --no-deps https
curl --fail --retry 5 --retry-connrefused --retry-delay 1 https://vpn.example.com:8443/healthz
```

Войдите по HTTPS с прежними данными администратора. Эти команды не перезапускают VPN. После включения Secure cookies используйте HTTPS для обычного входа; HTTP через SSH-туннель больше не является поддерживаемым способом входа.

| Сервис | Порт сервера | Назначение |
| --- | --- | --- |
| `endpoint` | `443/tcp` | VPN TrustTunnel |
| `https` | `8443/tcp` | Публичная HTTPS-панель |
| `panel` | `127.0.0.1:8080/tcp` | HTTP-бэкенд на loopback |

Сохраните в nginx `proxy_set_header Host $http_host`: панель проверяет полный источник запроса, включая `:8443`. Замена на `$host` убирает порт и ломает отправку форм. Также сохраните политику referrer `same-origin` и защиту CSRF. Настройки VPN описаны в [конфигурации TrustTunnel](./configuration.ru.md).

## Продление сертификата

**Настройте подхват продлённого сертификата даже при доступе к панели только через SSH: сертификат нужен самому TrustTunnel.** Выпуском и продлением занимается Certbot на сервере; панель этого не делает.

В `deploy/certbot-deploy-hook.sh` сохраните правильные значения пути сертификата и каталога установки непосредственно в файле:

```sh
lineage=${TT_CERT_LINEAGE:-/etc/letsencrypt/live/vpn.example.com}
panel_dir=${TT_PANEL_DIR:-/opt/tunnelapanel}
```

Переменные интерактивной SSH-сессии не являются постоянной настройкой для автоматических запусков Certbot. Используйте реальный каталог сертификата, который не обязательно совпадает с доменом. Перед миграцией проверьте существующие deploy hooks и сохраните их копии: старый hook не должен запускать прежний VPN-сервис после переноса. Сохраните hooks, нужные другим сертификатам и сервисам.

Сохраните существующий целевой hook, затем установите и проверьте новый вручную:

При обновлении используйте в командах ниже имя уже установленного hook (например, `trusttunnel.sh` вместо `tunnelapanel.sh`). Если оставить старый hook рядом с новым, Certbot выполнит оба. Храните резервные копии вне каталога deploy hooks.

```sh
install -d /etc/letsencrypt/renewal-hooks/deploy
hook_backup=$(mktemp -d /root/tunnelapanel-hook.XXXXXX)
if [ -e /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh ]; then
  cp -p /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh "$hook_backup/"
fi
install -m 755 deploy/certbot-deploy-hook.sh /etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
/etc/letsencrypt/renewal-hooks/deploy/tunnelapanel.sh
```

Hook фильтрует продления Certbot по каталогу сертификата, отправляет HUP работающему endpoint и проверяет/перезагружает nginx, если тот запущен. До миграции или после отката он вместо этого перезапускает активный системный сервис `trusttunnel`, не запуская остановленный. При ошибке Certbot получает ненулевой код завершения.

Перезагрузка сертификатов выполняется внутри контейнеров через `exec`. Не заменяйте эти команды на `docker kill` или `docker compose kill`: даже SIGHUP, не завершающий процесс, [может отключить автоматический запуск после перезагрузки сервера](https://github.com/moby/moby/issues/47792) при `restart: unless-stopped`. Если ранее был установлен hook с `compose kill`, замените также установленную копию и выполните [шаги восстановления](operations.ru.md#после-перезагрузки-сервера).

Проверьте расписание автоматического продления Certbot, журнал endpoint и сертификат в новых TLS-соединениях на обоих используемых портах. Сохраните прежний способ проверки ACME и необходимый для продления доступ. Перед обновлениями сохраняйте свои правки nginx и hook; см. [эксплуатацию](./operations.ru.md).
