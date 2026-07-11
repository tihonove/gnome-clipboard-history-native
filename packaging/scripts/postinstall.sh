#!/bin/sh
# The package installed the udev rule (uaccess) statically in /usr/lib/udev/rules.d — here we only
# activate it for the CURRENT session: load the module and reload/apply the rule.
# The per-user part (autostart, hotkey, starting the daemon) is done by
# `gnome-clipboard-history-native --install` in the graphical session — from
# a root postinst that's impossible. `|| true` — so that
# a missing udev/modprobe in an exotic environment doesn't fail the package install.
set -e

modprobe uinput 2>/dev/null || true
udevadm control --reload-rules 2>/dev/null || true
udevadm trigger --subsystem-match=misc --sysname-match=uinput 2>/dev/null || true

echo "gnome-clipboard-history-native installed. Finish setup in your session: gnome-clipboard-history-native --install"

exit 0
