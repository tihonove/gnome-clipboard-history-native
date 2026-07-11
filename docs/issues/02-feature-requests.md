# CopyQ feature requests through the lens of gnome-clipboard-history-native

An analysis of the most-requested feature requests in the
[hluk/CopyQ](https://github.com/hluk/CopyQ) repository (the `feature` label, sorted by
reactions and by comment count, deduplicated). The goal isn't to copy CopyQ, but to figure out
**what from the community wishlist is actually worth adopting** into our minimalist
`Win+V` clone, what to consciously leave out, and which cheap wins we've
missed.

A reminder about gnome-clipboard-history-native's positioning: native clipboard history for **GNOME/X11**,
a resident GTK daemon, a popup at the cursor, `Enter` pastes via XTEST. Right now —
**text only, memory only** (we deliberately don't write to disk). **Images are on the
near-term roadmap.** Anything that pulls CopyQ toward being an IDE-grade clipboard (scripts,
tabs, sync, themes) is, for us, bloat.

Verdicts: **Adopt** — in scope, doing it; **Maybe** — nice, but later;
**Out of scope** — bloat for a `Win+V` clone; **Already have** — implemented.

## Top feature requests

| # | Summary | reactions / comments | Verdict | Why |
|---|--------|----------------------|---------|--------|
| [#510](https://github.com/hluk/CopyQ/issues/510) | Unlimited history | 24 / 29 | **Out of scope** | We're in-memory on purpose. Offer a sensible `max-entries` (see cheap wins), but "unlimited" = disk + DB, which is an anti-goal. |
| [#1884](https://github.com/hluk/CopyQ/issues/1884) | arm64 (M1) builds | 4 / 58 | **Maybe** | We're Linux-only; arm64 is just adding to the `build.yml` matrix, but not a priority. |
| [#252](https://github.com/hluk/CopyQ/issues/252) | Images: save to file | 0 / 45 | **Maybe** | Once images exist — "save as…" from the popup is cheap and logical. |
| [#92](https://github.com/hluk/CopyQ/issues/92) | Dropbox / cloud sync | 2 / 26 | **Out of scope** | Sync between machines is a whole separate product. `Win+V` doesn't do this. |
| [#3444](https://github.com/hluk/CopyQ/issues/3444) | Paste via typing emulation (type, not Ctrl+V) | 0 / 25 | **Maybe** | Fits our XTEST path directly: for windows where Ctrl+V is ignored (VNC/Citrix/VM), "type" character by character. We're already layout-independent — this is a layer on top of `pasteInto`. |
| [#246](https://github.com/hluk/CopyQ/issues/246) | Number hotkeys to paste a position | 2 / 20 | **Adopt** | A `Win+V`/clcl classic: `1`–`9` in the open popup instantly paste the N-th entry. Cheap — just a branch in the key polling. |
| [#614](https://github.com/hluk/CopyQ/issues/614) | Show the window under the mouse cursor | 2 / 19 | **Already have** | `popupXY` already opens the popup at the cursor (or centered on the active window). |
| [#44](https://github.com/hluk/CopyQ/issues/44) | Sequential paste / "pop mode" | 0 / 19 | **Maybe** | Paste → the entry is removed / cursor moves to the next. Niche, but useful for forms; fits finish(). Later. |
| [#445](https://github.com/hluk/CopyQ/issues/445) | Alt+N tab switching | 0 / 17 | **Out of scope** | We have no tabs and never will. |
| [#1328](https://github.com/hluk/CopyQ/issues/1328) | Sound on copy | 3 / 17 | **Out of scope** | Pure bloat for a `Win+V` clone. |
| [#1687](https://github.com/hluk/CopyQ/issues/1687) | Delete all images, keep text | 0 / 15 | **Maybe** | Relevant after images: filter / bulk delete by type. While text-only — not relevant. |
| [#343](https://github.com/hluk/CopyQ/issues/343) | Images as entries | 0 / 14 | **Adopt** | Exactly our planned image support. |
| [#2056](https://github.com/hluk/CopyQ/issues/2056) | Delete a block of entries (range) | 0 / 14 | **Maybe** | Useful, but requires multi-selection — complicates the UI. Later. |
| [#502](https://github.com/hluk/CopyQ/issues/502) | Backup | 0 / 13 | **Out of scope** | Nothing to back up — history lives in memory. |
| [#3014](https://github.com/hluk/CopyQ/issues/3014) | Don't save from a specific source (`pass`, password managers) | 0 / 13 | **Adopt** | A key security feature. Exclusion by source WM_CLASS + secret detection — see cheap wins. |
| [#3366](https://github.com/hluk/CopyQ/issues/3366) | Pre-select the second entry in the menu | 1 / 13 | **Maybe** | Handy for "swap the last two" — but our Enter already pastes the top one. A trifle. |
| [#226](https://github.com/hluk/CopyQ/issues/226) | Entry counter | 0 / 13 | **Out of scope** | No tabs; a minimalist doesn't need a counter. |
| [#1365](https://github.com/hluk/CopyQ/issues/1365) | Search the selection in Google, etc. | 0 / 13 | **Out of scope** | These are actions over the clipboard — CopyQ's domain, not ours. |
| [#1948](https://github.com/hluk/CopyQ/issues/1948) | Cycle through entries and paste with a single hotkey | 0 / 12 | **Out of scope** | A separate interaction model beyond the popup; complicates things. |
| [#2456](https://github.com/hluk/CopyQ/issues/2456) | Multiple images | 0 / 12 | **Maybe** | Part of image support; the basic case (one image) first. |
| [#187](https://github.com/hluk/CopyQ/issues/187) | Undo delete | 0 / 11 | **Maybe** | Cheap with in-memory (a last-deleted buffer), a pleasant safety net. |
| [#3165](https://github.com/hluk/CopyQ/issues/3165) | Hide the popup from screen capture | 1 / 11 | **Out of scope** | Windows-specific (WDA_EXCLUDEFROMCAPTURE); not relevant on X11. |
| [#145](https://github.com/hluk/CopyQ/issues/145) | Window position + search | 2 / 10 | **Adopt** (search) | Position — already have it; **search-as-you-type** is our biggest missed win (see below). |
| [#151](https://github.com/hluk/CopyQ/issues/151) | "Paste as plain text" hotkey | 0 / 10 | **Adopt** | We already store only text, but it's meaningful: pasting without formatting via an alt-hotkey from the popup. Cheap. |
| [#1147](https://github.com/hluk/CopyQ/issues/1147) | Fully plain-text mode | 0 / 9 | **Already have** | We're text-only by definition — we don't store formatting. |
| [#1257](https://github.com/hluk/CopyQ/issues/1257) | Strip all formatting | 3 / 2 | **Already have** | Same: there's nothing for us to strip. |
| [#1288](https://github.com/hluk/CopyQ/issues/1288) | Fuzzy search | 3 / 8 | **Maybe** | First a simple substring search (Adopt), fuzzy is an upgrade later. |
| [#1172](https://github.com/hluk/CopyQ/issues/1172) | Don't delete "pinned" entries | 4 / 3 | **Adopt** | Pin/favorite is frequently requested; protects entries from eviction by `max-entries`. |
| [#2244](https://github.com/hluk/CopyQ/issues/2244) | Collect duplicates | 4 / 3 | **Adopt** | Deduplicating identical entries — a must for clean history; cheap. |
| [#2964](https://github.com/hluk/CopyQ/issues/2964) | Clear everything except pinned | 1 / 2 | **Adopt** | A natural complement to clear-all + pin. |
| [#641](https://github.com/hluk/CopyQ/issues/641) | Encrypt entries | 2 / 6 | **Out of scope** | No disk — nothing to encrypt; we handle secrets via exclusion, not crypto. |
| [#2253](https://github.com/hluk/CopyQ/issues/2253) | Delete confirmation | 1 / 5 | **Maybe** | A cheap safeguard against accidental deletion; could be replaced by Undo (#187). |
| [#344](https://github.com/hluk/CopyQ/issues/344) | Limit by entry age | 1 / 7 | **Maybe** | Entry TTL — paired with `max-entries`; but a count limit is simpler and sufficient. |
| [#1247](https://github.com/hluk/CopyQ/issues/1247) / [#1503](https://github.com/hluk/CopyQ/issues/1503) | Native notifications | 2 / 7 | **Out of scope** | `Win+V` is silent. Not needed. |
| [#1834](https://github.com/hluk/CopyQ/issues/1834) | Keep search focus on Up/Down | 1 / 4 | **Adopt** (together with search) | An important UX nuance: you type-filter, arrows move the selection without losing the search line. |
| [#1569](https://github.com/hluk/CopyQ/issues/1569) / [#1318](https://github.com/hluk/CopyQ/issues/1318) | Search: words in any order / diacritics-insensitive | 1 / 3 | **Maybe** | Search improvements after the basic version. |
| [#2558](https://github.com/hluk/CopyQ/issues/2558) / [#2750](https://github.com/hluk/CopyQ/issues/2750) | QR generator / emoji picker | 1 / 4 | **Out of scope** | Clearly CopyQ's "pocket knives". Not our niche. |
| [#247](https://github.com/hluk/CopyQ/issues/247) | Different paste behavior per window | 1 / 5 | **Already have** (partially) | We already distinguish terminals (Ctrl+Shift+V) by WM_CLASS. Generalizing the rules — no need. |

## Cheap wins we've missed

Small features with a high benefit/cost ratio that fit directly onto our
architecture (popup + in-memory list + XTEST paste). None require disk and none
break the minimalism.

1. **Search-as-you-type** — [#145](https://github.com/hluk/CopyQ/issues/145),
   [#1288](https://github.com/hluk/CopyQ/issues/1288), [#1834](https://github.com/hluk/CopyQ/issues/1834).
   The most requested and the cheapest for us. We already read the keyboard by
   polling via xgb (not GTK), so printable characters already reach us —
   add a filter line and rebuild `listBox` by substring. Arrows
   keep moving the selection (focus "stays" in the search — we physically don't have any,
   we drive the selection manually via `SelectRow`). **Highest priority.**

2. **Numeric quick hotkeys `1`–`9`** — [#246](https://github.com/hluk/CopyQ/issues/246).
   In the open popup a digit instantly pastes the N-th visible entry. One branch in
   the polling key handler → `finish(true)` with the right index. Combines with search
   (digits paste when not in query-input mode, or via a
   modifier). Very "Win+V".

3. **`max-entries` cap + dedup** — [#2244](https://github.com/hluk/CopyQ/issues/2244),
   [#344](https://github.com/hluk/CopyQ/issues/344).
   Since memory is finite, a ring buffer of N entries (say, 200) + dropping
   an exact duplicate (raise the existing entry to the top instead of appending).
   Trivial on an in-memory slice; immediately keeps history clean.

4. **Pin / favorites + "clear everything except pinned"** —
   [#1172](https://github.com/hluk/CopyQ/issues/1172), [#2964](https://github.com/hluk/CopyQ/issues/2964).
   A `pinned` flag on an entry: not evicted by `max-entries`, shown at the top.
   A pin hotkey in the popup. Practically "free" on the in-memory model.

5. **Clear-history / clear-all**. In CopyQ it's implied everywhere; we don't have it at all.
   A hotkey (or item) "clear history" — one line zeroing the slice (respecting
   pinned). Plus, optionally, auto-clear on restart
   ([#2918](https://github.com/hluk/CopyQ/issues/2918)) — we already have it de facto
   (memory doesn't survive a restart), so we could just document it as a feature.

6. **Excluding secrets / password sources** —
   [#3014](https://github.com/hluk/CopyQ/issues/3014), CopyQ's FAQ about "ignore password window".
   Since we own the clipboard and know the moment of copying, we can skip saving entries
   when the active window is a known password manager (by WM_CLASS), or when the clipboard's
   `TARGETS` contains a secret marker (`x-kde-passwordManagerHint`,
   used to mark passwords by KeePassXC / a number of GTK apps). Cheap, meaningful for
   trust. CLI tools (`pass`) are harder — but at least app-level exclusion we take.

7. **"Paste as plain text"** — [#151](https://github.com/hluk/CopyQ/issues/151).
   We're already text-only, so the default is plain anyway. A meaningful gesture — an alt-hotkey
   paste that forcibly emits `text/plain` without an inherited rich
   format (becomes relevant with images / rich sources). For now — nearly free.

8. **Undo the last delete** — [#187](https://github.com/hluk/CopyQ/issues/187).
   Keep the last-deleted entry and restore it via a hotkey. On an in-memory list —
   trivial; replaces the confirmation dialog ([#2253](https://github.com/hluk/CopyQ/issues/2253)).

9. **Paste via "typing" (autotype)** — [#3444](https://github.com/hluk/CopyQ/issues/3444).
   For windows where Ctrl+V doesn't work (VNC/Citrix/VM/some terminals), a mode that
   "types the content character by character" via XTEST. We already have layout-independent
   injection (a spare keycode) — this is a layer on top of `pasteInto`, on its own hotkey.

## Image-related requests (we're adding images)

Images are the near-term plan, so these tickets are worth keeping in view as a
checklist of user expectations:

- [#343](https://github.com/hluk/CopyQ/issues/343) **Images as entries** (14 comments) —
  the basic case: save an image from the clipboard as an entry and be able to paste it back.
  The core of the feature.
- [#252](https://github.com/hluk/CopyQ/issues/252) **Images: save to file** (45 comments, the most
  discussed among images) — "save as PNG" from the popup. Cheap and expected.
- [#2456](https://github.com/hluk/CopyQ/issues/2456) **Multiple images** (12 comments) — history
  should properly hold a series of images, not just the last one.
- [#1687](https://github.com/hluk/CopyQ/issues/1687) **Delete all images, keep text**
  (15 comments) — once different types appear, a filter / bulk delete by type is needed.

Practical takeaways for our implementation: (1) entries become typed
(text/image) — a thumbnail preview in the popup row instead of 3 lines of text; (2) images are
heavier — `max-entries` (cheap win #3) becomes mandatory, otherwise memory is
eaten up; (3) owning the clipboard gets more complex: serve `image/png` in response to a paste
request alongside `text/plain`.
