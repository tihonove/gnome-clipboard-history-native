# X11 / Wayland / paste / focus — lessons from CopyQ issues

An analysis of `hluk/CopyQ` issues on the topics that are core to gnome-clipboard-history-native
rather than peripheral: synthetic paste, focus stealing, X11 selection mechanics,
keyboard layout during paste, and (for the future) Wayland. The goal is the same as
before — not to clone the CopyQ "kitchen sink", but to take the bugs it already paid
for and check them against our architecture.

A reminder about gnome-clipboard-history-native for context: GNOME **X11 only** (Wayland out of
scope), Go + GTK3 (Yaru) + pure X11 via `jezek/xgb`. The hotkey is not
`XGrabKey(Super)` (impossible under mutter) but a gsettings custom keybinding that
launches `gnome-clipboard-history-native --show`. The popup is a `GTK_WINDOW_POPUP`
(override-redirect); we obtain keyboard focus via `xproto.GrabKeyboard` on root +
polling, we do not call `SetInputFocus` (the target window keeps focus). Paste is a
native XTEST FakeInput Ctrl+V (Ctrl+Shift+V for terminals, by WM_CLASS),
layout-independent via a temporary spare keycode remapped to 'v' in all XKB groups.
The daemon owns CLIPBOARD through GTK while it is alive.

Notation: `#NNNN`, versions, and setting names are as in the originals.

---

## Paste and focus (X11)

### CopyQ pastes via synthetic keypresses — and it's fragile

CopyQ doesn't "write into the window" — it simulates the paste keystrokes through
**libXtst** (`XTestFakeKeyEvent`), exactly like we do. Hence the whole class of their
problems — and confirmation that our path (XTEST FakeInput) is the very same one, with
all the same pitfalls.

- **#3196** (ghostty/kitty, X11, closed) — "paste on activate" stopped working in
  terminals starting from v11. Using `kitty --debug-input`, the user proved that
  **CopyQ wasn't sending the Shift modifier**: kitty saw a bare `Insert` instead of
  Shift+Insert. hluk immediately asked about `ldd | rg libX` — whether `libXtst.so.6`
  is present. Lesson: injecting a modifier + a key via XTEST is not guaranteed atomic;
  it happens that the key "goes through" but the modifier does not.

- **#1729** (GNOME/X11, open) — **paste fires many times** (3–30 duplicates) and hangs
  the target window for a second. hluk: "the application stalls completely while it
  simulates Shift+Insert — probably because of the window-minimize animation in the
  WM". Key advice from the thread — straight about timing:
  - `window_wait_after_raised_ms` (they tried 150 ms) — **a pause after
    raising/hiding the window before paste**;
  - `window_key_press_time_ms` (**default 50 ms on Linux**) — the delay between press
    and release of a synthetic key; `0` breaks paste in Chrome;
  - as a fix CopyQ **removed the `XSync()` in the middle** of the press→release
    simulation (#2116).

  The duplication mechanism, apparently: while the hide/minimize animation of the popup
  window runs, focus "trembles", events are re-sent repeatedly / to the wrong place.

**How this maps onto gnome-clipboard-history-native — and what we already did right:**

1. **We do not minimize a window with animation.** Our popup is an override-redirect
   `GTK_WINDOW_POPUP`; the WM doesn't manage it and doesn't animate it. So the main
   source of #1729 (WM hide animation stalls XTEST) is structurally absent for us. This
   is a strong argument in favor of override-redirect.

2. **The order "release grab → return focus to target → paste" is time-critical.**
   Since we hold `GrabKeyboard` on root, we must release it (`Ungrab`) before FakeInput,
   otherwise the synthetic events go to the wrong place. And between ungrab/hide of the
   popup and FakeInput there needs to be a **small pause** — this is exactly the
   analogue of `window_wait_after_raised_ms`. If we see duplicates / a lost modifier,
   this gap is the prime suspect.

3. **Plan for the press→release delay from the start.** CopyQ's default of 50 ms on
   Linux and the explicit Chrome breakage at 0 ms are not superstition: some toolkits
   (Chromium) need a nonzero key-hold time, otherwise the keypress isn't registered.
   Recommendation: keep ~30–50 ms between FakeInput(press) and FakeInput(release) for
   each key, configurable. Do **not** put press and release in one XSync packet
   back-to-back.

4. **Verify the active window before paste.** CopyQ relies on
   `currentWindowTitle()`/`currentWindowClass()` both in scripts (#2557) and in the
   general API. We need this twice: (a) to choose Ctrl+V vs Ctrl+Shift+V by WM_CLASS —
   we already do this; (b) as a sanity check that after ungrab focus really returned to
   the window that was active **before** the popup was shown (remember
   `_NET_ACTIVE_WINDOW` at `--show`, verify it before FakeInput). Otherwise — paste into
   the wrong window.

### Terminals require Shift, and this cannot be solved with "one shortcut"

- **#2557** (Hyprland, closed) and **#3196** — by default CopyQ sends **Shift+Insert**
  (hluk: "this is the default paste for both CLIPBOARD and PRIMARY in most terminals").
  But in some terminals (Alacritty with default config, kitty) Shift+Insert is not
  bound to paste, while Ctrl+Shift+V is; and conversely, Ctrl+Shift+V breaks paste in
  most **non**-terminals. hluk's conclusion: **detect the window by title/class and
  pick the shortcut** (`if window == 'Alacritty' → Ctrl+Shift+V else Shift+Insert`).

**For gnome-clipboard-history-native:** this is exactly our strategy (Ctrl+V for normal
windows, Ctrl+Shift+V for terminals by WM_CLASS), and CopyQ's history validates it:
**there is no universal paste shortcut**, window detection is mandatory. The difference:
CopyQ chose Shift+Insert as the base, we chose Ctrl+V. Our choice is better for
GTK/Qt/browsers; for terminals Ctrl+Shift+V is the GNOME Terminal/VTE standard, which
is what we want. Worth keeping the **list of terminal WM_CLASSes extensible** (ghostty,
kitty, alacritty, wezterm, foot(xwl), Terminator, xterm — xterm only takes
Shift+Insert at all!). For xterm/urxvt a separate Shift+Insert branch may be needed —
build it as a "class → combination" map, not a boolean "terminal yes/no" flag.

### Focus stealing: CopyQ can only "ask for" activation — we bypass it

This is perhaps the main architectural takeaway from the whole sample.

- **#2960** (i3, closed) — a menu opened via a WM keybinding **does not get focus**;
  opened via `copyq menu` from a script — it does. hluk: "this is focus prevention or a
  WM bug, I don't fix it in the app itself."
- **#2993** (Mint, closed) — `copyq show` **doesn't always start focused** (depends on
  how it was closed last time). hluk plainly: **"I have almost no control over window
  focus — the app can only *request* activation"**.
- **#3325** (KDE, closed) — the tray menu doesn't activate on shortcut if the CopyQ
  window isn't focused; the log says `Failed to create grabbing popup … parent window
  has received input`.

**For gnome-clipboard-history-native:** our design hits this exact class of bugs. We **do not rely
on activation / `SetInputFocus`** at all — instead `GrabKeyboard` on root intercepts
the keyboard entirely, and the target window never loses focus. This is precisely what
CopyQ cannot achieve from Qt. As long as the grab succeeds, GNOME's
focus-stealing-prevention doesn't get in our way — we don't ask for focus, we
temporarily **capture it at the X level**. Risks to keep in mind:
- `GrabKeyboard` may return `AlreadyGrabbed`/`Frozen` (another client holds the grab —
  e.g. an open system menu). Handle the return code; on failure — retry with a short
  backoff or quietly exit, **don't** show a "dead" popup without a keyboard (that's bug
  #2960/#3325 in another wrapper).
- The grab must be released (`UngrabKeyboard`) **before** the XTEST paste and on **any**
  popup-close path (Esc, loss of visibility, selection). Otherwise the keyboard "sticks"
  at root — the target window goes mute (analogous to the hang in #1729).

### Global hotkey: why we were right to move to gsettings

- **#1267** (open) — `Meta+V`/Super as a global shortcut **requires a double press** or
  doesn't work; in GNOME it conflicts with the built-in Super+V. **The working solution
  from the thread is exactly ours**: create a custom keybinding in GNOME settings that
  runs `copyq -e "menu()"`/`toggle()`. Several users arrived at the same thing
  independently.
- **#3616** (closed) — on autostart the global shortcut **isn't registered without a
  ~5 s delay** (the D-Bus portal isn't up yet); the fix is to connect to D-Bus later.
- **#3488** (KDE/Wayland, open) — internal global shortcuts don't fire.

**For gnome-clipboard-history-native:** confirmation that `XGrabKey(Super)` under mutter is a dead
end (CopyQ collected the same pitfalls both via Qt and via the D-Bus portal), and
**delegating the hotkey to GNOME itself via gsettings is the only reliable path**.
Bonus: we don't depend on the D-Bus portal start race (#3616) — GNOME itself launches
`gnome-clipboard-history-native --show`.

---

## Layout / XKB and synthetic paste

Our most hard-won bug (the real keycode 'v' in the ru layout produces Cyrillic)
manifests for CopyQ from other angles — but the root is the same: **keycode ≠ keysym,
and synthetic input operates on keycodes.**

- **#3378** (open) — shortcuts with numpad digits **get mismapped**: you assign
  `Ctrl+Keypad1`, the UI shows `Ctrl+1`, and only the top row works. Cause (in the
  thread): the hardware keycodes of the numpad and the top row are different, while the
  Qt layer collapses them into one keysym. That is, Qt/CopyQ get confused precisely at
  the level of keycode↔keysym mapping — the same layer where our 'v' bug lives.
- **#3405** (open) — Chinese characters **are sometimes pasted as literal unicode** (not
  as text), fixed by re-selecting from history. This is no longer XTEST but selection
  format negotiation (see below), yet the symptom "the wrong thing got pasted" is the
  same.
- **#3253** (open) — a broken `Keyboard layout change (xneur like)` command — shows that
  CopyQ users actively live in multi-layout environments; there is no direct XTEST
  layout fix in CopyQ.

**Conclusion about our approach.** There is **no** direct duplicate of our
"synthetic 'v' → Cyrillic" bug in CopyQ's issue tracker — and that's probably because
CopyQ sends **Shift+Insert**, and `Insert` is a key with no letter keysym, invariant to
layout. This is essentially an **alternative solution to the same problem**: pick a
paste key whose keysym doesn't depend on the XKB group.

Hence two valid strategies, both legitimate:

1. **Our path — a temporary spare keycode remapped to 'v' in all groups**
   (`XChangeKeyboardMapping` on an unused keycode → FakeInput on it → revert). Pro: we
   send exactly Ctrl+V / Ctrl+Shift+V — the "native" combinations that all
   GTK/Qt/browsers/terminals understand. Con: we mutate the global layout for
   milliseconds (a race if someone is typing; you need `XGrabKeyboard`/`GrabServer`
   during the remap and a mandatory rollback in `defer`).
2. **CopyQ's path — paste with a layout-invariant key** (Insert+modifier), without
   touching the mapping. Pro: we don't mutate the layout. Con: Shift+Insert is not
   universal (#2557/#3196), in terminals and some apps it's not bound to paste; which is
   exactly why CopyQ still has to detect the window and switch to Ctrl+Shift+V — where
   'v' again runs into the layout (not a problem for them, since terminals often treat
   the keycode, but in the general case — the same pit).

**Bottom line for gnome-clipboard-history-native:** our spare-keycode approach is **valid and
probably more reliable** for the target set of applications (GTK/Qt/Chromium/VTE),
because we send exactly the combinations these applications expect (Ctrl+V /
Ctrl+Shift+V) rather than the compromise Shift+Insert. What must be verified in the
implementation:
- do the remap under `GrabServer`/`GrabKeyboard`, so no one sees a "half-baked" layout;
  the keycode rollback goes in `defer`, even on panic;
- find the spare keycode dynamically (empty in the current map), don't hardcode it —
  otherwise a conflict on nonstandard layouts;
- edge case: if the group is "shifted" (the layout switcher is on Ctrl/Shift) — our
  synthetic Ctrl/Shift must not accidentally switch the group; test on ru+en with a
  Shift switcher.

---

## X11 selection mechanics (PRIMARY vs CLIPBOARD, INCR, ownership transfer)

While we own `CLIPBOARD`, these traps are ours.

### We don't touch PRIMARY — and that's a deliberate saving of pain

CopyQ monitors **both** CLIPBOARD **and** PRIMARY (the mouse selection) and endlessly
fixes their synchronization (this is the dominant bug category in their release
history, see `01-release-history-lessons.md`). **#1644** is a separate story about how
to catch PRIMARY at all (it's about Wayland there, but it illustrates how much
attention PRIMARY demands).

**For gnome-clipboard-history-native:** we read only CLIPBOARD and don't maintain PRIMARY — this
removes a whole layer of selection↔selection races. Keep this as a principle, don't
"improve" it.

### INCR: large clips can't be handed over in one chunk

- **#2233** (Wayland→XWayland, closed) — pasting a large image, CopyQ handed over
  **exactly 65536 bytes (2^16) and cut off**: `Failed to send all clipboard data; sent
  65536 bytes out of 1686475`. This is the classic X11 boundary: a selection larger
  than the maximum property size **must** be transferred via the **INCR** protocol
  (in chunks, through `PropertyNotify`).
- **#3424** (X11, closed) — "CopyQ doesn't paste large items" (40+ lines) with
  Synchronize enabled; the item also gets **zeroed out**. hluk admits that paste rests
  on a **heuristic** and doesn't always work on large/special data.

**For gnome-clipboard-history-native:** as the owner of CLIPBOARD we **must implement the server
side of INCR** for handing over large values (especially once images arrive — they'll
blow past the property limit right away). If we rely on GTK ownership of the selection,
INCR is handled by GTK — but if we ever move to a pure xgb owner, we'll have to write
the INCR transfer ourselves (chunk via `PropertyNotify`, the `INCR` flag in the first
reply, a final empty chunk). While GTK owns it — verify that large images actually
reach `gimp`/LibreOffice (that same #2233 case) and aren't truncated.

### Ownership transfer, `application/x-copyq-owner`, buffer loss on exit

- **#1960** (closed) — CopyQ **by default only monitors** the buffer and **doesn't take
  ownership** on every clip: hluk plainly writes that taking ownership on every change
  "turned out to be a bad idea — it confuses applications, drops their internal buffer
  data". While the owner is the source application, a clip has one TARGET (`STRING`);
  the full set (`text/plain`, `UTF8_STRING`, `TEXT`, `TIMESTAMP`, `TARGETS`, `MULTIPLE`,
  `SAVE_TARGETS`, and the marker **`application/x-copyq-owner`**) appears only when
  CopyQ takes ownership.

**For gnome-clipboard-history-native — several important consequences:**
- **Owner marker.** CopyQ tags its clips with `application/x-copyq-owner` so as **not to
  loop** on capturing its own paste. We need an analogue (our own mime target like
  `application/x-gchn-owner`): when the daemon itself puts content into CLIPBOARD, the
  monitor should recognize "this is mine" by the marker and **not** re-add it to
  history. Otherwise — duplicates and potential loops (cf. the Wayland copy-paste loop
  #2224).
- **Ownership = responsibility to serve on request.** Since the daemon owns CLIPBOARD
  through GTK "while alive", when the daemon exits the **buffer will clear** (the owner
  is gone) — the classic complaint about X11 managers. Options: (a) don't hold ownership
  permanently, only for the moment of paste; (b) on exit, hand the content off via the
  `SAVE_TARGETS`/clipboard-manager protocol. For now an (a)-like scenario is preferable:
  minimizing ownership time also reduces the risk of "confusing applications" from
  #1960.
- **Don't clobber the buffer needlessly.** The lesson of #1960 + release history:
  aggressively becoming the owner on every change is harmful. And we shouldn't: we take
  ownership only when the user selects an old item to paste.
- **`TIMESTAMP` against redundant re-reads.** From CopyQ's release history: use the
  `TIMESTAMP` target to avoid re-reading an unchanged buffer — protection against
  stalls and duplicates. If we read CLIPBOARD by polling / by event — compare TIMESTAMP
  before pulling the data.

### Format normalization: careful with newlines and encodings

- **#2168** (closed) — CopyQ **changed `\n` to `\r\n`** (LF→CRLF) because of the code
  "Wayland: fix synchronizing UTF-8". A side effect of UTF-8 normalization corrupted the
  content on X11.
- **#3405** — Chinese text sometimes degraded into literal unicode.

**For gnome-clipboard-history-native:** hand over data **byte-for-byte**, as received. Don't "fix"
newlines, don't re-encode. If we support multiple targets (`UTF8_STRING`,
`text/plain;charset=utf-8`, `STRING`) — for `STRING` (latin-1) hand over only ASCII-safe
data, for unicode strictly `UTF8_STRING`/`text/plain;charset=utf-8`, otherwise we get
#3405.

---

## Wayland — what doesn't work and why

gnome-clipboard-history-native is X11-only, and CopyQ's issues explain **why** that is reasonable
and **what** would be needed for a hypothetical Wayland backend. Short conclusion: under
GNOME Wayland a full clipboard manager with a popup-at-cursor and synthetic paste is
**fundamentally impossible** with stock means; it works only on wlroots (Sway/Hyprland),
and even then with external hacks.

### 1. Reading the buffer: needs a special protocol that GNOME Wayland lacks

Under Wayland an ordinary application **cannot see** another app's buffer — you need the
privileged "data-control" protocol:
- **wlr-data-control-unstable-v1** (wlroots: Sway, Hyprland) or the newer
  **ext-data-control-v1** (#3294 — Sway 1.11 added it; a monitoring regression when
  moving to it), or `COSMIC_DATA_CONTROL_ENABLED=1` in Cosmic (#2847).
- **GNOME Wayland (mutter) doesn't implement these protocols.** So under GNOME Wayland
  CopyQ runs the buffer monitor **as an X11 application through XWayland** (see the
  detection `XAUTHORITY includes 'mutter-Xwayland'` in the Wayland-support script,
  #2747). That is, CopyQ's "GNOME Wayland support" is actually the X11 path under
  XWayland, with all its limitations (it can't see native Wayland clips).

Symptoms when the protocol is missing / slow:
- **#1243**, **#2847** — "No clipboard content", log `Activating Wayland clipboard
  took 5000 ms → Failed to activate Wayland clipboard`, `Null data in selection`.
- **#3125** (open) — when accessing its **own** buffer CopyQ **freezes for ~1 s**:
  `DataControlOffer: timeout reading from pipe`, `ELAPSED 1010 ms accessing …`.
  hluk: "Wayland clipboard management is a mess"; data is transferred through a unix
  pipe in the same GUI thread, which blocks on it. A multi-threaded workaround gave rare
  crashes.
- **#1644** — PRIMARY (the mouse selection) under Wayland is caught only via
  data-control, and only if the components (GTK/Qt) support it.
- **#1917**, **#2224** — on Sway CopyQ **eats a core** / goes into a **copy-paste loop**
  and hangs the DE.

### 2. Synthetic paste: XTEST doesn't work, you need ydotool/wtype

Under native Wayland `XTestFakeKeyEvent` **has no effect** (there's no X server to
inject into the focus). CopyQ works around this with external tools in the
Wayland-support script:
- **ydotool** — requires a daemon with access to `/dev/uinput` (root/group), generates
  input at the kernel level. Fragile to set up: **#2747** (Hyprland) — works from a
  terminal but not from `exec-once`/systemd autostart (service environment/permission
  problems), fixed by a manual restart.
- **wtype** — an alternative for wlroots; doesn't work under GNOME.
- Terminals still require choosing Shift+Insert vs Ctrl+Shift+V by window title
  (**#2557**) — except getting the window title under Wayland is also impossible with
  stock means (in KDE `currentWindowTitle` isn't supported, noted in the script itself
  #2747).

**How we solved this.** We don't spawn `ydotool` — we hold our own long-lived
`/dev/uinput` device in-process (`internal/uinput`) and send a layout-independent
`Shift+Insert` (works in both GUI and terminals — no window detection needed). We close
the "fragile permission setup" problem (#2747) with the auto-setup in
`uinput_setup.go`: `--install`/`--setup-input` install a udev rule (pkexec/sudo
escalation via re-exec of our own binary), `.deb`/`.rpm` — statically from the same
text. The rule is combined:
- **`TAG+="uaccess"`** — logind puts an ACL on the active user immediately, without
  re-login (mainline systemd: GNOME/Ubuntu/Fedora/Arch). Narrow: only this user, only
  `uinput`. Verified — `getfacl /dev/uinput` shows `user:<you>:rw-` right away.
- **`GROUP="input"` + `usermod -aG`** — a fallback if logind didn't grant the ACL;
  requires a re-login, broader in permissions (all input devices).

The one-time nature is critical: before escalating we check `uinput.HasAccess()` — if
access already exists, we do NOT invoke sudo/pkexec. The autostart daemon only logs a
hint, it doesn't escalate (a prompt in a non-interactive context is unacceptable).

**A future root-free path — libei/RemoteDesktop portal.**
`org.freedesktop.portal.RemoteDesktop` + libei gives Wayland-native injection without
`/dev/uinput` permissions (a permission dialog instead of udev). Out of scope for now:
there's no mature pure-Go binding (cgo to `libei`/`liboeffis` + a recent
`xdg-desktop-portal`, patchy on old Ubuntu LTS), it needs D-Bus negotiation and a
per-session dialog. A migration candidate once the bindings mature.

### 3. Popup positioning and focus: Wayland forbids this by design

- `Wayland does not support QWindow::requestActivate()` (**#3237**, #2233) — a window
  **cannot** request focus for itself.
- You cannot **set a window's position** on screen or **query the cursor position** →
  "show under mouse cursor" is impossible (**#3331**, hluk's comment in #1982).
- Popup grabs require a `transientParent` that has received input, otherwise
  `Failed to create grabbing popup` (**#3325**); under Qt 6.9+ Wayland popups stopped
  showing at all (**#3108**), on KDE the popup isn't visible (**#3237**).
- Non-native Qt notifications don't work (no positioning) — only through the
  notification daemon (#3237).
- Global shortcuts — only through the D-Bus GlobalShortcuts portal, with a start race
  (#3616) and bugs (#3488).

### Verdict on the feasibility of a gnome-clipboard-history-native Wayland backend

- **GNOME Wayland: not worthwhile.** mutter gives neither data-control (reading the
  buffer), nor popup positioning, nor synthetic paste, nor focus. Everything that
  "works" for CopyQ under GNOME Wayland is an XWayland hack that can't see native clips
  and still runs into XTEST anyway. Our popup-at-cursor and grab-focus are not
  implementable there.
- **wlroots (Sway/Hyprland): technically possible, but expensive and fragile.** You'd
  need: (1) `ext-data-control-v1`/`wlr-data-control` for monitoring and owning the
  buffer (accounting for the blocking pipe reads of #3125 → move to a separate
  thread/process); (2) `ydotool` (uinput, root) or `wtype` for paste, plus terminal
  detection without a stock window title; (3) `zwlr-layer-shell` to show the popup on
  top (including over fullscreen, #2953) — ordinary Wayland windows don't set a
  position.
- **Practical conclusion: staying X11-only is right.** If Wayland ever — then **only
  wlroots** and **as a separate backend** (data-control + layer-shell + ydotool), not by
  "porting" the X11 logic. We cross off GNOME Wayland until mutter provides a
  data-control protocol.

---

## GNOME / mutter specifics

- **Focus-stealing prevention.** GNOME/mutter denies windows activation — this is
  #2960/#2993/#3325 for CopyQ. Our override-redirect popup + `GrabKeyboard` on root
  **bypasses** it: we don't participate in the WM activation policy. Keep this as a key
  advantage and don't break it (don't call `SetInputFocus`, don't request
  `_NET_ACTIVE_WINDOW` on ourselves).
- **Override-redirect vs hide animations.** #1729 shows that a managed window, when
  minimized with animation, stalls the XTEST paste. Our override-redirect isn't animated
  by the WM — we just hide/destroy it. Preserve this path; still leave a micro-pause
  between hiding the popup and FakeInput.
- **Multi-monitor / popup positioning.** **#3608** — a fundamental trap: `_NET_WORKAREA`
  in EWMH is **one rectangle for the whole virtual desktop**, it physically doesn't
  describe the available area **per monitor**. Qt takes `_NET_WORKAREA ∩ screenRect` and
  on secondary monitors gets garbage (the window flies down, the height is clipped to
  the laptop's height). **For gnome-clipboard-history-native:** compute the popup position at the
  cursor from the **RandR CRTC** (the geometry of the specific output under the cursor),
  **not** from `_NET_WORKAREA`. Take the monitor bounds from
  `RRGetCrtcInfo`/the `xinerama` screen the cursor landed in; clamp the popup to that
  rectangle, not to the overall workarea.
- **"Show under cursor" on the first try.** #1982 — CopyQ's first show places the window
  in the wrong spot (WM restore-geometry overwrites the managed window's position). Our
  override-redirect sets the position itself (`ConfigureWindow` x/y), the WM doesn't
  "restore" it — so this bug doesn't reproduce for us, **as long as** we don't rely on a
  saved WM geometry. Always set the position explicitly on every show.
- **HiDPI/scaling.** CopyQ hit scaling problems on Hyprland (#2744) — under X11 with
  fractional scaling the cursor coordinates and window geometry in device pixels vs
  logical pixels diverge. For gnome-clipboard-history-native: we work in X11 pixels (RandR/xgb
  returns device px), GTK may scale content by `GDK_SCALE` — watch that the popup
  position is computed in the same units as the cursor (raw X pixels), while the window
  size accounts for GTK scale.
- **Menus/tray.** Not our case (we have our own popup, not a Qt tray menu), but #3325 is
  useful as a reminder: grab popups under a compositor are finicky; our root grab is more
  reliable than a WM-managed grabbing popup.

---

## Conclusions for gnome-clipboard-history-native (checklist)

**Paste / XTEST:**
- [ ] Keep a pause of **~30–50 ms** per key between FakeInput(press) and
      FakeInput(release) (CopyQ default 50 ms; `0` breaks Chrome). Configurable. #1729
- [ ] Do **not** XSync between press and release in one packet (the duplication bug
      #1729/#2116).
- [ ] A pause between hiding the popup / ungrab and starting the paste (analogue of
      `window_wait_after_raised_ms`), so focus has time to return. #1729
- [ ] `UngrabKeyboard` **mandatory** before XTEST and on **any** popup-close path —
      otherwise the target window goes mute. #2960/#3325
- [ ] Check the `GrabKeyboard` return code (`AlreadyGrabbed`/`Frozen`) — on failure
      don't show a keyboardless popup. #2960/#3325
- [ ] A "WM_CLASS → paste combination" map, not a boolean "terminal": Ctrl+Shift+V for
      VTE/kitty/alacritty/ghostty/wezterm; Shift+Insert for xterm/urxvt; Ctrl+V — the
      default. Extensible list. #2557/#3196
- [ ] Remember `_NET_ACTIVE_WINDOW` at `--show`, verify before paste — don't paste into
      the wrong window.

**Layout / XKB:**
- [ ] Do the spare-keycode remap under `GrabServer`/`GrabKeyboard`; the keycode rollback
      in `defer` (even on panic). #3378 (the same keycode↔keysym pit)
- [ ] Find the spare keycode dynamically (an empty slot), don't hardcode it.
- [ ] Test on ru+en with a group switcher on Shift/Ctrl — our synthetic modifiers must
      not switch the layout.

**Selection:**
- [ ] Don't touch PRIMARY at all — only CLIPBOARD (saves a layer of races). #1644
- [ ] Implement/verify **INCR** for handing over large values (mandatory once images
      arrive — #2233 cuts off at 64 KB). #2233/#3424
- [ ] A `application/x-gchn-owner` marker on our own clips → the monitor ignores our own
      paste (no duplicates/loops). #1960/#2224
- [ ] Own CLIPBOARD **for a minimal time** (ideally only for the moment of paste), so as
      not to confuse applications and not to lose the buffer when the daemon exits.
      #1960
- [ ] Hand over data **byte-for-byte**: don't change `\n`→`\r\n`, don't re-encode;
      unicode — strictly `UTF8_STRING`/`text/plain;charset=utf-8`. #2168/#3405
- [ ] Compare `TIMESTAMP` before re-reading an unchanged buffer. (release history)

**Focus / hotkey / positioning (GNOME X11):**
- [ ] Hotkey — only a gsettings custom keybinding (`gnome-clipboard-history-native --show`),
      not `XGrabKey(Super)`; independent of the D-Bus race. #1267/#3616
- [ ] Don't call `SetInputFocus`/`requestActivate` on the popup — grab-focus instead of
      activation bypasses focus-stealing-prevention. #2960/#2993
- [ ] Override-redirect popup (no WM hide animation) — keep as protection against #1729.
- [ ] Compute the popup position from the **RandR CRTC** under the cursor, **not** from
      `_NET_WORKAREA` (which is one for the whole desktop → garbage on secondary
      monitors). Set the position explicitly on every show. #3608/#1982
- [ ] Keep the cursor coordinates and popup position in the same units (raw X px);
      account for GTK scale only for content size. #2744

**Wayland:**
- [ ] Stay X11-only. GNOME Wayland — crossed off (mutter gives no data-control,
      positioning, XTEST, focus). #1243/#2847/#3237/#3331
- [ ] If Wayland ever — only wlroots, as a separate backend: `ext-data-control-v1` +
      `zwlr-layer-shell` + `ydotool`/`wtype`, moving the blocking pipe reads of the
      buffer off the main thread. #3294/#3125/#2747/#2953
