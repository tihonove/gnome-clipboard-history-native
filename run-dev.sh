#!/usr/bin/env bash
# run-dev.sh — сборка и запуск ДЕВ-инстанса clipmgr в FOREGROUND (с логами).
#
# Отдельный сокет/слот gsettings/хоткей — не толкается с установленным clipmgr
# (см. раздел «Дев-инстанс» в CLAUDE.md). Хоткей дев-меню: Super+Ctrl+B.
# Логи демона льются прямо в терминал; Ctrl+C — останавливает демон и чистит сокет.
#
# Запуск:  ./run-dev.sh          (Ctrl+C — стоп)
# Свой сокет/хоткей:  CLIPMGR_SOCK=… CLIPMGR_HOTKEY='<Super><Control>b' ./run-dev.sh
set -euo pipefail
cd "$(dirname "$0")"

SOCK="${CLIPMGR_SOCK:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/clipmgr-dev.sock}"
HOTKEY="${CLIPMGR_HOTKEY:-<Super><Control>b}"
NAME='clipmgr-dev'
BASE='org.gnome.settings-daemon.plugins.media-keys'
KB_ROOT='/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings'

log() { printf '\033[1;34m[dev]\033[0m %s\n' "$*" >&2; }

# 1) Сборка (всегда — чтобы гонять свежий код).
log "сборка clipmgr-dev…"
go build -o clipmgr-dev ./cmd/clipmgr

# 2) Разовая регистрация дев-хоткея. Ставим только если слота ещё нет — чтобы на
# Wayland лишний раз не дёргать привилегированную настройку uinput внутри --install.
dev_hotkey_present() {
	local list p schema cmd
	list=$(gsettings get "$BASE" custom-keybindings 2>/dev/null) || return 1
	for p in $(printf '%s' "$list" | grep -oE "custom[0-9]+/"); do
		schema="$BASE.custom-keybinding:$KB_ROOT/$p"
		cmd=$(gsettings get "$schema" command 2>/dev/null || echo)
		case "$cmd" in *clipmgr-dev*) return 0 ;; esac
	done
	return 1
}
if dev_hotkey_present; then
	log "дев-хоткей уже настроен"
else
	log "регистрирую дев-хоткей $HOTKEY…"
	CLIPMGR_SOCK="$SOCK" CLIPMGR_HOTKEY="$HOTKEY" CLIPMGR_NAME="$NAME" ./clipmgr-dev --install
fi

# 3) Погасить прежний дев-демон и подчистить сокет.
pkill -x clipmgr-dev 2>/dev/null || true
rm -f "$SOCK"
sleep 0.3

# 4) Демон в foreground: логи в терминал, Ctrl+C → стоп + уборка сокета.
trap 'echo; log "останавливаю дев-демон…"; kill "$D" 2>/dev/null || true; wait "$D" 2>/dev/null || true; rm -f "$SOCK"; exit 0' INT TERM
log "старт дев-демона (сокет $SOCK). Меню: $HOTKEY. Ctrl+C — стоп."
env CLIPMGR_SOCK="$SOCK" ./clipmgr-dev &
D=$!
wait "$D"
