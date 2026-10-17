#!/bin/sh
# Certbot deploy hook: reload TrustTunnel and the optional HTTPS panel proxy.
set -eu

lineage=${TT_CERT_LINEAGE:-/etc/letsencrypt/live/vpn.example.com}
panel_dir=${TT_PANEL_DIR:-/opt/tunnelapanel}

# Certbot runs global deploy hooks for every renewed certificate. Ignore others.
# An unset RENEWED_LINEAGE permits an administrator's manual smoke test.
if [ -n "${RENEWED_LINEAGE:-}" ] && [ "$RENEWED_LINEAGE" != "$lineage" ]; then
    exit 0
fi

compose() {
    docker compose -p tunnelapanel -f "$panel_dir/deploy/compose.yml" "$@"
}

endpoint_id=
https_id=
if command -v docker >/dev/null 2>&1 && [ -f "$panel_dir/deploy/compose.yml" ]; then
    # If Docker inspection fails, report failure rather than silently missing a reload.
    endpoint_id=$(compose ps --status running --quiet endpoint)
    https_id=$(compose --profile https ps --status running --quiet https)
fi

# Try both consumers even if one fails, and make any failure visible to Certbot.
result=0
if [ -n "$endpoint_id" ]; then
    # endpoint-agent PID 1 forwards HUP for TLS-only reload.
    # Signal inside the container: Docker kill marks it manually stopped and
    # prevents unless-stopped from bringing it back after a daemon restart.
    compose exec -T endpoint /bin/sh -c 'kill -HUP 1' || result=1
else
    # Do not start an intentionally stopped native instance.
    systemctl try-restart trusttunnel || result=1
fi

if [ -n "$https_id" ]; then
    # Check renewed certificate/key readability before gracefully reloading nginx.
    if compose --profile https exec -T https nginx -t; then
        compose --profile https exec -T https nginx -s reload || result=1
    else
        result=1
    fi
fi
exit "$result"
