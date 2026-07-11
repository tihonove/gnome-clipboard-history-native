# Distribution & marketing plan

Where to post this project and how to promote it. The niche is narrow but sharp:
**a `Win+V` equivalent for Ubuntu/GNOME** — native, Yaru-styled, works on **both
Wayland and X11**, installs with one command. GNOME ships no clipboard history out of
the box; the usual answers (GPaste, Clipboard Indicator, CopyQ) are extensions, look
non-native, or fight Wayland.

## The hook (what we sell on)

Two framings for two audiences:

- **For users** — "clipboard history for Ubuntu/GNOME, native, Yaru-styled, works on
  Wayland and X11, one-line install."
- **For developers** — **"how I built clipboard history on Wayland without
  `data-control`"** (XWayland + XFIXES bridge, INCR for screenshots, XTEST/uinput
  paste, spare-keycode for layout independence). This is the angle that earns
  attention — "Wayland clipboard history" is a known-hard problem. "Yet another
  clipboard manager" is not; the technical story is.

**Strategy: lead with the technical hook, not "look, a clipboard manager."**

## Decisions already made

- **No Snap / Flatpak.** The app's whole job (read every app's clipboard, inject
  keystrokes, register a global hotkey, autostart) is exactly what strict
  sandboxing forbids. A *classic* snap is technically possible but needs Canonical's
  manual approval, isn't one-click in GNOME Software, drops the sandbox anyway, and
  undercuts the "native Yaru look" selling point (snaps don't pick up the host
  theme). Stick with the signed **apt repository** (already built: one-liner install,
  auto-updates).
- **No Show HN.** Not the right scale for this project (by choice).

## To-do — prep before promoting

Do these first; posting a rough project wastes the shot.

- [ ] **Demo GIF** of the flow: `Super+Ctrl+V` → arrows → `Enter` → pasted. Matters
      more than static screenshots for Reddit/blogs. (Screenshots already done.)
- [ ] **Comparison section in README** — "vs GPaste / CopyQ / Clipboard Indicator".
      It's the first question in every comment thread; pre-empt it.
- [ ] **GitHub topics**: `gnome`, `ubuntu`, `wayland`, `x11`, `clipboard-manager`,
      `gtk`, `clipboard-history`. Free discovery in GitHub search.
- [ ] **AlternativeTo** listing — as an alternative to GPaste / CopyQ / Windows
      Clipboard. A traffic channel in itself.
- [ ] **Polish**: document/handle the `apt upgrade` restart pitfall (issue #2);
      consider an arm64 build (widens the audience).

## To-do — content to create

- [ ] **Technical blog post** about the Wayland clipboard-history bridge (the hook).
      Publish on dev.to / a personal blog. Becomes the "proof" link every other
      channel points to. Source material: `ARCHITECTURE.md`, `CLAUDE.md`.

## To-do — where to post (highest ROI first)

- [ ] **Ubuntu/Linux blogs — submit a tip**: OMG! Ubuntu, It's FOSS, 9to5Linux.
      "Native clipboard manager for Ubuntu with Wayland support" is exactly their
      format. One writeup beats ten Reddit threads.
- [ ] **Reddit**: r/gnome, r/Ubuntu, r/linux, r/unixporn (if shot nicely). Post as
      "I built…", follow each sub's self-promotion rules (spacing, disclose
      authorship).
- [ ] **Mastodon / Fediverse** (fosstodon.org), tags `#Linux #GNOME #Ubuntu
      #Wayland`. The core Linux crowd lives there.
- [ ] **Community Q&A (evergreen SEO)**: GNOME Discourse, Ubuntu Discourse, Ask
      Ubuntu — answer existing "clipboard history GNOME/Wayland" questions, honestly
      disclosing that you're the author. Slow but long-lived Google traffic.
- [ ] **Awesome-list PRs**: `awesome-linux-software`, `awesome-gnome` (clipboard
      section). Slow, but compounds.
- [ ] **Lobsters** (tags `linux`, `gui`) — only if you have an invite.

## Suggested sequence

1. **Prep**: GIF + comparison section + GitHub topics + AlternativeTo.
2. **Content**: write the Wayland-bridge technical post.
3. **Push** (same window): blog tips (OMG!/It's FOSS/9to5Linux) + Reddit + Mastodon.
4. **Long tail**: Ask Ubuntu / Discourse answers, awesome-list PRs.
