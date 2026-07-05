#!/bin/sh
# udev-правило (uaccess) пакет положил статически в /usr/lib/udev/rules.d — тут лишь
# активируем его для ТЕКУЩЕЙ сессии: загрузить модуль и перечитать/применить правило.
# Per-user часть (автозапуск, хоткей, старт демона) делает
# `gnome-clipboard-history-native --install` уже в графической сессии — из
# root-postinst это невозможно. `|| true` — чтобы
# отсутствие udev/modprobe в экзотическом окружении не роняло установку пакета.
set -e

modprobe uinput 2>/dev/null || true
udevadm control --reload-rules 2>/dev/null || true
udevadm trigger --subsystem-match=misc --sysname-match=uinput 2>/dev/null || true

echo "gnome-clipboard-history-native установлен. Заверши настройку в своей сессии: gnome-clipboard-history-native --install"

exit 0
