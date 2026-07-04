#!/bin/sh
# Установка clipmgr через apt-репозиторий на GitHub Pages.
# Использование (БЕЗ sudo — скрипт сам поднимет права где надо):
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
#
# Идемпотентен: повторный запуск обновит ключ/список и переустановит пакет.
set -eu

REPO_URL="https://tihonove.github.io/gnome-clipboard-history-native"
KEYRING="/etc/apt/keyrings/clipmgr.gpg"
LIST="/etc/apt/sources.list.d/clipmgr.list"

if ! command -v apt-get >/dev/null 2>&1; then
    echo "clipmgr: этот установщик для apt-дистрибутивов (Ubuntu/Debian)." >&2
    echo "Не-apt система? Возьми standalone-скрипт или бинарник:" >&2
    echo "  https://github.com/tihonove/gnome-clipboard-history-native/releases" >&2
    exit 1
fi

# Запускать нужно от своего юзера: только так финальный `clipmgr --install`
# поднимет демона и хоткей в ТВОЕЙ графической сессии (root-postinst этого не может).
if [ "$(id -u)" -eq 0 ]; then
    echo "clipmgr: запусти БЕЗ sudo — скрипт сам применит sudo к системным шагам." >&2
    exit 1
fi

echo "clipmgr: добавляю ключ репозитория → $KEYRING"
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL "$REPO_URL/clipmgr-archive-keyring.gpg" | sudo tee "$KEYRING" >/dev/null

echo "clipmgr: прописываю источник → $LIST"
echo "deb [signed-by=$KEYRING] $REPO_URL stable main" | sudo tee "$LIST" >/dev/null

echo "clipmgr: apt update + установка (пакет положит udev-правило /dev/uinput)"
sudo apt-get update
sudo apt-get install -y clipmgr

# Per-user часть в текущей сессии: хоткей + запуск демона прямо сейчас (без релогина).
echo "clipmgr: настраиваю хоткей и запускаю демона в текущей сессии"
clipmgr --install

echo "clipmgr: готово. Super+Ctrl+V работает; обновления — через apt."
