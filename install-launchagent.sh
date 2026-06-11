#!/bin/bash

set -e

read -p "Enter the ADMIN_API_KEY: " ADMIN_API_KEY
if [ -z "${ADMIN_API_KEY}" ]; then
  echo "Error: ADMIN_API_KEY is required."
  exit 1
fi

PROJECT_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST_NAME="sh.d.codex-oauth-proxy.plist"
PLIST_LOCAL="${PROJECT_DIR}/${PLIST_NAME}"
LAUNCHAGENTS_DIR="${HOME}/Library/LaunchAgents"
PLIST_SYMLINK="${LAUNCHAGENTS_DIR}/${PLIST_NAME}"

echo "Installing codex-oauth-proxy LaunchAgent..."
echo "Project directory: ${PROJECT_DIR}"

mkdir -p "${LAUNCHAGENTS_DIR}"

cat > "${PLIST_LOCAL}" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>sh.d.codex-oauth-proxy</string>

    <key>Program</key>
    <string>${PROJECT_DIR}/run_proxy.sh</string>

    <key>WorkingDirectory</key>
    <string>${PROJECT_DIR}</string>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
        <key>Crashed</key>
        <true/>
    </dict>

    <key>ThrottleInterval</key>
    <integer>30</integer>

    <key>StandardOutPath</key>
    <string>${HOME}/Library/Logs/codex-oauth-proxy.log</string>

    <key>StandardErrorPath</key>
    <string>${HOME}/Library/Logs/codex-oauth-proxy.error.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PORT</key>
        <string>9879</string>
        <key>HOME</key>
        <string>${HOME}</string>
        <key>PATH</key>
        <string>${PATH}</string>
        <key>ADMIN_API_KEY</key>
        <string>${ADMIN_API_KEY}</string>
    </dict>
</dict>
</plist>
EOF

echo "Plist file created at: ${PLIST_LOCAL}"

if [ ! -f "${PLIST_LOCAL}" ]; then
    echo "❌ Error: Failed to create plist file"
    exit 1
fi

if [ -L "${PLIST_SYMLINK}" ]; then
    echo "Removing old symlink..."
    rm "${PLIST_SYMLINK}"
fi

if launchctl list | grep -q "sh.d.codex-oauth-proxy"; then
    echo "Unloading existing service..."
    launchctl unload "${PLIST_SYMLINK}" 2>/dev/null || true
fi

echo "Creating symlink: ${PLIST_SYMLINK} -> ${PLIST_LOCAL}"
ln -sf "${PLIST_LOCAL}" "${PLIST_SYMLINK}"

if [ ! -L "${PLIST_SYMLINK}" ]; then
    echo "❌ Error: Failed to create symlink"
    exit 1
fi

echo "Loading service..."
launchctl load "${PLIST_SYMLINK}"

sleep 2
if launchctl list | grep -q "sh.d.codex-oauth-proxy"; then
    echo "✅ LaunchAgent installed and started successfully!"
    echo ""
    echo "Service management commands:"
    echo "  Check status:  launchctl list | grep codex-oauth-proxy"
    echo "  View logs:     tail -f ~/Library/Logs/codex-oauth-proxy.log"
    echo "  View errors:   tail -f ~/Library/Logs/codex-oauth-proxy.error.log"
    echo "  Stop service:  launchctl unload ~/Library/LaunchAgents/${PLIST_NAME}"
    echo "  Start service: launchctl load ~/Library/LaunchAgents/${PLIST_NAME}"
    echo "  Uninstall:     ./uninstall-launchagent.sh"
else
    echo "⚠️  Service may not have started correctly. Check logs at:"
    echo "  ~/Library/Logs/codex-oauth-proxy.error.log"
fi
