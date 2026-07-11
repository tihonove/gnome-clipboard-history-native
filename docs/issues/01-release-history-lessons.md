# Lessons from CopyQ's release history

An analysis of every CopyQ release (`hluk/CopyQ`) — from **v2.0.0 (2014)** to **v16.0.0 (2026)**,
roughly 87 releases. The goal is not to copy CopyQ (it's a "kitchen sink"; we are
deliberately minimalist), but to extract what CopyQ learned the hard way over 12 years:
which bugs recur release after release (meaning they are structural platform traps, not
flukes), which features turned out to be mandatory, and which are ballast for our scope.

Our positioning, for context: a clipboard history for **GNOME/X11**, an analog of
`Win+V`, Go + GTK3 + pure X11 (xgb). A resident daemon, a popup at the cursor on
`Super+V`, pasting into the previous window via XTEST. Currently: **text only**,
**in-memory only** (we deliberately don't touch the disk), terminal-awareness when
pasting, layout-independent pasting via a temporary spare keycode. **Coming soon:
image support.**

Notation: version tags, issue numbers `#NNNN`, and feature names are kept as in the
original.

---

## Recurring themes

### 1. Clipboard owner / source-window title
Who "owns" the clipboard and what the source window is called — CopyQ fixed this again and again.

- `v2.4.4` — fix for overriding the mouse selection (X11); `v3.7.2` / `v3.7.3-*` —
  "save the window title immediately on the clipboard-change signal"; `v3.8.0` —
  "correct owner when the window hides right after copying (a password manager copied
  and then hid)"; `v6.0.0` — introduced `change_clipboard_owner_delay_ms`
  (the default delay was increased) because the source manages to close;
  `v8.0.0` — `currentClipboardOwner()` / `currentWindowTitle()`.

**Key lesson:** the source window can vanish in the very same milliseconds as the
copy. If we want to show "where it was copied from", we must capture the active
window's title **at the moment the clipboard-change signal arrives**, not lazily when
showing the popup.

> **for gnome-clipboard-history-native:** relevant, but with a caveat. We currently don't store a source label.
> If we add one — capture `_NET_ACTIVE_WINDOW`/title synchronously in the
> clipboard-change handler. For now — ignore.

### 2. X11 selection quirks / clipboard ↔ primary synchronization
The single "fattest" recurring category across the whole history.

- `v3.3.1` — "don't overwrite a new clipboard with an older selection", "don't clear
  empty clipboard/selection"; `v3.7.0` — "check the clipboard faster and more safely",
  "clipboard first, then selection"; `v3.7.2`/`v6.4.0` — **use the `TIMESTAMP` format
  so as not to re-read an unchanged clipboard**; `v3.9.1` — "stuck clipboard access",
  sync speedup; `v3.10.0` — "don't read the clipboard in parallel in the monitor
  process"; `v3.11.1`, `v5.0.0`, `v6.2.0/6.3.x/6.4.0` — endless fixes for UTF-8
  selection↔clipboard.

**Key lesson:** the selection↔clipboard loop is a source of races and "sticking".
Avoiding re-reading unchanged content (via `TIMESTAMP`) is not an optimization but a
way to avoid hangs and duplicates.

> **for gnome-clipboard-history-native:** partially relevant. We do **not** touch the PRIMARY selection (only
> CLIPBOARD) — and, judging by CopyQ's history, that saves an enormous amount of pain:
> half of their X11 bugs are precisely about syncing the two buffers. **Worth locking
> this in as a conscious decision and not being tempted by "primary too".** But the
> `TIMESTAMP` / "has it changed?" trick before re-reading is worth a **preempt now**
> if we ever poll the clipboard owner.

### 3. Pasting, focus, releasing modifiers
Directly about our XTEST path.

- `v3.9.0` — Windows: "Ctrl/Shift and other modifiers sticking after paste";
  `v3.12.0` — Windows: "paste is deferred until the user releases the hotkey — more
  reliable than releasing keys automatically, and matches the Linux behavior";
  `v4.0.0` — a whole batch of timings: `script_paste_delay_ms` (250ms),
  `window_wait_before_raise_ms`, `window_wait_raised_ms`, `window_wait_after_raised_ms`,
  `window_key_press_time_ms`, **`window_wait_for_modifiers_released_ms`**; `v10.0.0`
  (Linux) — "wait for modifiers to be released when syncing the selection"; `v6.0.1`/
  `v12.0.1` (X11) — registering global shortcuts with modifiers.

**Key lesson:** you can't send a synthetic `Ctrl+V` while the user is still physically
holding the hotkey keys — the modifiers get "mixed in" and pasting breaks. CopyQ
arrived at "wait for modifiers to be released" plus a set of configurable pauses (raise
window → pause → key press → pause). The magic is in the delays around window focus.

> **for gnome-clipboard-history-native:** **relevant / preempt now.** We paste via XTEST into the previous
> window. Mandatory: (1) wait for the physical modifiers `Super`/etc. to be released
> before injection; (2) build in small configurable pauses between focusing the target
> window and `Ctrl(+Shift)+V`. These are exactly the traps CopyQ stepped on for years
> (`v3.9.0`→`v3.12.0`→`v4.0.0`→`v10.0.0`).

### 4. Wayland
Appears in nearly every release from `v4.0.0` through `v16.0.0`
(`v4.0.0, 4.1.0, 6.0.0–6.4.0, 8.0.0, 10.0.0, 12.0.0, 13.0.0, 14.0.0, 15.0.0, 16.0.0`).

Especially telling:
- `v10.0.0` — "on GNOME (Wayland) the monitor and the clipboard provider work through
  **XWayland, because GNOME doesn't support the Wayland data-control protocol**";
- `v12.0.0` — global shortcuts via **Portal**; `v14.0.0` — X11 Portal for shortcuts via
  `COPYQ_USE_PORTAL`; `v13.0.0` — clipboard access in KDE Plasma 6.5 via `KGuiAddons`.

> **for gnome-clipboard-history-native:** ignore for now (we're X11-only and that's written into the
> requirements). But the **strategic takeaway**: GNOME specifically is the most hostile
> to clipboard managers due to the absence of data-control; when Wayland comes up, the
> path is one of two — XWayland (like CopyQ) or a dedicated GNOME extension (see theme
> 12). Keep in mind, don't drag it into the architecture now.

### 5. Performance and OOM on large data
Comes up regularly and is solved ever more strictly.

- `v3.1.1`/`v3.1.2` — "performance for long strings", "eliding huge text"; `v3.9.0` —
  "large images are no longer converted automatically to other formats — it was slow";
  `v8.0.0` — **large items stored separately** (`item_data_threshold`, default 1024
  bytes) for memory's sake; `v9.0.0` — a bunch of improvements for large volumes;
  `v13.0.0` — "filtering large tabs no longer blocks the UI"; **`v16.0.0` —
  `clipboard_mime_size_limit` and OOM protection** (example: `text/html.*:0;.*:100M` —
  don't store HTML, everything else ≤100MiB; default `.*:100M`), plus "avoid crashing
  on receiving very large data from the clipboard".

**Key lesson:** the clipboard can contain tens/hundreds of MB (rich HTML, huge images,
many MIME representations of a single object). Without a size limit and without cutting
off "fat" formats — hangs and OOM. The HTML representation is especially prone to
bloating.

> **for gnome-clipboard-history-native:** **relevant, preempt before images.** We're in-memory and text-only —
> the risk is small right now. But: (1) build in an upper limit on the size of a single
> entry (CopyQ arrived at ~100MiB) and an overall memory budget for the history; (2)
> when we add images — don't store all MIME representations, keep one + a thumbnail;
> (3) don't convert formats "just in case" (`v3.9.0`).

### 6. Sensitive data / passwords
A rare but critical theme that "matured" over time.

- `v2.4.8` — "hide content if `application/x-copyq-hidden` = 1";
- **`v8.0.0`** — "don't touch the clipboard from password managers": ignore if
  `Clipboard Viewer Ignore` (Windows), `application/x-nspasteboard-concealed-type`
  (macOS), **`x-kde-passwordManagerHint` with value `secret` (Linux)** is present;
- `v9.0.0` — "recognize secrets from more applications" (Windows);
- `v9.1.0` — `onSecretClipboardChanged()` + the `mimeSecret=1` format.

**Key lesson:** password managers mark the clipboard with special formats precisely so
that history won't save it. Ignoring these markers = storing passwords in an open
history.

> **for gnome-clipboard-history-native:** **relevant / preempt now (cheap).** On X11/Linux it's enough, when
> reading targets, to check for the presence of `x-kde-passwordManagerHint`=`secret`
> (and, while at it, `application/x-copyq-hidden`, `Clipboard Viewer Ignore`) and **not
> save such an entry**. A few lines of code, and it closes a whole class of leaks.
> Especially important since we keep history **in memory without encryption**.

### 7. Encodings / mojibake / UTF-8
Runs through the entire history.

- `v2.2.0` — "command I/O is UTF-8 only (fixes encoding problems on Windows)"; `v2.4.2`
  — "guess the clipboard encoding better"; `v3.8.0` — "detect the encoding for other
  text formats"; `v4.0.0`/`v7.0.0`/`v12.0.0` — **prefer the format
  `text/plain;charset=utf-8`, with `text/plain` as a fallback**; `v6.3.1`/`v6.4.0` —
  UTF-8 fixes when syncing the selection; `v10.0.0` — "setting UTF-8 text on a broken
  GNOME XWayland"; `v14.0.0` — "read environment variables in the correct encoding".

**Key lesson:** text in the X11 clipboard has several representations; you should take
`text/plain;charset=utf-8` in priority, with `text/plain` (and `STRING`/`UTF8_STRING`)
as a fallback. Otherwise — garbled characters on non-ASCII.

> **for gnome-clipboard-history-native:** **relevant / preempt now.** Explicitly prioritize
> `UTF8_STRING`/`text/plain;charset=utf-8` over `STRING`/`text/plain` when requesting
> targets. Cheap, removes a whole layer of bugs that CopyQ caught for years.

### 8. Window geometry / multi-monitor / positioning
One of the most frequent categories of fixes (close to home for us — we show a popup at
the cursor).

- `v2.4.2, 2.5.0, 2.6.1, 3.0.1–3.0.3, 3.1.2` — "open on the current screen"; `v3.2.0` —
  "menu/window on the left screen (negative coordinates)"; `v3.7.1` — "geometry
  restoration on i3 and with different scaling factors"; `v6.0.0` — **"the window opens
  only within the visible screen area"**; `v6.2.0` — "geometry restoration in a loop"
  (bug); `v6.1.0` — "on many Wayland compositors you can't set the window position";
  `v14.0.0` — `showAt()` with a maximized window.

**Key lesson:** "show at the cursor / at the mouse" breaks at screen edges, negative
coordinates (left/top monitor), and varying DPI. The popup must be clamped into the
visible area of the current monitor.

> **for gnome-clipboard-history-native:** **relevant / preempt now.** Our `popupXY` must: determine the
> monitor under the cursor, clamp the coordinates so the whole list fits (accounting for
> negative coordinates of a left monitor and the bottom edge, where the list "runs off"
> below the cursor). These are exactly the traps of `v3.2.0` and `v6.0.0`.

### 9. High-DPI / fractional scaling
- `v2.4.6, 3.0.1, 3.1.0, 3.2.0` (icons/previews on high-DPI), `v3.9.1` ("window on
  another screen with a different DPI"), `v3.10.0` ("fractional scaling").

> **for gnome-clipboard-history-native:** weakly relevant now (GTK handles the scale itself), will become more
> important with **image thumbnails** — render them accounting for the scale-factor,
> otherwise they'll be blurry.

### 10. Deduplication / merging consecutive identical entries
- `v3.6.0` — "merge the top item with the same new clipboard text", `v2.4.5`/`v8.0.0` —
  "ignore a clipboard change with unchanged content", "update the last text only if the
  start/end matches"; `v6.4.0`/`v14.0.0` — duplicates in synchronized tabs and
  **duplication of a pinned item on clipboard re-read** (`#3131, #3042`).

> **for gnome-clipboard-history-native:** **relevant / preempt now.** Simple deduplication: if the new entry
> == the current top one, don't spawn a duplicate but raise/update the existing one.
> Cheap, noticeably improves the history UX.

### 11. Notifications
- `v4.0.0` — switch to system popups; `v4.1.0` — brought back the old system as an
  option; `v6.0.0` — lowered urgency for frequent clipboard-change notifications;
  **`v7.0.0` — limit the notification text length (~100k characters / 100 lines),
  otherwise it lags the DE**; `v10.0.0` — urgency/persistent.

> **for gnome-clipboard-history-native:** ignore (we have no notifications and probably don't need any — we're
> a "quiet" Win+V). If they appear — take `v7.0.0` to heart: don't cram all the content
> into the notification body.

### 12. Capturing history on GNOME and global shortcuts
- `v14.0.0` — "GNOME: monitor the clipboard via a **dedicated GNOME extension**
  (`#2342, #1243`)"; `v12.0.0`/`v14.0.0` — global shortcuts via **Portal**.

> **for gnome-clipboard-history-native:** ignore for now (we're X11, we hang the hotkey through GNOME itself —
> a gsettings custom keybinding, which precisely works around the impossibility of
> `XGrabKey` on `Super`). But this confirms our note on record: on GNOME, intercepting
> `Super` by the application itself doesn't work — CopyQ went to an extension/Portal, we
> went to gsettings. The decision is correct.

### 13. Crash on exit / logout / session manager
- `v2.4.5` ("blocking system shutdown"), `v3.0.1`/`v3.2.0` (exit on logout), `v6.2.0`
  (Windows logout), `v3.12.0` (don't crash on SIGHUP), **`v12.0.0`** — "correctly
  propagate the exit code on SIGINT/SIGTERM", "don't exit on commit-data from the
  session manager".

> **for gnome-clipboard-history-native:** weakly relevant. We're a daemon; we should handle SIGTERM/SIGINT
> correctly (clean exit, releasing the grab, removing the socket) — we already have this
> partially. Don't block logout.

### 14. Persistence and on-disk data corruption
- `v2.0.0` (sync with files), `v6.2.0` — **"load at least some items from a partially
  corrupted file, discard the rest"**, "safe saving via `QSaveFile`"; `v16.0.0` —
  "handle file operation errors and locking when syncing to disk/share".

> **for gnome-clipboard-history-native:** ignore for now (we're deliberately **in-memory**, we don't touch the
> disk — and CopyQ's history shows how many bugs persistence brings: locks, partial
> corruption, atomic writes). **This is one more argument for our decision not to store
> to disk.** If we ever add it — only atomic writes (temp file + rename, like
> `v3.13.0`/`QSaveFile`) and resilience to corruption.

---

## Notable features by "stickiness"

**Turned out to be important (core of a clipboard manager):**
- **History + search/filter over the list** — present from the very start; filtering
  large lists without blocking the UI matured through `v13.0.0`. For us — core.
- **Pinned / protected items** (`v3.0.0`) and **locked** (`v4.0.0`) — pinned entries
  turned out to be in demand; the subtlety is they must not be lost on history overflow
  (`v12.0.0`: "pinned/locked are not evicted when the limit changes").
- **Indexing rows from 1, not from 0** (`v4.0.0`, `row_index_from_one`) — a trifle, but
  users asked for it. We navigate by keys — we could show numbers 1..9.
- **Ignoring password managers** (`v8.0.0`) — became a de-facto standard.
- **Pasting plain text only on Shift** (`v3.7.0`: "Copy/Paste plain text only when
  Shift is held") — echoes our Ctrl+Shift+V for terminals.
- **Show/hide on the same key** (`v15.0.0`, `#2272`) — a toggle instead of re-opening.
  Cheap and convenient.

**Ballast for our scope (deliberately not taking):**
- **Scripting engine** (JS/ECMAScript, `action()`, `dialog()`, `NetworkRequest`,
  `ItemSelection`, `stats()`, dozens of script functions) — half the releases are about
  it. A huge surface. Not our path.
- **FakeVim / Emacs navigation** (`v6.4.0, v10.0.0, v11.0.0` and constant fixes).
- **Tab encryption / QCA / keychain** (`v3.0.0, v7.1.0, v14.0.0`) — for us, in-memory
  without disk, this is irrelevant (data lives only in the daemon's memory).
- **Synchronize plugin** (files on disk, share) — a source of a large number of bugs
  (`v4.0.0, v6.4.0, v8.0.0, v9.0.0, v9.1.0, v16.0.0`). Not taking it.
- **Tabs/tab groups, themes, One Dark/Nord/Black, audio `playSound()`
  (`v14.0.0`), network requests, notes/tags** — kitchen sink. We are one list.

---

## History related to images (for future support)

All releases where images/thumbnails/formats figure — read before adding image
support:

- **`v2.0.0`** — opening an external image editor (fix on Windows).
- **`v2.8.0`** — "Insert images in editor"; fix for pasting animated images.
- **`v2.8.2`** — drag'n'drop of images into more applications.
- **`v2.8.3`** — support for **animated GIF** (played when selected).
- **`v3.0.3`** — improved **thumbnail** rendering; fix for image sizes.
- **`v3.1.0/3.1.1/3.2.0`** — rendering icons/images on **high-DPI**, blurriness and
  preview sizes.
- **`v3.5.0`** — fix for storing **SVG** and other XML formats together with text.
- **`v3.9.0`** — **"large images are NO longer converted automatically to other
  formats — it was slow and unnecessary, since some usable format already exists"**.
  The most important lesson: don't multiply an image's representations.
- **`v7.1.0`** — on Edit of an image without text, open the **image editor**, not the
  text one.
- **`v8.0.0`** — **"don't insert all image formats as a new entry"** (a clipboard with
  an image carries a pile of MIME — don't take all of them); Wayland: fix for copying
  images between instances.
- **`v11.0.0`** — previews for **`ico` and `webp`**.
- **`v16.0.0`** — `clipboard_mime_size_limit` (you can disable `text/html` entirely,
  limit the size); fix: **thumbnails disappeared for tagged image elements with "Show
  simple items" enabled** (`#3602`).

**The bottom line for our future image support:**
1. An image in the X11 clipboard arrives as **several targets** (`image/png`,
   `image/bmp`, sometimes `text/html` with `<img>`, `text/uri-list`). **Take one
   canonical representation** (preferably `image/png`), **don't store all of them**, and
   **don't convert "in reserve"** (`v3.9.0`, `v8.0.0`).
2. A **size limit** on the entry and on memory is mandatory (`v16.0.0`) — images are the
   first to trigger OOM.
3. The **thumbnail** for the list is rendered separately and accounting for the
   **scale-factor** (`v3.x` high-DPI traps).
4. The `text/html` representation is often bloated — either ignore it when an image is
   present, or limit it hard (`v16.0.0`).
5. Animated GIF and SVG — separate cases; in the first stage we can reduce them to a
   static thumbnail and not drag in full animation (CopyQ fussed with it: `v2.8.0`,
   `v2.8.3`).

---

## What we might miss (top takeaways for gnome-clipboard-history-native)

- **Releasing modifiers before the XTEST paste + pauses around window focus.** CopyQ's
  longest-running traps (`v3.9.0`→`v3.12.0`→`v4.0.0`→`v10.0.0`). Sending `Ctrl+V` while
  the hotkey is held is a broken paste. **preempt now.**
- **Ignoring password-manager markers** (`x-kde-passwordManagerHint=secret` and
  `application/x-copyq-hidden`, `Clipboard Viewer Ignore`). A few lines — closes a
  password leak into unencrypted in-memory history (`v8.0.0`).
- **Priority of `UTF8_STRING`/`text/plain;charset=utf-8`** over `STRING`/`text/plain`
  when reading targets — otherwise mojibake (`v4.0.0`, `v7.0.0`, `v12.0.0`).
- **Clamping the popup into the visible area of the current monitor** (negative
  coordinates of a left/top monitor, the bottom edge) — `v3.2.0`, `v6.0.0`.
- **An upper limit on entry size and an overall memory budget**; don't store all MIME
  representations, especially bloated `text/html`; don't convert formats "in reserve"
  (`v3.9.0`, `v8.0.0`, `v16.0.0`). Critical before images.
- **Dedup "new == top"** — don't spawn identical entries in a row (`v3.6.0`).
- **A "has it changed?" check before re-reading the clipboard** (analog of `TIMESTAMP`)
  — against races/sticking/duplicates (`v3.7.2`, `v6.4.0`).
- **Confirmations of our design.** CopyQ's history shows that the three things we
  **declined** gave it the most bugs: (1) **PRIMARY-selection sync** (theme 2), (2)
  **on-disk persistence** (themes 5/14), (3) **the scripting kitchen sink**. Our
  minimalist "no"s are justified; don't give in to the temptation to add them.
