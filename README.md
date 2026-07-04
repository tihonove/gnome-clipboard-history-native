# clipmgr

**Clipboard history for GNOME — like `Win+V` on Windows.** A small resident GTK
daemon: press a hotkey, pick a previous clipboard entry from a popup, and it's
pasted into the focused window.

[![Release](https://img.shields.io/github/v/release/tihonove/gnome-clipboard-history-native?sort=semver)](https://github.com/tihonove/gnome-clipboard-history-native/releases)
[![CI](https://github.com/tihonove/gnome-clipboard-history-native/actions/workflows/ci.yml/badge.svg)](https://github.com/tihonove/gnome-clipboard-history-native/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

Works on **GNOME under both X11 and Wayland**, single binary, no external tools
at runtime.

## Features

- **`Super+Ctrl+V`** pops up your clipboard history at the cursor (X11) or centered
  (Wayland).
- Arrow keys / `PageUp` / `PageDown` / `Home` / `End` to navigate, `Enter` to paste,
  `Esc` to close.
- Pastes into the **focused window** — GUI apps and terminals alike.
- History captured **in-process** (no `xsel`/`xdotool` spawning), themed with **Yaru**.
- One binary, two backends chosen at runtime; distributed as a **signed apt
  repository** with automatic updates.

## Install

### apt (recommended — automatic updates)

```sh
curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install.sh | sh
```

Adds the signed apt repository and installs `clipmgr`. Updates then arrive via
`apt upgrade` like any other package.

### Standalone (no apt)

```sh
curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/install-standalone.sh | sh
```

Downloads the binary from the latest release and sets it up. No package manager
integration, so re-run the script to update.

### Manual (apt)

```sh
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://tihonove.github.io/gnome-clipboard-history-native/clipmgr-archive-keyring.gpg \
  | sudo tee /etc/apt/keyrings/clipmgr.gpg >/dev/null
echo "deb [signed-by=/etc/apt/keyrings/clipmgr.gpg] https://tihonove.github.io/gnome-clipboard-history-native stable main" \
  | sudo tee /etc/apt/sources.list.d/clipmgr.list >/dev/null
sudo apt update && sudo apt install clipmgr
```

Or grab the `.deb` / raw binary straight from the
[releases page](https://github.com/tihonove/gnome-clipboard-history-native/releases).

After installing, run once in your session to register the hotkey and start the
daemon (the apt/standalone scripts do this for you):

```sh
clipmgr --install
```

## Usage

| Key | Action |
| --- | --- |
| `Super+Ctrl+V` | Open the clipboard history popup |
| `↑` / `↓` | Move selection |
| `PageUp` / `PageDown` / `Home` / `End` | Jump around the list |
| `Enter` | Paste the selected entry into the focused window |
| `Esc` | Close |

CLI:

```
clipmgr --install         register the Super+Ctrl+V hotkey + autostart, start the daemon
clipmgr --uninstall       remove hotkey/autostart, stop the daemon
clipmgr --setup-input     one-time /dev/uinput access for pasting on Wayland
clipmgr --show            open the popup (what the hotkey runs)
clipmgr --version
```

## Requirements

- **GNOME** on X11 or Wayland.
- **GTK 3** runtime (`libgtk-3-0`).
- On **Wayland**: access to `/dev/uinput` for synthetic paste (set up automatically
  via a `uaccess` udev rule shipped by the package) and **XWayland** for reading
  clipboard history.

> **Keyboard layouts:** if you use more than one layout, configure switching via
> **GNOME Tweaks** (not Settings) — otherwise modifiers get swallowed and the
> hotkey/paste can break on the second layout.

## How it works

- **X11** — the popup is an override-redirect window positioned at the cursor; the
  keyboard is grabbed via `xgb` (GNOME steals focus from popups), and paste is a
  native `XTEST` keystroke using a spare keycode remapped to `v` in every layout
  group.
- **Wayland** — the popup is a normal toplevel that receives focus and uses regular
  GTK signals; paste goes through a virtual keyboard on `/dev/uinput`
  (`Shift+Insert`), and history is read from the X11 `CLIPBOARD` that mutter mirrors
  into XWayland.

See [ARCHITECTURE.md](./ARCHITECTURE.md) for the details.

## Build from source

Requires **Go 1.23+**, **cgo**, and `libgtk-3-dev`:

```sh
sudo apt install -y libgtk-3-dev
go build -o clipmgr ./cmd/clipmgr
```

## Uninstall

```sh
sudo apt remove clipmgr      # if installed via apt
clipmgr --uninstall          # remove the per-user hotkey/autostart
```

## License

[MIT](./LICENSE)
