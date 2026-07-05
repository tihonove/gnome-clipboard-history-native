# gnome-clipboard-history-native

Clipboard history for GNOME on X11 & Wayland — native GTK daemon. Pops up a history
list on `Super+Ctrl+V` (like Windows' clipboard history), Enter pastes into the
active window.

## Install

**Via apt (recommended, auto-updates):**

```sh
curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
```

**Standalone binary (no apt):**

```sh
curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install-standalone.sh | sh
```

After installation, run `gnome-clipboard-history-native --install` to register the hotkey (`Super+Ctrl+V`) and enable autostart.
