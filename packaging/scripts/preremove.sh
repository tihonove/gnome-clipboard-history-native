#!/bin/sh
# udev-правило и modules-load — обычные файлы пакета, их удалит сам dpkg. Тут только
# перечитываем udev, чтобы правило перестало действовать сразу. Per-user настройку
# (автозапуск/хоткей) юзер снимает сам через `clipmgr --uninstall` из своей сессии.
set -e

udevadm control --reload-rules 2>/dev/null || true

exit 0
