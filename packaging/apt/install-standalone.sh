#!/bin/sh
# Установка gnome-clipboard-history-native БЕЗ apt: скачать бинарник из GitHub
# Releases и настроить. Для не-apt систем или кто не хочет добавлять репозиторий.
# Использование (БЕЗ sudo):
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install-standalone.sh | sh
#
# Обновлений через пакетный менеджер тут нет — перезапусти скрипт, чтобы обновиться.
set -eu

REPO="tihonove/gnome-clipboard-history-native"
BIN_URL="https://github.com/$REPO/releases/latest/download/gnome-clipboard-history-native-linux-x64"
DEST="/usr/local/bin/gnome-clipboard-history-native"

if [ "$(id -u)" -eq 0 ]; then
    echo "gchn: запусти БЕЗ sudo — sudo применится только к системным шагам." >&2
    exit 1
fi

# GTK3 рантайм нужен обязательно (cgo-линковка) — предупредим заранее.
if command -v ldconfig >/dev/null 2>&1 && ! ldconfig -p | grep -q 'libgtk-3\.so'; then
    echo "gchn: не вижу libgtk-3 — без GTK3 бинарник не запустится." >&2
    echo "  Ubuntu/Debian: sudo apt install libgtk-3-0" >&2
fi

echo "gchn: качаю бинарник → $DEST"
tmp="$(mktemp)"
curl -fsSL "$BIN_URL" -o "$tmp"
sudo install -m 0755 "$tmp" "$DEST"
rm -f "$tmp"

# Настройка целиком в --install: автозапуск + хоткей + демон, а на Wayland он сам
# один раз настроит доступ к /dev/uinput (эскалируется через sudo/pkexec — пакетного
# udev-правила тут нет).
echo "gchn: настраиваю (автозапуск, хоткей, /dev/uinput при необходимости)"
"$DEST" --install

echo "gchn: готово. Super+Ctrl+V работает."
