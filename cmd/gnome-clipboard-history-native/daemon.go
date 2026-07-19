//go:build linux

// daemon.go — resident part: single instance on a socket, backend initialization
// (X11/Wayland), clipboard watcher and history (in-memory only).
package main

import (
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/jezek/xgb/xtest"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/keybind"

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const maxHistory = 25 // how many entries we keep in memory

var (
	clipboard *gtk.Clipboard // CLIPBOARD: we both watch it and own it when pasting
	primary   *gtk.Clipboard // PRIMARY: needed for Shift+Insert paste into VTE terminals (Wayland)
	history   []*clipItem    // clipboard history, newest on top (in-memory only)

	// When we ourselves put an entry into the clipboard on paste, we don't want to move it
	// to the top of the history (the selected item must stay in place). We mark such a self-set
	// with the entry's key (shared for text and image) so that owner-change/XFIXES skips it.
	selfSetKey     string
	selfSetPending bool
)

func runDaemon() {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		log.Fatal("daemon is already running")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if isWayland() {
		// Wayland: xgb grabs/XTEST don't work — paste is done via our own
		// uinput device (see internal/uinput). History — via the XWayland bridge
		// (startClipboardWatchWayland in wayland.go).
		if err := uinput.Init(); err != nil {
			log.Printf("uinput unavailable (%v). Paste on Wayland disabled — "+
				"run once: gnome-clipboard-history-native --setup-input", err)
		}
	} else {
		var err error
		X, err = xgbutil.NewConn()
		if err != nil {
			log.Fatalf("no connection to X: %v", err)
		}
		keybind.Initialize(X)
		if err := xtest.Init(X.Conn()); err != nil {
			log.Fatalf("XTEST init: %v", err)
		}
		if cs := keybind.StrToKeycodes(X, "Control_L"); len(cs) > 0 {
			ctrlKey = cs[0]
		}
		if vs := keybind.StrToKeycodes(X, "v"); len(vs) > 0 {
			vKey = vs[0]
		}
		if ss := keybind.StrToKeycodes(X, "Shift_L"); len(ss) > 0 {
			shiftKey = ss[0]
		}
		spareKey = findSpareKeycode()
	}

	gtk.Init(nil)
	applyCSS()
	startClipboardWatch()
	startSocketListener()
	watchConfig() // live hotkey reload from ~/.config/<name>/config.{json,yaml,toml}

	log.Printf("daemon (GTK, %s): listening on socket %s — waiting for --show", sessionKind(), sockPath())
	gtk.Main()
	uinput.Close() // no-op on X11
}

func startSocketListener() {
	os.Remove(sockPath())
	ln, err := net.Listen("unix", sockPath())
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				continue
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			c.Close()
			switch {
			case strings.HasPrefix(string(buf[:n]), "show"):
				glib.IdleAdd(func() bool {
					if isWayland() {
						showPopupWayland()
					} else {
						showPopup()
					}
					return false
				})
			case strings.HasPrefix(string(buf[:n]), "quit"):
				glib.IdleAdd(func() bool { gtk.MainQuit(); return false })
			}
		}
	}()
}

// ---------- clipboard watcher ----------

func startClipboardWatch() {
	var err error
	clipboard, err = gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
	if err != nil {
		log.Fatalf("clipboard: %v", err)
	}
	// GTK owner-change + WaitForContents("image/png") returns empty data for
	// binary formats (GetData len=0). We use XFIXES+xgb directly — the same
	// mechanism as for the Wayland backend: we read image/png via ConvertSelection
	// without a GTK intermediary. Works on both X11 and Wayland (both connected via $DISPLAY).
	startClipboardWatchWayland()
}

// ingestText puts new clipboard text into the history, skipping our own pastes
// (self-set) so the selected entry doesn't jump to the top. Call only from the GTK thread.
func ingestText(txt string) {
	txt = cleanCaptured(txt)
	if strings.TrimSpace(txt) == "" {
		return
	}
	key := textKey(txt)
	if selfSetPending && key == selfSetKey {
		selfSetPending = false // this is our own paste — don't touch the order
		return
	}
	addItem(&clipItem{kind: kindText, text: txt, key: key})
}

// cleanCaptured normalizes clipboard text before it enters history: it strips a
// trailing NUL terminator that some apps append (CopyQ #681), otherwise shown as a
// garbage char in the popup. Newlines are preserved — apps rely on a trailing \n,
// and the terminal auto-run hazard is handled at paste time instead (see finish()).
func cleanCaptured(s string) string {
	return strings.TrimRight(s, "\x00")
}

// ingestImage puts a new image into the history, skipping our self-sets. png is
// the canonical bytes (already copied), pix is their decoded pixbuf.
// Call only from the main GTK thread.
func ingestImage(png []byte, pix *gdk.Pixbuf) {
	if len(png) == 0 || pix == nil {
		return
	}
	key := imageKey(png)
	if selfSetPending && key == selfSetKey {
		selfSetPending = false
		return
	}
	addItem(&clipItem{kind: kindImage, png: png, pix: pix, key: key})
}

// setClipboard makes the daemon the owner of CLIPBOARD with text s. While the daemon is
// alive, it serves paste requests itself — so no external xsel/xclip is needed.
// Called only when pasting the selected entry, so we mark a self-set: the owner-change that
// follows with this text must not move the entry to the top of the history.
func setClipboard(s string) {
	if clipboard != nil {
		selfSetKey = textKey(s)
		selfSetPending = true
		clipboard.SetText(s)
	}
}

// setClipboardImage makes the daemon the owner of CLIPBOARD with image pix (GTK itself serves
// image/png on request from the pasting application). png is only needed for the self-set mark:
// the owner-change that follows with the same image must not move the entry to the top.
func setClipboardImage(png []byte, pix *gdk.Pixbuf) {
	if clipboard != nil && pix != nil {
		selfSetKey = imageKey(png)
		selfSetPending = true
		clipboard.SetImage(pix)
	}
}

// setPrimary makes the daemon the owner of PRIMARY with text s. Needed on Wayland: VTE
// terminals on Shift+Insert paste exactly PRIMARY (not CLIPBOARD), so without this
// the console would paste the old selection contents rather than the selected entry.
// We don't monitor PRIMARY (we take history from CLIPBOARD), so no self-set mark is needed.
func setPrimary(s string) {
	if primary == nil {
		if p, err := gtk.ClipboardGet(gdk.SELECTION_PRIMARY); err == nil {
			primary = p
		}
	}
	if primary != nil {
		primary.SetText(s)
	}
}

func sockPath() string {
	// GCHN_SOCK — a separate socket for a dev instance, so it doesn't collide with
	// the installed daemon (a shared socket is the only single-instance conflict).
	if s := os.Getenv("GCHN_SOCK"); s != "" {
		return s
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "gnome-clipboard-history-native.sock")
}
