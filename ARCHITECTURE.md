# Architecture

This document describes how `gnome-clipboard-history-native` is built and why these particular
decisions were made. Many of them are consequences of X11/GNOME limitations, not
free choices.

## Overview

A single resident daemon application in Go that:
1. listens on a unix socket and, on command, shows a popup list at the cursor;
2. draws the list with **GTK** (to get the native Yaru theme for free);
3. reads keys and performs the paste with **xgb** (low-level X11), because the GTK
   path doesn't work for our kind of window (see below).

The "client" (`gnome-clipboard-history-native --show`) is a thin "call" process launched by the
GNOME hotkey; it just wakes the daemon over the socket and exits.

## Control flow

```
[Super+B]  (GNOME custom keybinding)
    │  runs the command
    ▼
gnome-clipboard-history-native --show ──unix-socket──►  daemon: socket listener (goroutine)
                                     │  glib.IdleAdd (into the GTK thread)
                                     ▼
                                 showPopup():
                                   • ewmh.ActiveWindowGet → target window
                                   • build the GTK window (POPUP) + list
                                   • position it (popupXY)
                                   • GrabKeyboard on root (with retries)
                                   • start key polling (glib.TimeoutAdd 8ms)
                                     │
                     Up/Down/PageUp/PageDown/Home/End → setSel → updateSelection
                     Enter → finish(true) → paste the selected entry
                     Escape / 15s timeout → finish(false)
                                     │
                                     ▼
                                 finish():
                                   • Ungrab, destroy the window
                                   • (paste) clip.SetText + pasteInto(term)
```

## Components

- **Daemon** (`runDaemon`): `gtk.Main()`. Single instance (a socket `Dial` check
  before `Listen`). Holds an `xgbutil` X connection for xgb operations and GTK for
  the UI.
- **Client** (`runClient`): `net.Dial` to the socket, sends `show`, exits.
- **Socket**: `$XDG_RUNTIME_DIR/gnome-clipboard-history-native.sock`. Listened on in a goroutine;
  showing the window is marshaled into the GTK thread via `glib.IdleAdd` (xgb/GTK
  aren't thread-safe).

## The GTK + xgb hybrid — why

GTK gives the native theme (Yaru), CSS, fonts, a list — everything "as in the
system". But:

- The popup window is a `GTK_WINDOW_POPUP` (override-redirect): frameless, exactly
  at a point, outside WM control.
- GNOME **doesn't give keyboard focus** to such a "self-popped-up" window
  (focus-stealing prevention), so GTK key handlers don't fire.
- So we take input around GTK: `xproto.GrabKeyboard` on **root** (owner_events =
  false → all keys come to us), and we read events by polling
  `X.Conn().PollForEvent()` in `glib.TimeoutAdd(8ms)`. We move the list selection
  manually (`listBox.SelectRow` + scroll).
- Side benefit: since we never took X focus away from the target window (grab ≠
  SetInputFocus), after Ungrab the paste flies straight into the target — without
  focus "fixup" and delays.

## Hotkey — why through GNOME

Under GNOME/mutter an application **cannot** intercept `Super+<key>` via XGrabKey:
mutter holds Super itself. So the key is bound by GNOME itself (a gsettings custom
keybinding) to launch `gnome-clipboard-history-native --show`. This is also why we need a "client"
call rather than self-interception in the daemon.

## Positioning (`popupXY`)

- If the mouse cursor is **inside** the active window — the popup is at the mouse.
- Otherwise — at the **center** of the active window.
- Window geometry: `GetGeometry` (size) + `TranslateCoordinates` (absolute position;
  it can't be taken directly from GetGeometry — the WM reparents the window with a
  frame).
- Finally — clamp to the screen bounds.

## Paste (`finish` → `pasteInto`)

1. Clipboard ownership: `clip.SetText` — the daemon is alive, so it serves the
   subsequent paste request itself. No external `xsel` needed.
2. The target window is determined ahead of time (`ewmh.ActiveWindowGet` at show).
3. **Layout independence** (the least obvious part; details — in the comment above
   `setupSpareKey`). You can't send the real keycode for 'v': in the Russian group
   that key produces Cyrillic → you get "м". We keep a **spare unused keycode**,
   mapped to 'v'/'V' in **all** groups, and send it — then in any layout it's 'v'.
   The real keycode also won't do because terminals (kitty) manage the active group
   themselves.
   - We map it (`setupSpareKey`) when the popup **opens** — so apps have time to
     asynchronously re-read the keymap before the keypress; we restore it to NoSymbol
     (`restoreSpareKey`) ~300 ms after it closes.
   - **Why temporarily, not permanently:** if 'v' hangs permanently on the spare
     keycode, mutter resolves `Super+V` to it (in the Russian layout 'v' exists
     nowhere else) → physical Super+V stops opening the popup. While the popup is
     open, the keyboard is grabbed anyway, so Super+V isn't needed.
   - **Why a delayed restore:** Qt/Electron read the event asynchronously; if you
     restore the mapping immediately, they'll see NoSymbol and won't paste.
   - We take `keysymsPerKeycode` from the server, otherwise the group offset breaks.
4. **Terminal-aware:** by the target window's `WM_CLASS` (`icccm.WmClassGet`) we send
   **Ctrl+Shift+V** to terminals (kitty/gnome-terminal/konsole/foot/alacritty/
   ghostty/…), and **Ctrl+V** to everyone else.
5. Injection — native **XTEST** (`xtest.FakeInput`), without spawning external
   processes (spawning `xdotool` gave a visible delay, especially in the Russian
   layout, where it has to remap itself).

## Appearance

- The Yaru theme is picked up automatically (GTK reads `gtk-theme`).
- Custom CSS (via `CssProvider` with USER priority) on top of the theme: a rounded
  "frame" wrapper (RGBA-transparent window → rounded corners + shadow, no control
  buttons), an accent **outline** on the selected row in the style of Nautilus file
  focus.
- Each entry is **exactly 3 lines**: long ones are truncated (`displayText`, no wrap
  + ellipsize on the right), short ones are padded with empty lines. The list height
  is ~3.5 entries (a hint of scrolling).

## Dependencies

- `github.com/gotk3/gotk3` **v0.6.3** — Go bindings for GTK3/GDK/GLib/Pango (cgo). Not
  v0.6.4: it has a missing import in `gdk`, doesn't build.
- `github.com/jezek/xgb` + `github.com/jezek/xgbutil` — pure X11: grab, XTEST, EWMH
  (active window), ICCCM (WM_CLASS), keybind (keycodes), event poll.

## Installation and releases

- **Installation** is built into the binary: `gnome-clipboard-history-native --install` writes
  autostart (`~/.config/autostart/gnome-clipboard-history-native.desktop` pointing at the binary path)
  and the `Super+Ctrl+V` hotkey (gsettings, in a free slot, without touching others;
  a repeated `--install` rebinds its own slot), and starts the daemon; idempotent.
  `--uninstall` — removes everything and stops the daemon (the `quit` socket command).
  The idea is to distribute a single binary; the user drops it in and runs
  `--install`.
- **Releases** — GitHub Actions (`.github/workflows/`): `bump-version` (manual, bumps
  `VERSION` + tag) → `release` (build via `build.yml` + git-cliff changelog + GitHub
  Release). The commit convention and process — in `CLAUDE.md`.

## Current limitations / TODO

- **Text** only, **in memory** only (no disk — intentional). No images.
- **Wayland (GNOME): backend** (`cmd/gnome-clipboard-history-native/wayland.go` + `internal/uinput`) —
  centered popup (a regular toplevel + `focus-out`), `Shift+Insert` paste via
  `/dev/uinput`. Works in Chrome (native Wayland), Telegram (XWayland) and the
  console, in both layouts. **History — via an XWayland bridge:** the background wl
  path can't see another app's clipboard (mutter doesn't grant `data-control`), but
  mutter mirrors the clipboard into the X11 CLIPBOARD, and we catch XFIXES
  notifications over XWayland and read the selection in-process via xgb (no external
  utilities — like CopyQ) — fast copies aren't lost (event-driven). Requires only
  XWayland (`$DISPLAY`). `wl-paste --watch` won't do (it requires wlroots
  data-control). At-cursor positioning under Wayland is impossible by design. The
  X11 path is full-featured (at the cursor, XTEST, history via GTK owner-change).
- No search over history, no settings, no placeholder icon.
- Build only `linux-x64` (arm64 — add to the build.yml matrix if desired).
