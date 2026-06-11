#!/usr/bin/env bash

set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This installer is intended for Linux (systemd) only."
  exit 1
fi

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVICE_NAME="codex-oauth-proxy"
SERVICE_FILE_LOCAL="${PROJECT_DIR}/${SERVICE_NAME}.service"
SERVICE_FILE_SYSTEM="/etc/systemd/system/${SERVICE_NAME}.service"

read -p "Port to listen on [9879]: " PORT
PORT="${PORT:-9879}"

if [[ -z "${PORT}" ]]; then
  echo "Error: PORT must not be empty."
  exit 1
fi

read -p "Enter ADMIN_API_KEY (used for /admin endpoints): " ADMIN_API_KEY
if [[ -z "${ADMIN_API_KEY}" ]]; then
  echo "Error: ADMIN_API_KEY is required."
  exit 1
fi

echo "Project directory: ${PROJECT_DIR}"
echo "Generating systemd unit at ${SERVICE_FILE_LOCAL} (and will install to ${SERVICE_FILE_SYSTEM})."

cat > "${SERVICE_FILE_LOCAL}" <<EOF
[Unit]
Description=Codex Proxy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${PROJECT_DIR}
Environment=HOME=${HOME}
Environment=PORT=${PORT}
Environment=ADMIN_API_KEY=${ADMIN_API_KEY}
ExecStart=${PROJECT_DIR}/codex-oauth-proxy
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

echo "Local unit file created at: ${SERVICE_FILE_LOCAL}"

if [[ ! -f "${SERVICE_FILE_LOCAL}" ]]; then
  echo "❌ Error: failed to create local systemd unit file."
  exit 1
fi

if ! command -v systemctl >/dev/null 2>&1; then
  echo "systemd (systemctl) not found; skipping installation into /etc/systemd."
  echo "You can manually copy ${SERVICE_FILE_LOCAL} to ${SERVICE_FILE_SYSTEM} on a systemd-based host."
  exit 0
fi

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "Note: installing into ${SERVICE_FILE_SYSTEM} requires root."
  echo "Run the following as root (or with sudo):"
  echo "  sudo cp \"${SERVICE_FILE_LOCAL}\" \"${SERVICE_FILE_SYSTEM}\""
  echo "  sudo systemctl daemon-reload"
  echo "  sudo systemctl enable --now ${SERVICE_NAME}.service"
  echo
  echo "The service listens on 0.0.0.0:${PORT}, so it is reachable from outside this host if your firewall/network allows it."
  exit 0
fi

cp "${SERVICE_FILE_LOCAL}" "${SERVICE_FILE_SYSTEM}"
systemctl daemon-reload
systemctl enable --now "${SERVICE_NAME}.service"

echo "✅ systemd service installed and started."
echo "Status:    systemctl status ${SERVICE_NAME}.service"
echo "Logs:      journalctl -u ${SERVICE_NAME}.service -f"
echo "Listening: 0.0.0.0:${PORT} (subject to firewall rules)"
