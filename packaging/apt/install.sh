#!/bin/sh
# Однострочный установщик clipmgr через apt-репозиторий на GitHub Pages.
# Использование:
#   curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
#
# Идемпотентен: повторный запуск просто обновит ключ/список и переустановит пакет.
# TODO: тексты/брендинг причесать.
set -eu

REPO_URL="https://tihonove.github.io/gnome-clipboard-history-native"
KEYRING="/etc/apt/keyrings/clipmgr.gpg"
LIST="/etc/apt/sources.list.d/clipmgr.list"

if ! command -v apt-get >/dev/null 2>&1; then
    echo "clipmgr: этот установщик рассчитан на apt-дистрибутивы (Ubuntu/Debian)." >&2
    echo "Скачай бинарник вручную: https://github.com/tihonove/gnome-clipboard-history-native/releases" >&2
    exit 1
fi

# sudo только если мы не root
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
    SUDO="sudo"
fi

echo "clipmgr: добавляю ключ репозитория → $KEYRING"
$SUDO install -d -m 0755 /etc/apt/keyrings
curl -fsSL "$REPO_URL/clipmgr-archive-keyring.gpg" | $SUDO tee "$KEYRING" >/dev/null

echo "clipmgr: прописываю источник → $LIST"
echo "deb [signed-by=$KEYRING] $REPO_URL stable main" | $SUDO tee "$LIST" >/dev/null

echo "clipmgr: apt update + установка"
$SUDO apt-get update
$SUDO apt-get install -y clipmgr

echo "clipmgr: готово. Хоткей Super+Ctrl+V настроен, демон запущен."
