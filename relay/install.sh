#!/usr/bin/env bash
set -euo pipefail

# Bondify relay installer.
# Installs a prebuilt bondify-relay binary, writes a root-only environment file,
# creates a systemd service, enables forwarding/NAT through the relay binary,
# and starts the service. Safe to re-run.

INSTALL_DIR=${INSTALL_DIR:-/usr/local/bin}
CONFIG_DIR=${CONFIG_DIR:-/etc/bondify}
SERVICE_NAME=${SERVICE_NAME:-bondify-relay}
LISTEN=${LISTEN:-:51820}
POOL=${POOL:-10.77.0.0/24}
TUN=${TUN:-bondify0}
DNS=${DNS:-1.1.1.1,9.9.9.9}
KEEPALIVE=${KEEPALIVE:-15}
BINARY_SOURCE=${1:-}

log() { printf '[bondify-install] %s\n' "$*"; }
die() { printf '[bondify-install] ERROR: %s\n' "$*" >&2; exit 1; }

[[ ${EUID} -eq 0 ]] || die "run as root (sudo relay/install.sh /path/to/bondify-relay)"
command -v systemctl >/dev/null 2>&1 || die "systemd is required"
command -v ip >/dev/null 2>&1 || die "iproute2 is required"

if [[ -z ${BINARY_SOURCE} ]]; then
  if [[ -x ./bondify-relay ]]; then
    BINARY_SOURCE=./bondify-relay
  elif [[ -x ./build/bondify-relay ]]; then
    BINARY_SOURCE=./build/bondify-relay
  else
    die "pass the built relay binary as argument 1"
  fi
fi
[[ -f ${BINARY_SOURCE} ]] || die "relay binary not found: ${BINARY_SOURCE}"

NAT_IFACE=${NAT_IFACE:-$(ip -4 route show default | awk 'NR==1 {for (i=1;i<=NF;i++) if ($i=="dev") {print $(i+1); exit}}')}
[[ -n ${NAT_IFACE} ]] || die "could not determine default IPv4 egress interface; set NAT_IFACE explicitly"

install -d -m 0755 "${INSTALL_DIR}"
install -m 0755 "${BINARY_SOURCE}" "${INSTALL_DIR}/bondify-relay"
install -d -m 0700 "${CONFIG_DIR}"

cat >"${CONFIG_DIR}/relay.env" <<EOF
BONDIFY_LISTEN=${LISTEN}
BONDIFY_POOL=${POOL}
BONDIFY_TUN=${TUN}
BONDIFY_DNS=${DNS}
BONDIFY_KEEPALIVE=${KEEPALIVE}
BONDIFY_NAT_IFACE=${NAT_IFACE}
EOF
chmod 0600 "${CONFIG_DIR}/relay.env"

cat >"/etc/systemd/system/${SERVICE_NAME}.service" <<EOF
[Unit]
Description=Bondify WAN bonding relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=${CONFIG_DIR}/relay.env
ExecStart=${INSTALL_DIR}/bondify-relay \\
  -listen \${BONDIFY_LISTEN} \\
  -pool \${BONDIFY_POOL} \\
  -tun \${BONDIFY_TUN} \\
  -dns \${BONDIFY_DNS} \\
  -keepalive \${BONDIFY_KEEPALIVE} \\
  -nat-iface \${BONDIFY_NAT_IFACE} \\
  -key-file ${CONFIG_DIR}/relay.key
Restart=on-failure
RestartSec=2
LimitNOFILE=1048576
NoNewPrivileges=true
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "${SERVICE_NAME}.service"
sleep 1

if ! systemctl is-active --quiet "${SERVICE_NAME}.service"; then
  systemctl --no-pager --full status "${SERVICE_NAME}.service" || true
  die "service did not start"
fi

PUBKEY=$(journalctl -u "${SERVICE_NAME}.service" -n 100 --no-pager 2>/dev/null | sed -n 's/.*relay: public key: //p' | tail -n1)

log "installed ${INSTALL_DIR}/bondify-relay"
log "service: ${SERVICE_NAME}.service (active)"
log "UDP listen: ${LISTEN}"
log "tunnel pool: ${POOL}"
log "NAT egress: ${NAT_IFACE}"
if [[ -n ${PUBKEY} ]]; then
  log "relay public key: ${PUBKEY}"
else
  log "relay public key is in: journalctl -u ${SERVICE_NAME}.service"
fi
log "next: open UDP port ${LISTEN#:} in the VPS/cloud firewall if one is present"
