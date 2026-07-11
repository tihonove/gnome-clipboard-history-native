#!/usr/bin/env bash
# run-dev.sh — build and run a DEV instance in the FOREGROUND (with logs).
#
# A separate socket / gsettings slot / hotkey — doesn't collide with the installed
# gnome-clipboard-history-native (see the "Dev instance" section in CLAUDE.md).
# Dev-menu hotkey: Super+Ctrl+B. The daemon's logs stream straight to the terminal;
# Ctrl+C stops the daemon and cleans up the socket.
#
# Run:  ./run-dev.sh          (Ctrl+C to stop)
# Custom socket/hotkey:  GCHN_SOCK=… GCHN_HOTKEY='<Super><Control>b' ./run-dev.sh
set -euo pipefail
cd "$(dirname "$0")"

BIN='gnome-clipboard-history-native-dev'
SOCK="${GCHN_SOCK:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/gnome-clipboard-history-native-dev.sock}"
HOTKEY="${GCHN_HOTKEY:-<Super><Control>b}"
NAME='gnome-clipboard-history-native-dev'
BASE='org.gnome.settings-daemon.plugins.media-keys'
KB_ROOT='/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings'

log() { printf '\033[1;34m[dev]\033[0m %s\n' "$*" >&2; }

# 1) Build (always — to run the freshest code).
log "building $BIN…"
go build -o "$BIN" ./cmd/gnome-clipboard-history-native

# 2) One-time registration of the dev hotkey. Install only if the slot doesn't exist yet — so on
# Wayland we don't needlessly trigger the privileged uinput setup inside --install.
dev_hotkey_present() {
	local list p schema cmd
	list=$(gsettings get "$BASE" custom-keybindings 2>/dev/null) || return 1
	for p in $(printf '%s' "$list" | grep -oE "custom[0-9]+/"); do
		schema="$BASE.custom-keybinding:$KB_ROOT/$p"
		cmd=$(gsettings get "$schema" command 2>/dev/null || echo)
		case "$cmd" in *"$NAME"*) return 0 ;; esac
	done
	return 1
}
if dev_hotkey_present; then
	log "dev hotkey already configured"
else
	log "registering dev hotkey $HOTKEY…"
	GCHN_SOCK="$SOCK" GCHN_HOTKEY="$HOTKEY" GCHN_NAME="$NAME" "./$BIN" --install
fi

# 3) Kill the previous dev daemon and clean up the socket. pkill -f (not -x): the name is longer than
# 15 chars — the kernel truncates comm, so we match on the full command line.
pkill -f "$BIN" 2>/dev/null || true
rm -f "$SOCK"
sleep 0.3

# 4) Daemon in the foreground: logs to the terminal, Ctrl+C → stop + socket cleanup.
trap 'echo; log "stopping dev daemon…"; kill "$D" 2>/dev/null || true; wait "$D" 2>/dev/null || true; rm -f "$SOCK"; exit 0' INT TERM
log "starting dev daemon (socket $SOCK). Menu: $HOTKEY. Ctrl+C to stop."
env GCHN_SOCK="$SOCK" "./$BIN" &
D=$!
wait "$D"
