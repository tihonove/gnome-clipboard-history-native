#!/bin/sh
# The udev rule and modules-load are ordinary package files; dpkg removes them itself. Here we
# only reload udev so the rule stops taking effect immediately. The per-user setup
# (autostart/hotkey) is removed by the user via `gnome-clipboard-history-native --uninstall` from their session.
set -e

udevadm control --reload-rules 2>/dev/null || true

exit 0
