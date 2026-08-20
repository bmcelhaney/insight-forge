#!/usr/bin/env bash
# Keep ClickHouse Cloud's public IPv4 routed through the ch-egress NetBird peer.
# Management syncs can wipe AllowedIPs; re-apply on an interval.
set -euo pipefail
export PATH="/usr/sbin:/usr/bin:/bin:${PATH}"
unset HTTPS_PROXY HTTP_PROXY ALL_PROXY https_proxy http_proxy all_proxy

EGRESS_HOST="${EGRESS_HOST:-ch-egress.netbird.selfhosted}"
CH_DNS="${CH_DNS:-igijlwqd6s.eastus2.azure.clickhouse.cloud}"
WG_IFACE="${WG_IFACE:-wt0}"
INTERVAL="${INTERVAL:-15}"

discover_peer() {
  python3 - "$EGRESS_HOST" <<'PY'
import subprocess, sys
want = sys.argv[1]
try:
    out = subprocess.check_output(["netbird", "status", "--detail"], text=True, errors="replace")
except Exception:
    print("100.86.156.220 RpiCUdaDS9WUao9/O3EDsPk/PRhFRvo5rOAVJTCU918=")
    raise SystemExit
peers = []
rec = {}
in_peers = False
for line in out.splitlines():
    if line.startswith("Peers detail"):
        in_peers = True
        rec = {}
        continue
    if in_peers and line and not line[0].isspace() and line[0].isalpha():
        if rec.get("ip") and rec.get("key"):
            peers.append(rec)
        break
    if not in_peers:
        continue
    if line.startswith(" ") and line.strip().endswith(":") and "detail" not in line:
        if rec.get("ip") and rec.get("key"):
            peers.append(rec)
        rec = {"name": line.strip().rstrip(":"), "ip": "", "key": "", "status": ""}
        continue
    s = line.strip()
    if s.startswith("NetBird IP:") and "IPv6" not in s and not rec.get("ip"):
        rec["ip"] = s.split(":", 1)[1].strip().split("/")[0]
    elif s.startswith("Public key:") and not rec.get("key"):
        rec["key"] = s.split(":", 1)[1].strip()
    elif s.startswith("Status:") and not rec.get("status"):
        rec["status"] = s.split(":", 1)[1].strip()
if rec.get("ip") and rec.get("key"):
    peers.append(rec)

local_ip = ""
for line in out.splitlines():
    s = line.strip()
    if s.startswith("NetBird IP:") and "IPv6" not in s and not line.startswith(" "):
        local_ip = s.split(":", 1)[1].strip().split("/")[0]

def dump(p):
    if p["ip"] == local_ip:
        return False
    print(f"{p['ip']} {p['key']}")
    return True

for p in peers:
    if p["name"] == want and dump(p):
        raise SystemExit
helpers = [p for p in peers if "ch-egress" in p["name"]]
for p in helpers:
    if p.get("status") == "Connected" and dump(p):
        raise SystemExit
for p in helpers:
    if dump(p):
        raise SystemExit
print("100.86.156.220 RpiCUdaDS9WUao9/O3EDsPk/PRhFRvo5rOAVJTCU918=")
PY
}

wake_peer() {
  local ip="$1"
  local i
  for i in $(seq 1 10); do
    if ping -c 1 -W 3 "$ip" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  return 1
}

resolve_ch_v4() {
  getent ahostsv4 "$CH_DNS" | awk '{print $1}' | grep -E '^[0-9.]+$' | head -1
}

apply_route() {
  local peer_ip="$1"
  local peer_key="$2"
  local ch_ip="$3"
  local self_ip
  self_ip="$(ip -4 -o addr show "$WG_IFACE" | awk '{print $4}' | cut -d/ -f1 | head -1 || true)"
  if [ -z "$peer_ip" ] || [ "$peer_ip" = "$self_ip" ] || [ "$peer_ip" = "$ch_ip" ]; then
    echo "[nb-ch-route] refusing to route via ${peer_ip} (self or empty)" >&2
    return 1
  fi
  wg set "$WG_IFACE" peer "$peer_key" allowed-ips "${peer_ip}/32,${ch_ip}/32"
  ip route replace "${ch_ip}/32" dev "$WG_IFACE"
}

last=""
while true; do
  peer="$(discover_peer || true)"
  ch_ip="$(resolve_ch_v4 || true)"
  if [ -n "$peer" ] && [ -n "$ch_ip" ]; then
    peer_ip="${peer%% *}"
    peer_key="${peer#* }"
    wake_peer "$peer_ip" || true
    state="${peer_ip} ${ch_ip}"
    if [ "$state" != "$last" ]; then
      echo "[nb-ch-route] ClickHouse ${CH_DNS} -> ${ch_ip} via ${peer_ip} (${WG_IFACE})"
      last="$state"
    fi
    apply_route "$peer_ip" "$peer_key" "$ch_ip" || true
  fi
  sleep "$INTERVAL"
done
