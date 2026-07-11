# Common bugs and pitfalls of clipboard managers (lessons from CopyQ)

An analysis of the most instructive [CopyQ](https://github.com/hluk/CopyQ) bugs from
the standpoint of which of them are an *inherent property of any X11 clipboard manager*
and therefore threaten `gnome-clipboard-history-native` too. For each class: what broke in CopyQ
(with issue numbers), the root cause in X11/clipboard mechanics, an assessment of the
threat to our architecture (the daemon owns CLIPBOARD while alive, history only in
memory, paste via XTEST), and concrete preventive measures.

> Important context: at the time of writing `gnome-clipboard-history-native` **still has no history
> capture** — there's demo data. That means the most dangerous pitfalls (intercepting
> passwords, reading another app's CLIPBOARD, INCR, detection races) are about **the
> buffer-monitoring code that is yet to be written**. It's cheaper to lay them out
> correctly from the start than to fix them later.

---

## 1. Leakage of sensitive data / password managers

The most dangerous class. A clipboard manager by definition intercepts **everything**
that gets copied, including passwords from KeePassXC, Bitwarden, browser managers.

### What broke in CopyQ

- **#2495, #2802, #1744, #561, #1068** — CopyQ by default **saved passwords** from
  KeePassXC. Out of the box it doesn't ignore password managers — the user has to add a
  ready-made command themselves (`KeePassXC Protector`, `Ignore *Password* window`),
  otherwise the password settles into history and (in CopyQ) **is written to disk**.
- **#2495** — KeePassXC clears the buffer after N seconds, but in CopyQ a copy has
  already settled into history; auto-clearing the source doesn't remove it.
- **#2679 / #2680** (fixed in v8/v9) — CopyQ learned to recognize "secret" formats and
  not store such a buffer. The list of markers that applications set:
  - X11/KDE: `x-kde-passwordManagerHint` (set by KeePassXC on Wayland/KDE);
  - Windows: `Clipboard Viewer Ignore`, `ExcludeClipboardContentFromMonitorProcessing`,
    `CanIncludeInClipboardHistory` (=0), `CanUploadToCloudClipboard`.
- **#2282** — a subtlety: the format marker `Clipboard Viewer Ignore` sometimes has
  **zero size** (0 bytes), and a naive "is there data in the format" check doesn't see
  it → the password is saved anyway. You need to check the **presence of the format**,
  not its content.
- **#2787** — the reverse trouble: "secrecy" detection turned out too aggressive
  (Chrome marks *everything* from incognito as secret, including URLs) → it broke a
  legitimate scenario. CopyQ added an opt-in `onSecretClipboardChanged` so that such
  data can be handled if desired.
- **#1900** — during screen sharing the content is visible in the list; a request to be
  able to hide/mask the content of individual entries.
- **#2746, #308, #1063** — requests for "clear the buffer on a timeout" and "clear on
  exit".

### Root cause

X11 doesn't distinguish "ordinary" and "secret" buffers. The source application can
only *hint* with an extra MIME target (`x-kde-passwordManagerHint` and analogues), but
this is a voluntary convention. A manager that blindly grabs `text/plain` will
guaranteed suck in passwords too.

### Does it threaten gnome-clipboard-history-native? — **YES (the main threat), but partly mitigated by the architecture**

- As soon as CLIPBOARD monitoring appears, `gnome-clipboard-history-native` **will start intercepting
  passwords** exactly like CopyQ before v8. Our plus — **history only in memory**,
  nothing leaks to disk (this removes CopyQ's heaviest risk — passwords in a
  `.dat`/sync folder). But the password will still hang in RAM, show up in the popup,
  and outlive the source's auto-clear.

### What to do preventively

- **From day one of capture** check for the presence of the `x-kde-passwordManagerHint`
  target (the only marker that actually occurs on Linux/X11) — and if it's present, **do
  not save the entry**. Check specifically for the **presence of the target** in
  `TARGETS`, not its content (lesson #2282: a 0-byte marker).
- Keep the **list of excluded formats** extensible (for the future — KDE/GNOME markers
  that might appear).
- Provide a **blacklist of applications by the owner window's `WM_CLASS`** (KeePassXC,
  Bitwarden, 1Password) as a second line of defense — in case a manager doesn't set the
  hint (lesson: the hint isn't always there, #2282/#1744).
- Don't overdo the "secrecy" heuristic (lesson #2787): if you add aggressive rules, make
  them **toggleable**.
- Since history is in memory and capped at 100 entries — the password will evict itself,
  but it's worth providing a manual "delete entry" and, possibly, "clear history".

---

## 2. Buffer ownership and content loss (clipboard ownership/loss)

### What broke in CopyQ

- **#1413** — "Clipboard cleared when closing source window (Ubuntu/GNOME)": you copied
  text, **closed the source window — the entry vanished** from the live buffer.
  Manifests only with CopyQ running (at that moment it's focused on detection).
  Maintainer: "this is expected — on Linux/X11 the buffer is owned and served by the
  application itself." Users reasonably object: **preserving content when the source
  closes is a basic feature** of a manager (parcellite can do "Restore Empty").
- **#181, #1505** — "Change of clipboard won't be detected after some time": buffer
  change detection **stops firing** after hours of running or after suspend/resume; only
  a restart helps, and even `copyq exit && copyq` doesn't always.
- **#1186** — "X selection and clipboard out of sync": PRIMARY (mouse selection) and
  CLIPBOARD diverge; plus rare **5-second freezes** surfaced when reading the selection
  (buffer-access timeouts).
- **#3276, #2847** — on newer KDE/GNOME the buffer content "isn't held" by the manager.
- **#3463** — a phantom notification: when the application **closes**, CopyQ shows a
  notification with the *last text copied from it* (a reaction to the selection
  ownership change).
- **#3304** — when the KDE lock screen activates, an empty `<Empty>` item gets into
  history (a buffer ownership-change event without useful data).

### Root cause

The key X11 mechanic: **CLIPBOARD is not a store but the "current owner"**. The data
physically lives in the owner application's memory and is served on request
(`SelectionRequest`). The owner died — the data is gone, the buffer is empty. For
content to outlive closing the source, the manager must **become the owner itself**
(take over the selection) and then serve `SelectionRequest`. Detecting "something was
copied" on X11 is done via **XFIXES `SelectionNotify`** (event-driven) or, in older
ones, by polling — and polling hangs/misfires (#181, #1131).

### Does it threaten gnome-clipboard-history-native? — **PARTLY**

- For **pasting the selected item** we're already right: `clip.SetText` makes the daemon
  the owner, and while the daemon is alive it serves `SelectionRequest` itself, the copy
  in memory. An external `xsel` isn't needed. This is the cure for #1413 as applied to
  our entries.
- But **on the capture side** #1413 is still relevant for the *live* buffer: if we
  merely "peek" at CLIPBOARD without taking ownership, then after the source closes
  **before we managed to read the data**, the entry is lost. And conversely — if we
  immediately aggressively take ownership on every foreign copy, we can break
  inter-application exchange and get #3463-like artifacts.
- Detection "stops working after a while" (#181/#1505) threatens if we do capture by
  polling or incorrectly reinitialize XFIXES after suspend.

### What to do preventively

- **Capture event-driven via XFIXES** (`XFixesSelectSelectionInput` +
  `XFixesSetSelectionOwnerNotifyMask`), not by polling. This also removes the #1131
  class.
- On an ownership-change event — **immediately read the data asynchronously**
  (`ConvertSelection` → wait for `SelectionNotify`) and **put a copy in memory**. Then
  closing the source (#1413) doesn't wreck history: we already have a copy.
- When the user **selects an entry to paste** we already become the owner (`SetText`) —
  keep this as an invariant: the live buffer after our paste outlives the death of any
  source.
- Ignore **empty** ownership-change notifications (lesson #3304): don't create an entry
  if `TARGETS` contains no text targets or the data is empty.
- **Reinitialize X subscriptions after suspend/resume** and on X-connection errors
  (lesson #181/#1505: detection "dies" and doesn't revive even on a process restart —
  meaning the cause is an unrestored subscription/connection).
- Don't chase PRIMARY↔CLIPBOARD synchronization (#1186) — it's a separate source of
  freezes and desync; for a "Win+V" analogue **CLIPBOARD alone** is enough.

---

## 3. Loss of formats, encodings, "junk" characters

### What broke in CopyQ

- **#158** — German umlauts / Czech characters broke into mojibake: the data came in the
  **system encoding** (`toLocal8Bit`), while the buffer expects **UTF-8**. Plus the
  "did the buffer change" comparison broke because of the re-encoding.
- **#681** — "invisible character added at the front": an entry has a **null byte (NUL)**
  at the end, which some applications (Photoshop, Chromium) paste as a visible
  character. The source itself puts `\0` in `text/plain`.
- **#2573** — a **trailing newline** (`\n`): you copied a command with a newline → on
  paste into a terminal it executes immediately. A request to be able to trim the
  trailing `\n`.
- **#1186** — multi-line text: selection and clipboard diverge precisely on multi-line /
  trailing-`\n` text.
- **#1304** — when reordering an entry the **rich text/style is lost**: a dozen formats
  are copied from LibreOffice (`text/html`, `text/rtf`, `text/richtext`, ODF…), while
  the manager keeps only `text/plain` + `text/html` → pasting breaks the document's
  style.
- **#2084** — images from browsers are saved **as text** (the browser hands over both
  `text/html` with a link and `image/*`; the wrong target is chosen).

### Root cause

CLIPBOARD hands over **several targets** at once (`text/plain`,
`text/plain;charset=utf-8`, `text/html`, `UTF8_STRING`, `STRING`, `COMPOUND_TEXT`,
app-specific MIME). The right target choice and correct encoding are on the manager.
`STRING` is latin-1, `UTF8_STRING`/`text/plain;charset=utf-8` are UTF-8; mix them →
mojibake. Plus the source is free to put a NUL and trailing newlines into the data.

### Does it threaten gnome-clipboard-history-native? — **PARTLY**

- We are **text-only and in Go** (strings are UTF-8) + GTK `SetText` expects UTF-8, so
  the basic #158 risk is low — but **only if on capture we request
  `UTF8_STRING`/`text/plain;charset=utf-8`, not `STRING`**.
- Rich-text loss (#1304) is **by design** for us — we store only text. This is a
  deliberate choice, but keep in mind: paste always goes **as plain text**, the style
  isn't preserved. It's more of a feature (terminal-safe), but it shouldn't surprise the
  user.
- NUL (#681) and a trailing `\n` (#2573) threaten directly: we hand the text to an XTEST
  paste, and a trailing `\n` in a terminal **executes the command immediately**.

### What to do preventively

- On capture **request targets in order of preference**: `UTF8_STRING` /
  `text/plain;charset=utf-8` → `text/plain` → `STRING`. Never blindly treat
  `STRING` data as UTF-8.
- **Trim the trailing `\0`** (lesson #681) and **validate UTF-8** on save (drop/repair
  broken sequences).
- Consider optional trimming of **trailing `\n`** (lesson #2573) — especially valuable
  since we paste into terminals; at a minimum don't add our own newline.
- Request only text targets; **don't try** to pull `image/*` as text (lesson #2084) —
  when images arrive, make a separate branch.

---

## 4. Performance and hangs on large entries (+ INCR / large images)

### What broke in CopyQ

- **#3096** — when copying large text CopyQ loaded 1 core at 100% for a long time and
  ate 1.7 GB RAM, then a popup error and a duplicate process.
- **#1615** — copying **0.5 MB of text** gave a **10–20 second delay** before reacting;
  the logs show `QtWarning: Retrying to obtain clipboard` (Qt retries to grab the large
  buffer; see QTBUG-97930, QTBUG-130316).
- **#1070** — `xclip`, writing a **large image** to CLIPBOARD while CopyQ is running,
  crashes with `X Error: BadWindow ... X_ChangeProperty`. The cause — the manager reads
  the selection via INCR/property, and by that moment the requestor destroys the
  window/property.
- **#636** — copying a large image from **Gimp crashes Gimp itself** (the same
  `BadWindow` on `X_ChangeProperty`). Partly a Gimp/Qt bug, but **triggered precisely by
  the presence of a manager** that reaches in to read the selection. The only reliable
  workaround is **not to read `image/*` at all**.

### Root cause

Large selections are transferred via the **INCR** protocol — in chunks, through a
property and `PropertyNotify` exchange. If the manager requests the data
synchronously/greedily, it (a) blocks on megabytes and (b) provokes X errors when the
source has already freed the resources. Synchronous reading on every copy = freezes and
BadWindow crashes of neighbors.

### Does it threaten gnome-clipboard-history-native? — **PARTLY**

- We are text-only, so the nastiest scenario (#636/#1070 — large images, Gimp/xclip
  crash) **avoids itself if we fundamentally don't request `image/*`**.
- But large **text** (#3096/#1615) threatens: a greedy synchronous `ConvertSelection` on
  megabyte text will hang the GTK thread (and all our X code is in the main GTK thread!),
  and the popup will start lagging.

### What to do preventively

- Read the selection **asynchronously and non-blocking**: `ConvertSelection` + waiting
  for a `SelectionNotify` event, **not** spinning a synchronous loop in the GTK thread.
- Support/carefully handle **INCR** (data arrives in chunks via `PropertyNotify`); if
  not ready — at least **set a size limit** and silently ignore entries larger than it
  (e.g. > 1–5 MB), so as not to hang (lesson #1615).
- **Wrap all X calls in X-error protection** (a BadWindow on a vanished
  requestor/window must not crash the daemon) — lesson #636/#1070.
- **Don't request `image/*`** at all while there's no image support — this switches off
  a whole class of neighboring-application crashes at once.
- Limit the length we show in the popup (we already have 3 lines/ellipsize) — but
  store/hand over the full text.

---

## 5. High CPU and polling

### What broke in CopyQ

- **#1131** — CopyQ spiked to ~1.5 cores every 5 seconds: the sync plugin **polls a
  folder** every 5 s (`QFileSystemWatcher` didn't start). Workaround — increase the
  interval / read only the active tab.
- **#1287, #1382** — high CPU in `gnome-shell` when interacting with CopyQ; general
  "slowness".

### Root cause

Polling instead of events (file or keyboard) gives background load and battery drain;
same for buffer detection — old managers polled the selection.

### Does it threaten gnome-clipboard-history-native? — **NO / low**

- We poll keys with `glib.TimeoutAdd(8ms)` **only while the popup is open** (a
  short-lived window); in the background the daemon spins nothing. We don't touch the
  disk at all (no persistence) — #1131 doesn't apply to us.

### What to do preventively

- Keep the invariant: **zero polling in the background**. Do buffer capture
  **event-driven via XFIXES**, not on a timer (see class 2). If a fallback poll is ever
  needed — the interval should be seconds, not milliseconds.

---

## 6. Focus stealing, paste into the wrong window, paste races

### What broke in CopyQ

- **#1601** — paste from the tray menu fired ~1 time out of 10: under GNOME after
  closing the menu **the target window isn't focused yet**, CopyQ pastes too early. A
  `usleep(50ms)` (or `XSync`) before injection helped; `_NET_ACTIVE_WINDOW` shows a
  window that in reality doesn't have input yet.
- **#1729** — on GNOME/Ubuntu the paste **duplicates** and hangs: the application "gets
  stuck" while simulating Shift+Insert (apparently because of the manager window's
  minimize animation). They fiddled with `window_wait_after_raised_ms`,
  `window_key_press_time_ms`.
- **#674, #2136** — the paste goes to the **wrong/previous** window.
- **#3428** — if the user **holds a modifier** (didn't release the hotkey), simulating
  Ctrl+C/Ctrl+V **fails** ("Failed to copy"): CopyQ waits up to 2 s for modifiers to be
  released, otherwise it sends the wrong shortcut. It used to force-release the
  modifiers — considered a bad solution.

### Root cause

The XTEST injection is "blind": it sends keys **to wherever the input focus is now**. If
the target window hasn't received focus yet (WM animations, `_NET_ACTIVE_WINDOW` delays)
or the user is **still holding the hotkey modifier** — the synthetic shortcut goes to
the wrong place, duplicates, or is interpreted with an extra modifier.

### Does it threaten gnome-clipboard-history-native? — **PARTLY (but architecturally we're in a better position)**

- We **deliberately don't take X focus** away from the target window (grab ≠
  SetInputFocus) and **don't raise/animate** our window — the target window keeps focus
  the whole time, and after Ungrab the paste flies straight to the target. This removes
  most of #1601/#1729/#674.
- But the **"Ungrab → inject" race** is still possible: XTEST may go before input has
  really returned to the target window. And **#3428 threatens directly**: our hotkey is
  `Super+B` via GNOME — if the user **holds Super** at the moment of Enter, the XTEST
  Ctrl+V will go together with the held Super → mutter/the application will read it as a
  different shortcut or swallow it.

### What to do preventively

- After `UngrabKeyboard`, before the XTEST injection, do an **`XSync` and a small pause**
  (lesson #1601: ~50ms/`XSync` radically raised reliability), giving input time to
  return to the target window.
- **Wait for the modifiers to be released** (Super/Ctrl/Shift/Alt) before injection,
  with a timeout (lesson #3428) — otherwise the synthetic Ctrl+V mixes with the held
  Super. At a minimum — don't send the paste while `XQueryPointer`/keymap show a held
  Super.
- Check that the target window (captured with `ewmh.ActiveWindowGet` at popup **show**)
  still exists; if it vanished — don't paste blindly (lesson #674/#2136).
- Keep the invariant "**don't raise our window, don't take focus**" — it's our protection
  against #1729; don't regress toward an ordinary WM window.

---

## 7. Races on fast copying and detection "going stale"

### What broke in CopyQ

- **#181 / #1505** — detection of new copies **goes silent** over time / after suspend
  (see class 2). A frequent cause — the "did the buffer change" comparison breaks on
  encoding/large data (#158: "content comparison doesn't converge"), and the manager
  decides that "nothing changed" (`Clipboard unchanged`), even though it did.
- **#3428** — fast/held modifiers break copying (see class 6).

### Root cause

On frequent consecutive copies the XFIXES/`SelectionNotify` events come in a stream; a
naive dedup "compare with the previous text" can (a) mute a real new copy (a false
"unchanged"), (b) catch half-read INCR data.

### Does it threaten gnome-clipboard-history-native? — **PARTLY**

- We have dedup + prepend + cap 100 — that's right. But the dedup must rest on
  **fully read** data, otherwise on fast copying/INCR an entry can get glued together or
  lost.

### What to do preventively

- Dedup **on the fact of a completed read** of the entry, not on partial data.
- Serialize the handling of XFIXES events (one selection read at a time), so that a burst
  of copies doesn't overlap `ConvertSelection` calls.
- Don't conclude "unchanged" from a fragile string comparison with potentially different
  encoding (lesson #158) — compare normalized UTF-8.

---

## 8. Memory growth

### What broke in CopyQ

- **#1928, #3247, #1952** — "CopyQ too high RAM consumption": a large history + large
  entries (especially images) bloat RAM.

### Root cause

Unbounded history + storing large items in memory without a ceiling.

### Does it threaten gnome-clipboard-history-native? — **NO / low**

- A hard **cap of 100 entries**, text only, in memory only. The risk exists only if a
  single entry is huge (megabyte text × 100).

### What to do preventively

- Keep the cap of 100 as an invariant; add a **limit on the size of a single entry** (see
  class 4) — then the upper bound on memory is predictable.

---

## Preventive measures for gnome-clipboard-history-native (prioritized checklist)

Order — by importance/risk. Lay out the first five **before/together with** the buffer
capture code.

1. **Don't store secrets.** On capture check for the presence of the
   `x-kde-passwordManagerHint` target (by presence, not content — #2282) and skip such
   entries. Plus a blacklist by `WM_CLASS` (KeePassXC/Bitwarden/1Password). The format
   list — extensible. (#2495, #2679, #2802)
2. **Capture event-driven via XFIXES, not by polling.** Kills at once the detection
   staleness (#181/#1505) and background CPU (#1131). Reinitialize the subscription after
   suspend and X-connection errors.
3. **Read the selection asynchronously and non-blocking, with a size limit.** Don't block
   the GTK thread; support/fence off INCR; silently ignore entries larger than N MB.
   (#1615, #3096)
4. **Don't request `image/*` and wrap X calls in X-error protection.** Removes the class
   of neighboring-application crashes (Gimp/xclip BadWindow). (#636, #1070)
5. **Put a copy of each captured entry into memory immediately.** Then closing the source
   doesn't wreck history (#1413); ignore empty ownership-change notifications (#3304).
6. **Encoding/cleanup on save.** Request `UTF8_STRING`/`;charset=utf-8`, validate UTF-8,
   trim the trailing `\0`; consider trimming the trailing `\n`. (#158, #681, #2573)
7. **Before the XTEST injection: `XSync` + pause + wait for the modifiers to be
   released.** Protection against the focus race (#1601) and against a held Super
   (#3428). Don't raise our window (protection against #1729) — keep as an invariant.
8. **Dedup on fully read data**, serialize event handling, compare normalized UTF-8 (not
   fragile strings). (#158, races)
9. **Memory limit:** cap 100 + per-entry size limit → a predictable RAM ceiling.
   (#1928, #3247)
10. **History management:** entry deletion and full clear (useful also as a reaction to
    an accidentally caught secret). (#1900, #308)

---

## Summary: the scariest *inherent* pitfalls

- **Password interception.** As soon as we enable CLIPBOARD monitoring, we — like CopyQ
  before v8 — will start sucking in passwords from KeePassXC/Bitwarden. In-memory saves
  us from a disk leak, but not from showing them in the popup. The
  `x-kde-passwordManagerHint` check + a `WM_CLASS` block-list must be laid out
  **immediately**, not "later".
- **Content loss when the source closes (#1413).** This is fundamental X11: CLIPBOARD is
  the owner, not a store. Only immediately reading a copy into memory on the XFIXES event
  saves us; on paste we already correctly become the owner ourselves.
- **INCR / large data (#1615, #636, #1070).** A greedy synchronous read will hang our
  GTK thread and may crash neighboring applications with a BadWindow error. Read
  asynchronously, with a limit, don't touch `image/*`, fence off X errors.
- **Detection going stale after suspend (#181/#1505).** We need an event-driven XFIXES
  subscription with reinitialization after resume — otherwise history silently stops
  filling.
- **Paste/focus races and a held Super (#1601, #3428).** Our "don't take focus, don't
  raise the window" architecture already greatly reduces the risk; finish it off with
  `XSync`+pause and waiting for the modifiers to be released before XTEST.
