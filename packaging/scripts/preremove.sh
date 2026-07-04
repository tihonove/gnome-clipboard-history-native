#!/bin/sh
# Симметрично postinst: убрать автозапуск/хоткей и остановить демона перед удалением.
set -e

if [ -x /usr/bin/clipmgr ]; then
    /usr/bin/clipmgr --uninstall || true
fi

exit 0
