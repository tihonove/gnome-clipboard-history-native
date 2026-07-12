#!/bin/sh
# Install gnome-clipboard-history-native via the apt repository on GitHub Pages.
# Usage (WITHOUT sudo — the script escalates where needed):
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
#
# Idempotent: re-running updates the key/list and reinstalls the package.
set -eu

REPO_URL="https://tihonove.github.io/gnome-clipboard-history-native"
KEYRING="/etc/apt/keyrings/gnome-clipboard-history-native.gpg"
LIST="/etc/apt/sources.list.d/gnome-clipboard-history-native.list"

if ! command -v apt-get >/dev/null 2>&1; then
    echo "gchn: this installer is for apt distributions (Ubuntu/Debian)." >&2
    echo "Non-apt system? Use the standalone script or the binary:" >&2
    echo "  https://github.com/tihonove/gnome-clipboard-history-native/releases" >&2
    exit 1
fi

# Must run as your own user: only then can the final `--install` bring up the daemon
# and hotkey in YOUR graphical session (a root postinst can't do that).
if [ "$(id -u)" -eq 0 ]; then
    echo "gchn: run WITHOUT sudo — the script applies sudo to the system steps itself." >&2
    exit 1
fi

echo "gchn: adding the repository key → $KEYRING"
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL "$REPO_URL/gnome-clipboard-history-native-archive-keyring.gpg" | sudo tee "$KEYRING" >/dev/null

echo "gchn: writing the source → $LIST"
echo "deb [signed-by=$KEYRING] $REPO_URL stable main" | sudo tee "$LIST" >/dev/null

echo "gchn: apt update + install (the package installs the /dev/uinput udev rule)"
sudo apt-get update
sudo apt-get install -y gnome-clipboard-history-native

# The per-user part in the current session: hotkey + start the daemon right now (no re-login).
echo "gchn: configuring the hotkey and starting the daemon in the current session"
gnome-clipboard-history-native --install

echo "gchn: done. Super+Ctrl+V works; updates come via apt."
