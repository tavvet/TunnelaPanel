#!/bin/sh
# Certbot deploy hook: reload the backend currently serving TrustTunnel.
set -eu

lineage=${TT_CERT_LINEAGE:-/etc/letsencrypt/live/dman.pancom.pw}
panel_dir=${TT_PANEL_DIR:-/opt/tunnelapanel}

# Certbot runs global deploy hooks for every renewed certificate. Ignore others.
# An unset RENEWED_LINEAGE permits an administrator's manual smoke test.
if [ -n "${RENEWED_LINEAGE:-}" ] && [ "$RENEWED_LINEAGE" != "$lineage" ]; then
    exit 0
fi

if command -v docker >/dev/null 2>&1 && [ -f "$panel_dir/deploy/compose.yml" ]; then
    # If Docker inspection fails, report failure rather than silently missing a reload.
    endpoint_id=$(docker compose -p tunnelapanel -f "$panel_dir/deploy/compose.yml" \
        ps --status running --quiet endpoint)
    if [ -n "$endpoint_id" ]; then
        # endpoint-agent PID 1 forwards HUP to TrustTunnel for TLS-only reload.
        docker compose -p tunnelapanel -f "$panel_dir/deploy/compose.yml" \
            kill --signal SIGHUP endpoint
        exit 0
    fi
fi

# Before migration or after rollback, update the native service if it is active.
# try-restart does not start an intentionally stopped native instance.
systemctl try-restart trusttunnel
