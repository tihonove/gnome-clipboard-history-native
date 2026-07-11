# CLAUDE.md

Guidelines for working on this repository. Design details are in
[ARCHITECTURE.md](./ARCHITECTURE.md).

## What this is

Native clipboard history for **GNOME (X11 + basic Wayland)** — a `Win+V`
equivalent. A resident GTK daemon written in Go: on a hotkey (`Super+Ctrl+V`) it
shows a popup list of entries (Yaru theme); arrows/PageUp/PageDown/Home/End move
the selection, `Enter` pastes the selected entry into the active window, `Escape`
closes it.

One binary (`cmd/gnome-clipboard-history-native`), two backends, chosen at runtime (`isWayland()` in
`cmd/gnome-clipboard-history-native/wayland.go`):
- **X11** (`x11.go`) — full-featured: popup at the cursor, xgb grab, XTEST paste,
  real history capture.
- **Wayland** (`wayland.go` + `internal/uinput`) — centered popup, standard GTK
  signals, paste via `/dev/uinput` (`Shift+Insert`). History — via an **XWayland
  bridge**: the standard wl path can't see another app's clipboard in the
  background (no `data-control`), but mutter mirrors the clipboard into the X11
  CLIPBOARD, and we catch XFIXES notifications over XWayland and read the
  selection in-process via xgb (no external utilities — like CopyQ).

## Environment and requirements

- **X11**: XTEST injection and override-redirect windows.
- **Wayland (GNOME)**: the popup is a regular toplevel (it gets focus); paste goes
  through `/dev/uinput` (needs write access). Access is configured
  **automatically**: `--install` on Wayland installs a udev rule (escalating via
  pkexec/sudo), or you can do it separately with `gnome-clipboard-history-native --setup-input`;
  `.deb`/`.rpm` drop the same rule themselves (postinst runs as root). The rule is
  combined — `uaccess` (instant ACL for the active user, no relogin) + `GROUP=input`
  as a fallback. The single source of the rule's text is `uinput_setup.go`. See
  `cmd/gnome-clipboard-history-native/uinput_setup.go`. Configure layout switching via **GNOME
  Tweaks** (not Settings), otherwise the modifiers get "swallowed" and the
  hotkey/paste break on the 2nd layout.
- **GNOME (mutter)**: the hotkey is bound through GNOME itself (a gsettings custom
  keybinding), because mutter holds `Super` and the application can't intercept it
  via XGrabKey. Works on both X11 and Wayland.
- Build: **Go 1.23+**, **cgo**, `libgtk-3-dev`. Runtime: GTK3; on X11 — an X server
  with XTEST, on Wayland — access to `/dev/uinput` (see auto-setup above).

## Build and dev cycle

```sh
go build -o gnome-clipboard-history-native ./cmd/gnome-clipboard-history-native

# restart the daemon after a rebuild (typical dev loop):
# pkill -f, not -x: the name is longer than 15 chars — the kernel truncates comm, so match on cmdline.
pkill -f gnome-clipboard-history-native; sleep 0.3; rm -f "$XDG_RUNTIME_DIR/gnome-clipboard-history-native.sock"
setsid ./gnome-clipboard-history-native >>/tmp/gnome-clipboard-history-native.log 2>&1 &   # daemon log is here
```

- Client/"call" (what the GNOME hotkey does): `./gnome-clipboard-history-native --show`.
- **Strictly one instance** — the daemon checks the socket
  `$XDG_RUNTIME_DIR/gnome-clipboard-history-native.sock` before starting. Always `pkill` the old one
  before launching a new one.

## How to test (there are no automated tests)

Self-testing without a physical keyboard — synthetic keys get through, because
input goes via an xgb grab on root:

```sh
./gnome-clipboard-history-native --show; sleep 0.4
xdotool key --clearmodifiers Down; xdotool key --clearmodifiers Return
xsel -ob        # verify the selected entry's text landed in the clipboard
```

What matters to check physically (synthetics can't cover it reliably): the actual
paste into a **console** (kitty → Ctrl+Shift+V) and into **both layouts** (the
layout bug has already been fixed by remapping the keycode).

## Structure

Standard Go layout: the binary in `cmd/`, private packages in `internal/`.
Module — `github.com/tihonove/gnome-clipboard-history-native`.

- `cmd/gnome-clipboard-history-native/` — package main, split across files:
  - `main.go` — entry point, flag dispatch, `version`;
  - `client.go` — the `--show` "call" (triggered by the GNOME hotkey);
  - `install.go` — `--install`/`--uninstall`, gsettings helpers;
  - `daemon.go` — the resident part: socket, backend initialization, clipboard
    watcher, text/image capture, `setClipboard`/`setClipboardImage`;
  - `item.go` — the history entry model `clipItem` (text OR image), dedup by key,
    image byte budget, PNG→pixbuf decode;
  - `popup.go` — shared across backends: CSS, the content builder `buildPopupBox()`
    (text — Label, image — cover render into a `DrawingArea` via cairo);
  - `x11.go` — the **X11 backend**: popup at the cursor, grab/poll, XTEST paste via
    a spare keycode, positioning;
  - `wayland.go` — the **Wayland backend**: `isWayland()`, `showPopupWayland()`,
    `finishWayland()`, the XWayland history bridge.
  - `uinput_setup.go` — one-time setup of access to `/dev/uinput`
    (`--setup-input`/`--remove-input`): a udev rule + pkexec/sudo escalation via
    re-exec of its own binary (the hidden `__setup-input-root`/`__remove-input-root`).
- `internal/uinput/` — a virtual keyboard via `/dev/uinput` (paste on Wayland):
  `Init()`, `Close()`, `InjectPaste()`, `InjectPasteCtrlV()` (for images),
  `HasAccess()`, `DevPath`.
- `.golangci.yml` — golangci-lint config (runs in CI; intentional
  fire-and-forget calls are excluded pointwise — see comments in the config).

## Invariants and pitfalls (do NOT regress)

- **Hotkey — only through GNOME** (gsettings), not XGrabKey. mutter holds Super.
  The binding is `Super+Ctrl+V` (`hotkeyBinding`). Works on both X11 and Wayland.
- **Two backends, chosen at runtime** via `isWayland()`. The branch is at exactly
  two seams: initialization in `runDaemon` and the `show` dispatch. X11 functions
  (grab/poll/XTEST/spare/positioning/isTerminal) are NOT called on Wayland, and
  vice versa — don't mix the paths.

### X11 backend (`x11.go`)
- **Input — via xgb, not GTK.** GNOME denies keyboard focus to the popped-up
  `GTK_WINDOW_POPUP` (focus-stealing prevention), so we grab the keyboard via
  `xproto.GrabKeyboard` on root and read keys by polling in `glib.TimeoutAdd`. GTK
  is only needed for drawing (the Yaru theme).
- **Keyboard grab — with retries:** right after Super, mutter still holds the
  keyboard (`AlreadyGrabbed`), releasing it once the keys are released.
- **Paste — native XTEST via a spare keycode** (detailed comment above
  `setupSpareKey`). The real keycode for 'v' in the Russian layout produces "м". We
  keep a spare unused keycode, mapped to 'v' in all groups, and send it. We map it
  when the popup OPENS and restore it to NoSymbol ~300ms after it closes. We can't
  keep it mapped permanently — mutter would route Super+V to it. We can't restore it
  immediately either — Qt/Electron wouldn't keep up. Do NOT spawn `xdotool` (visible
  delay).
- **Terminals get Ctrl+Shift+V** (detected via `WM_CLASS`), everyone else gets Ctrl+V.
- All X calls are from the main GTK thread. The socket goroutine only wakes it via
  `glib.IdleAdd`.

### Wayland backend (`wayland.go` + `internal/uinput`)
- **Popup — a regular `GTK_WINDOW_TOPLEVEL`.** Under Wayland it DOES get focus, so
  we read keys via standard GTK signals (`key-press-event`);
  arrows/PageUp/Home/End go to the focused `ListBox` natively — we only intercept
  Enter/Escape. No xgb grab. Don't turn it back into an override-redirect — there'd
  be no focus.
- **Hiding — on `focus-out-event`** (there's no click-away via pointer grab on
  Wayland).
- **Position — centered** (`WIN_POS_CENTER_ALWAYS`). At-cursor / in-active-window
  positioning (like X11 `popupXY`) is IMPOSSIBLE for a native toplevel: mutter
  ignores `gtk_window_move`, and the cursor can't be obtained reliably
  (`QueryPointer` over XWayland is only fresh above an XWayland window;
  `_NET_ACTIVE_WINDOW` for native wl windows = `None`). The only positionable popup
  is an override-redirect via XWayland (`GDK_BACKEND=x11`), but `XGrabKeyboard` on
  root under mutter returns `Success` and won't deliver keys (focus goes to the wl
  window) — which is why the backend uses a native toplevel. The best achievable is
  centering on the monitor mutter picks (usually the active one). Don't try to place
  it at the cursor — same class of limitation as `data-control`/XTEST.
- **Paste — `Shift+Insert` via `/dev/uinput`** (not XTEST — it doesn't reach native
  Wayland windows). `Insert` is a function key, layout-independent. IMPORTANT:
  `Shift+Insert` takes CLIPBOARD in GUI fields but PRIMARY in VTE terminals, so on
  paste we put the selected entry into BOTH selections (`setClipboard` +
  `setPrimary` in `finishWayland`). Without PRIMARY, the console would paste the old
  mouse selection instead of the chosen entry. Window detection under Wayland isn't
  needed (and is impossible). The device is created once at daemon startup and
  reused (see the comment in `internal/uinput`). Env override `GCHN_PASTE=ctrlv`.
- **Access to `/dev/uinput` — one-time privileged setup** (`uinput_setup.go`), NOT
  on every paste. Check `uinput.HasAccess()` before escalating — if access already
  exists (e.g. `.deb` dropped the rule, or the node is world-writable), do NOT invoke
  sudo/pkexec. `--install` on Wayland configures it itself, but only if
  `!HasAccess() && !ruleInstalled()`. The privileged write is done by a re-exec of
  its own binary (a hidden subcommand), not a shell heredoc. Wayland by design
  forbids injecting into other apps' windows — that's the price, same as CopyQ (same
  `/dev/uinput` via ydotool); there's no zero-setup path without a udev rule.
- **History — via the XWayland bridge** (`startClipboardWatchWayland`). The
  background wl path can't see another app's clipboard (no `data-control`), so we
  monitor the X11 CLIPBOARD that mutter mirrors the clipboard into: a separate xgb
  connection to XWayland, XFIXES owner-change notifications. On a notification we ask
  for `TARGETS` and take **text** (`UTF8_STRING`) or an **image** (`image/png`),
  reading the value ourselves (`ConvertSelection`→`SelectionNotify`→`GetProperty`)
  — no external utilities, like CopyQ. **Large values (screenshots) come in via
  INCR** — we read them in chunks (`readSelectionBytes`): a property of type `INCR`
  is a marker, deleting it signals the owner to send chunks, then for each one —
  `PropertyNotify(NewValue)`, an empty chunk = end (needs `EventMaskPropertyChange`
  on the requestor window). Event-driven (not polling) — fast copies aren't lost.
  The separate connection lives in its own goroutine; `ingest*` goes through
  `glib.IdleAdd` (the general invariant "X calls on the shared connection are from
  the GTK thread" isn't violated: this is a SEPARATE connection). Requires only
  XWayland (`$DISPLAY`).
- **A self-set on paste doesn't move history:** `setClipboard`/`setClipboardImage`
  tag the entry with a hash key (`selfSetPending`/`selfSetKey`; text vs image — a
  shared mechanism), and `ingestText`/`ingestImage` skip it — the selected entry
  stays in place. A shared path for X11 (owner-change) and Wayland (XFIXES).

### General
- **The GTK daemon itself owns the clipboard** (`clip.SetText`) while it's alive;
  external `xsel`/`xdotool` aren't needed at runtime on either X11 or Wayland
  (reading history on Wayland is in-process via xgb).
- **gotk3 is pinned to v0.6.3.** v0.6.4 doesn't build (a missing import in gdk). Don't
  upgrade blindly.

## Commits (important for changelog and releases)

We use **Conventional Commits**. The type determines whether a commit lands in the
changelog (assembled by git-cliff per `cliff.toml`).

Format: `type(scope): short description`. **Commit messages are written in English**
(subject and body) — the changelog is assembled from them, so releases come out in
English too.

**Land in the changelog** (user-facing changes):
- `feat:` — new functionality → "🚀 Features" section.
- `fix:` — bug fix → "🐛 Bug Fixes".
- `perf:` — performance → "⚡ Performance".
- any type with `scope` `security` (e.g. `fix(security):`) or a mention of security
  in the body → "🛡️ Security".
- **Breaking change**: `!` after the type (`feat!:`) or `BREAKING CHANGE:` in the
  body → "💥 Breaking Changes" section.

**Do NOT land in the changelog** (intentionally hidden): `chore:`, `docs:`,
`refactor:`, `test:`, `ci:`, `build:`, `style:` and anything non-conventional.

**Reserved:** `chore(release): vX.Y.Z` is created only by the bump-version workflow
— don't craft such commits by hand.

Other: commits are grouped by meaningful chunks; the body explains "why", not just
"what". **Commit messages, code comments, and documentation (CLAUDE.md,
ARCHITECTURE.md) are all written in English** (as is the whole project).

## Releases

The version is stored in the `VERSION` file and baked into the binary at build time
(`-ldflags "-X main.version=…"`; locally — `dev`). Check with:
`gnome-clipboard-history-native --version`.

Process (GitHub Actions):
1. Manually run the **Bump Version** workflow (`workflow_dispatch`, choose
   patch/minor/major) → it bumps `VERSION`, commits `chore(release): vX.Y.Z`, tags
   and pushes.
2. Pushing a `vX.Y.Z` tag triggers **Release** (`build → package → release →
   apt-repo`): the binary (`build.yml`), `.deb` (nfpm), changelog (git-cliff),
   GitHub Release (binary + `.deb`) and publication to the apt repository — see
   "Distribution" below.

Requires the repository secret **`REPOSITORY_PAT`** (a PAT with `contents:write`) —
otherwise the tag push from bump-version won't trigger release (a GITHUB_TOKEN
limitation). `ci.yml` runs build/`go vet`/golangci-lint on push to main and PRs.
**golangci-lint includes `gofmt`** — run `gofmt -l .` before pushing (empty = OK),
otherwise CI goes red.

## Distribution (apt repository)

Installed via **our own signed apt repository on GitHub Pages** (the `gh-pages`
branch), not a PPA. Everything is automated in `release.yml`.

**Package (`nfpm`, `packaging/nfpm.yaml`):** puts the binary in
`/usr/bin/gnome-clipboard-history-native` and a **static udev rule** (`uaccess`) in
`/usr/lib/udev/rules.d/` — the same `pkgUdevRulePath` that `--setup-input` checks, so
`gnome-clipboard-history-native --install` sees ready access and doesn't escalate. postinst only
activates udev (`modprobe`/`udevadm`); the per-user part (hotkey, autostart, daemon
start) is done by **`gnome-clipboard-history-native --install`** within the session — it's impossible
from the root postinst (the postinst does NOT start the daemon and does NOT call
`--install`: there was a bug with `/root`).

**Release pipeline** (`release.yml` on a tag): `build` (binary) → `package` (`.deb`)
→ `release` (GitHub Release: binary + `.deb` + changelog) and `apt-repo` (`reprepro`
signs, pushes state to `gh-pages` and **deploys the site via Actions**). reprepro
keeps one version (the pool doesn't bloat), re-pushing the same tag doesn't fail the
job (an empty commit is tolerated, the deploy goes ahead anyway).

**Pages deploy — via Actions (`build_type=workflow`), NOT branch serving.** The
`gh-pages` branch is only reprepro's persistent state store (pool/db/dists). The
served tree (dists/ + pool/ + static, without conf/db/.git) is uploaded by the
`apt-repo` job as an artifact (`upload-pages-artifact`) and deployed synchronously
(`deploy-pages`) in the SAME run — status is visible immediately, and there's no
longer a separate flaky "pages build and deployment" workflow. A manual redeploy of
the current state without a release — `pages.yml` (`workflow_dispatch`); it's also
needed for a one-time cutover when switching legacy→workflow, so the site doesn't go
down.

**Installation (for the user):** both scripts are thin (the logic is in the binary),
**run without sudo** (they escalate themselves where needed), and at the end they
call `gnome-clipboard-history-native --install`:
- apt + auto-updates: `curl -fsSL <pages>/install.sh | sh`;
- without apt: `curl -fsSL <pages>/install-standalone.sh | sh` (downloads the binary).

**One-time repo setup (outside the code, by hand in Settings):** the secret
`APT_GPG_PRIVATE_KEY` (the private signing key; the public one is exported by CI);
Pages → **Source: GitHub Actions** (`build_type=workflow`, not "Deploy from a
branch"); Actions → Read and write permissions (otherwise the push to `gh-pages`
fails). **The `github-pages` environment must allow deploys from tags**: the release
runs from the `vX.Y.Z` tag, but the environment's default policy allows only
branches — without a tag policy `apt-repo` fails with "Tag … is not allowed to
deploy to github-pages". Add the pattern:
`gh api -X POST repos/OWNER/REPO/environments/github-pages/deployment-branch-policies
-f name='v*.*.*' -f type=tag` (or in Settings → Environments → github-pages →
Deployment branch and tags → add a tag rule `v*.*.*`).

**Update pitfall:** `apt upgrade` changes the file on disk, but the running daemon
stays old in memory until restart/login (the root postinst doesn't restart someone
else's session). A naive self-restart would lose the in-memory history — an open
question, **issue #2**.

### Dev instance (test without colliding with the installed one)

The daemon is single-instance on a socket; a parallel dev one comes up via env
(`sockPath`/`install.go`):
- `GCHN_SOCK` — its own socket;
- `GCHN_NAME` — its own gsettings slot and **no autostart** (`isDevInstance`);
- `GCHN_HOTKEY` — its own key; the socket is passed through into the hotkey command.

Once: `GCHN_SOCK=… GCHN_HOTKEY='<Super><Control>b' GCHN_NAME=gnome-clipboard-history-native-dev
./gnome-clipboard-history-native-dev --install`. After that, each session is simply:
`GCHN_SOCK=… ./gnome-clipboard-history-native-dev`.
