#!/bin/sh
# Bring up NetBird, pin ClickHouse Cloud through ch-egress, then run Insight Forge.
# ClickHouse should see dedicated egress IP 209.71.103.68 (same as FMP).
set -eu

unset HTTPS_PROXY HTTP_PROXY ALL_PROXY https_proxy http_proxy all_proxy

if [ -z "${NB_SETUP_KEY:-}" ]; then
    echo "[insight-forge] NB_SETUP_KEY unset — skipping NetBird (local/sprite mode)"
    exec "$@"
fi

echo "[insight-forge] bringing NetBird up as ${NB_HOSTNAME:-insight-forge}"
if [ -f /var/lib/netbird/default.json ]; then
    netbird up \
        --management-url "${NB_MANAGEMENT_URL}" \
        --hostname "${NB_HOSTNAME:-insight-forge}" \
        --log-level "${NB_LOG_LEVEL:-info}" \
        --log-file console \
        --foreground-mode &
else
    netbird up \
        --setup-key "$NB_SETUP_KEY" \
        --management-url "${NB_MANAGEMENT_URL}" \
        --hostname "${NB_HOSTNAME:-insight-forge}" \
        --log-level "${NB_LOG_LEVEL:-info}" \
        --log-file console \
        --foreground-mode &
fi

i=0
while [ "$i" -lt 45 ]; do
    if ip link show wt0 >/dev/null 2>&1; then
        break
    fi
    i=$((i + 1))
    sleep 1
done
if ! ip link show wt0 >/dev/null 2>&1; then
    echo "[insight-forge] NetBird interface wt0 did not come up" >&2
    exec "$@"
fi
echo "[insight-forge] NetBird is up"

CH_DNS="${CH_DNS:-igijlwqd6s.eastus2.azure.clickhouse.cloud}"
GW_IP="${EGRESS_NB_IP:-100.86.156.220}"
GW_KEY="${EGRESS_NB_KEY:-RpiCUdaDS9WUao9/O3EDsPk/PRhFRvo5rOAVJTCU918=}"
CH_IP="$(getent ahostsv4 "$CH_DNS" | awk '{print $1}' | grep -E '^[0-9.]+$' | head -1 || true)"
if [ -n "$CH_IP" ]; then
    wg set wt0 peer "$GW_KEY" allowed-ips "${GW_IP}/32,${CH_IP}/32" || true
    ip route replace "${CH_IP}/32" dev wt0 || true
    echo "[insight-forge] routed ${CH_DNS} (${CH_IP}) via ${GW_IP}"
fi
ping -c 1 -W 3 "$GW_IP" >/dev/null 2>&1 || true

if [ -x /usr/local/bin/nb-ch-route.sh ]; then
    /usr/local/bin/nb-ch-route.sh &
fi

if command -v curl >/dev/null 2>&1; then
    if curl -sk --max-time 8 -o /dev/null "https://${CH_DNS}:8443/ping"; then
        echo "[insight-forge] ClickHouse reachable via ch-egress"
    else
        echo "[insight-forge] ClickHouse not ready yet; starting server anyway" >&2
    fi
fi

exec "$@"
