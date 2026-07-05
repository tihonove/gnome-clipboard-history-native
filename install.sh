#!/bin/sh
# Установка gnome-clipboard-history-native через apt-репозиторий на GitHub Pages.
# Использование (БЕЗ sudo — скрипт сам поднимет права где надо):
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
#
# Идемпотентен: повторный запуск обновит ключ/список и переустановит пакет.
set -eu

REPO_URL="https://tihonove.github.io/gnome-clipboard-history-native"
KEYRING="/etc/apt/keyrings/gnome-clipboard-history-native.gpg"
LIST="/etc/apt/sources.list.d/gnome-clipboard-history-native.list"

if ! command -v apt-get >/dev/null 2>&1; then
    echo "gchn: этот установщик для apt-дистрибутивов (Ubuntu/Debian)." >&2
    echo "Не-apt система? Возьми standalone-скрипт или бинарник:" >&2
    echo "  https://github.com/tihonove/gnome-clipboard-history-native/releases" >&2
    exit 1
fi

# Запускать нужно от своего юзера: только так финальный `--install` поднимет демона
# и хоткей в ТВОЕЙ графической сессии (root-postinst этого не может).
if [ "$(id -u)" -eq 0 ]; then
    echo "gchn: запусти БЕЗ sudo — скрипт сам применит sudo к системным шагам." >&2
    exit 1
fi

echo "gchn: добавляю ключ репозитория → $KEYRING"
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL "$REPO_URL/gnome-clipboard-history-native-archive-keyring.gpg" | sudo tee "$KEYRING" >/dev/null

echo "gchn: прописываю источник → $LIST"
echo "deb [signed-by=$KEYRING] $REPO_URL stable main" | sudo tee "$LIST" >/dev/null

echo "gchn: apt update + установка (пакет положит udev-правило /dev/uinput)"
sudo apt-get update
sudo apt-get install -y gnome-clipboard-history-native

# Per-user часть в текущей сессии: хоткей + запуск демона прямо сейчас (без релогина).
echo "gchn: настраиваю хоткей и запускаю демона в текущей сессии"
gnome-clipboard-history-native --install

echo "gchn: готово. Super+Ctrl+V работает; обновления — через apt."
