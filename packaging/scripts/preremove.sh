#!/bin/sh
# Симметрично postinst: снять СИСТЕМНУЮ настройку (udev-правило, modules-load,
# системный автозапуск). Per-user хоткей остаётся у юзера — он безвреден (указывает
# на удаляемый бинарник) и убирается через `clipmgr --uninstall` из своей сессии.
set -e

if [ -x /usr/bin/clipmgr ]; then
    /usr/bin/clipmgr --uninstall-system || true
fi

exit 0
