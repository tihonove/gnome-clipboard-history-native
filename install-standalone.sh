#!/bin/sh
# Install gnome-clipboard-history-native WITHOUT apt: download the binary from GitHub
# Releases and set it up. For non-apt systems or anyone who doesn't want to add the repository.
# Usage (WITHOUT sudo):
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install-standalone.sh | sh
#
# There are no package-manager updates here — re-run the script to update.
set -eu

REPO="tihonove/gnome-clipboard-history-native"
BIN_URL="https://github.com/$REPO/releases/latest/download/gnome-clipboard-history-native-linux-x64"
DEST="/usr/local/bin/gnome-clipboard-history-native"

if [ "$(id -u)" -eq 0 ]; then
    echo "gchn: run WITHOUT sudo — sudo is applied only to the system steps." >&2
    exit 1
fi

# The GTK3 runtime is mandatory (cgo linking) — warn up front.
if command -v ldconfig >/dev/null 2>&1 && ! ldconfig -p | grep -q 'libgtk-3\.so'; then
    echo "gchn: libgtk-3 not found — without GTK3 the binary won't run." >&2
    echo "  Ubuntu/Debian: sudo apt install libgtk-3-0" >&2
fi

echo "gchn: downloading the binary → $DEST"
tmp="$(mktemp)"
curl -fsSL "$BIN_URL" -o "$tmp"
sudo install -m 0755 "$tmp" "$DEST"
rm -f "$tmp"

# Setup lives entirely in --install: autostart + hotkey + daemon, and on Wayland it
# configures access to /dev/uinput once itself (escalates via sudo/pkexec — there's no
# package udev rule here).
echo "gchn: configuring (autostart, hotkey, /dev/uinput if needed)"
"$DEST" --install

echo "gchn: done. Super+Ctrl+V works."
