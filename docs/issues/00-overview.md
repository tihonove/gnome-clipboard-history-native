# CopyQ postmortem: what we might have missed

This folder is a digest of someone else's experience. We went through the issue
tracker and release history of
[hluk/CopyQ](https://github.com/hluk/CopyQ) (11.9k stars, 87 releases, 2014→2026)
with one goal: **to understand what gnome-clipboard-history-native, given our minimalist
positioning (a `Win+V` equivalent for GNOME/X11), might have missed** — which
pitfalls are inevitable for any X11 clipboard manager, which cheap features we
foolishly skipped, and what to prepare for in advance before images arrive.

CopyQ is a "combine harvester" (scripts, tabs, sync, encryption); we are a single
list at the cursor. We are **not copying** CopyQ; we're harvesting the bugs it has
already paid for.

## How to read this folder

| File | About | Who should read it first |
|------|-------|--------------------------|
| [01-release-history-lessons.md](01-release-history-lessons.md) | 14 recurring themes across 12 years of releases: what got fixed again and again = structural traps of the platform. | The big picture, "why exactly these pitfalls". |
| [02-feature-requests.md](02-feature-requests.md) | The people's wishlist by reactions/comments + a "take / later / out of scope" verdict and **cheap wins**. | What to do next on features. |
| [03-common-bugs-and-pitfalls.md](03-common-bugs-and-pitfalls.md) | 8 classes of bugs inherent to an X11 manager, + a prioritized checklist of preventive measures. | Before writing **clipboard capture** code. |
| [04-image-clipboard.md](04-image-clipboard.md) | Everything about images: MIME/targets, memory, fidelity, thumbnails, serving via INCR, a design sketch. | Before **image support**. |
| [05-x11-wayland-paste.md](05-x11-wayland-paste.md) | Paste/focus/XKB/selection/Wayland: validation of our XTEST+grab approach and a checklist. | On paste and (non-)support for Wayland. |

Each file references concrete `#NNNN` — you can trace back to the original ticket.

## Top takeaways: what we missed / what to prepare for

Cross-cutting priorities that surfaced in several files at once. Ordered by
risk/benefit.

### Prepare before/together with real clipboard capture

1. **Don't store secrets.** As soon as CLIPBOARD monitoring becomes full-featured,
   we — like CopyQ before v8 — will start sucking in passwords from
   KeePassXC/Bitwarden. In-memory saves us from a leak to disk, but the password
   will still hang around in RAM and show up in the popup. Check for the **presence**
   of the `x-kde-passwordManagerHint` target (by presence, not content — lesson
   #2282) + a blacklist by `WM_CLASS`. A few lines — closes a whole class of leaks.
   → 03 §1, 01 §6, 02 item 6.
2. **UTF-8 priority when reading.** `UTF8_STRING`/`text/plain;charset=utf-8` above
   `STRING`/`text/plain`, otherwise mojibake on Cyrillic/umlauts — a bug CopyQ chased
   for years. → 01 §7, 03 §3.
3. **Cleanup on save.** Trim a trailing `\0` (#681) and consider trimming a trailing
   `\n` (#2573) — especially important since we paste into terminals (an extra `\n`
   immediately runs the command). → 03 §3.
4. **Event-driven capture without blocking.** Don't poll in the background; read the
   selection asynchronously, with a size limit, without blocking the GTK thread.
   Re-initialize the subscription after suspend/resume (otherwise detection goes
   "stale" — #181/#1505). *Nuance: right now capture goes through GTK (`owner-change`
   + `WaitForText`), and GTK takes on XFIXES/INCR — but `WaitForText` is synchronous,
   so the size limit and not blocking the thread are still on us.* → 03 §2,4,5.
5. **Don't request `image/*` until image support** and wrap X calls with protection
   from X errors — this at once removes a class of crashes in neighboring apps
   (Gimp/xclip `BadWindow` on INCR of large images). → 03 §4, 04 §1.

### Paste (refine our XTEST path)

6. **A press→release pause of ~30–50 ms** per key (CopyQ default is 50 ms; `0`
   **breaks paste in Chrome**). Don't XSync between press and release. → 05, #1729.
7. **Wait for the modifiers to be released** (the held Super from the hotkey) +
   `XSync`/pause after ungrab before FakeInput — otherwise the synthetic Ctrl+V mixes
   with Super or goes to the wrong window. CopyQ's longest-running pitfall
   (v3.9→v4.0→v10). → 03 §6, 05.
8. **A "WM_CLASS → combination" map, not a boolean "terminal".** Ctrl+Shift+V for
   VTE/kitty/alacritty; **Shift+Insert** for xterm/urxvt; Ctrl+V — the default. There
   is no universal paste shortcut (validated by #2557/#3196). → 05.
9. **Verify the active window** (`_NET_ACTIVE_WINDOW`, captured at `--show`) before
   pasting — don't paste into the wrong window. → 05.

### Popup positioning

10. **Compute the position by the RandR CRTC under the cursor, not by
    `_NET_WORKAREA`** (there's one per desktop → garbage on secondary monitors,
    #3608). Clamp the popup to the visible area of exactly that monitor; account for
    negative coordinates of a left monitor and the bottom edge. → 01 §8, 05.

### Cheap features we foolishly skipped

11. **Search-as-you-type** — the most in-demand and nearly free: we read keys by
    polling anyway, we just need a filter string. **Digits `1`–`9`** for instant
    paste of the Nth entry. **Dedup** "new == top". **A `max-entries` cap**.
    **Pin/favorites** + **clear-all**. All fits on the in-memory list without disk.
    → 02 "Cheap wins".

## Images — keep in view (coming soon)

A separate digest — [04](04-image-clipboard.md). The most important things so as not
to repeat CopyQ's pain:

- **Store the original bytes of the target the source provided; don't
  re-convert** (JPEG silently became BMP — #2185; alpha loss due to BMP above PNG —
  #40). Priority `image/png` > svg > jpeg > tiff/bmp/gif. Decode — **only for the
  thumbnail**.
- **Memory is risk #1** (we're in-memory, we'll hit it first: CopyQ reached 1 GB+):
  compressed originals, lazy thumbnail rendering, a cap on capture size (24–32 MiB)
  and on total image history (LRU).
- **Dedup by `sha256`** of the bytes; fixed row height, `contain` thumbnail.
- **Browsers/Nautilus often don't put an image** — only `text/html`/`text/plain` with
  a link or `text/uri-list`. Don't draw a fake preview; read a local uri-list
  ourselves, don't **download** a remote URL. `data:image/*;base64` in text — decode
  into a real image (#973).
- **Serve as the selection owner**: advertise in `TARGETS` only the formats actually
  stored (#957 — over-advertising breaks other apps' copies); large ones — via
  **INCR** (#2233 cuts at 64 KB). Don't paste images into terminals.

## What our minimalism already got right

CopyQ's history confirms: three things we **deliberately gave up** are its biggest
sources of bugs. Don't give in to the temptation to add them:

- **PRIMARY-selection sync** — the dominant category of CopyQ's X11 bugs (races,
  5-second freezes). We track only CLIPBOARD. → 01 §2, 05.
- **On-disk persistence** — locks, partial file corruption, atomic writes. We're
  in-memory. → 01 §14.
- **A scripting combine** (JS engine, actions, network) — half the releases and a
  huge bug surface. We are a single list. → 01 "ballast".

And our **override-redirect + root-`GrabKeyboard`** architecture structurally sidesteps
a whole class of focus-stealing bugs that CopyQ, being Qt, **fundamentally cannot
handle** (#2960/#2993/#3325): we don't *ask* for focus, we temporarily grab the
keyboard at the X level, and the target window doesn't lose focus. → 05.

## Wayland

Short verdict: **staying X11-only is the right call.** GNOME/mutter gives neither
data-control (reading the clipboard), nor popup positioning, nor XTEST paste, nor
focus — "Wayland support" in CopyQ there is an XWayland crutch. Realistically only a
wlroots backend is possible (`ext-data-control-v1` + `zwlr-layer-shell` + `ydotool`),
as a separate module, not by porting X11 logic. Details — [05](05-x11-wayland-paste.md).
