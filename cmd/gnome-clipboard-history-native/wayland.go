//go:build linux

// wayland.go — Wayland backend for the popup and pasting (for GNOME Wayland).
//
// Differences from the X11 path (x11.go) are kept to a minimum — we reuse the shared
// framework (buildPopupBox, setClipboard, history, CSS, socket/daemon). Wayland
// specifics:
//   - Popup is a regular GTK_WINDOW_TOPLEVEL: under Wayland it receives keyboard focus,
//     so we read keys via the standard GTK signals (no xgb grab on root).
//   - List navigation is native: arrows/PageUp/Home/End go to the focused ListBox,
//     we intercept only Enter/Escape.
//   - Hiding on focus loss — focus-out-event (there is no click-outside via pointer-grab).
//   - Position — centered: under-cursor / in-active-window positioning (as on X11,
//     see popupXY in x11.go) is IMPOSSIBLE on a native Wayland toplevel — the same class
//     of limitation as data-control and XTEST below. Reasons: (1) mutter ignores
//     gtk_window_move — toplevel coordinates are set by the compositor, not the client;
//     (2) the cursor cannot be obtained reliably (QueryPointer via XWayland is fresh only
//     over an XWayland window, and _NET_ACTIVE_WINDOW for native wl windows = None). The
//     only positionable popup is override-redirect via XWayland (GDK_BACKEND=x11), but
//     XGrabKeyboard on root under mutter returns Success yet won't deliver keys (focus
//     goes to the wl window) — exactly the reason this backend uses a native toplevel.
//     Best achievable — centered on the monitor mutter picks (usually the active/under-
//     cursor one).
//   - Paste — uinput Shift+Insert (see internal/uinput): layout-independent and
//     universal for terminals and GUI, so window/terminal detection is not needed
//     (and is impossible under Wayland anyway).
//   - History — via the XWayland bridge (startClipboardWatchWayland): the background wl
//     path doesn't see another app's buffer (no data-control), but mutter mirrors the
//     buffer into the X11 CLIPBOARD, and we catch XFIXES notifications and read the
//     selection in-process via xgb (the same way CopyQ does).
package main

import (
	"log"
	"os"
	"unicode/utf8"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xfixes"
	"github.com/jezek/xgb/xproto"

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const wlPasteDelayMs = 120 // let focus return to the previous window before injection

var wlFinishing bool

// isWayland — whether we are running in a Wayland session (otherwise the X11 path).
func isWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("XDG_SESSION_TYPE") != "x11"
}

func sessionKind() string {
	if isWayland() {
		return "wayland"
	}
	return "x11"
}

// selectionAtoms — the set of X selection atoms the bridge needs (interned once).
type selectionAtoms struct {
	clip, prop, targets, utf8, png, incr xproto.Atom
}

const maxSelBytes = maxImageBytes // safeguard on the size of a single read (including INCR)

// startClipboardWatchWayland — background clipboard monitoring under GNOME Wayland via
// XWayland. The standard wl path doesn't see other apps' copies in the background (no
// data-control), but mutter mirrors the Wayland buffer into the X11 CLIPBOARD, so an X
// client receives XFIXES notifications about owner changes (including from native
// Wayland apps). On an owner change we ask for TARGETS and take text (UTF8_STRING) or an
// image (image/png) — the same as CopyQ, without external xsel. Large data (screenshots)
// arrives via INCR — we read it in chunks (see readSelectionBytes). A separate xgb
// connection (not GTK) runs in its own goroutine; ingest goes through IdleAdd. Requires
// only XWayland ($DISPLAY).
func startClipboardWatchWayland() {
	c, err := xgb.NewConn() // XWayland via $DISPLAY
	if err != nil {
		log.Printf("Wayland history unavailable: no XWayland (%v)", err)
		return
	}
	if err := xfixes.Init(c); err != nil {
		log.Printf("Wayland history: xfixes init: %v", err)
		c.Close()
		return
	}
	xfixes.QueryVersion(c, 5, 0).Reply() // mandatory handshake before xfixes calls
	root := xproto.Setup(c).DefaultScreen(c).Root

	a := selectionAtoms{
		clip:    internAtom(c, "CLIPBOARD"),
		prop:    internAtom(c, "GCHN_SEL"), // where the owner puts the converted value
		targets: internAtom(c, "TARGETS"),
		utf8:    internAtom(c, "UTF8_STRING"),
		png:     internAtom(c, "image/png"),
		incr:    internAtom(c, "INCR"),
	}
	if a.clip == 0 || a.prop == 0 || a.targets == 0 || a.utf8 == 0 {
		log.Println("Wayland history: failed to obtain atoms")
		c.Close()
		return
	}

	// Invisible requestor window: the owner sends it SelectionNotify and puts data in prop.
	// PropertyChange is needed for INCR (we wait for PropertyNotify on each chunk).
	reqWin, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, 0, reqWin, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange})

	xfixes.SelectSelectionInput(c, root, a.clip, xfixes.SelectionEventMaskSetSelectionOwner)
	log.Println("Wayland history: watching the clipboard via XWayland (XFIXES)")

	go func() {
		for {
			ev, err := c.WaitForEvent()
			if err != nil {
				continue
			}
			if ev == nil { // connection closed
				return
			}
			switch e := ev.(type) {
			case xfixes.SelectionNotifyEvent:
				// the CLIPBOARD owner changed → first ask for the list of available targets
				xproto.ConvertSelection(c, reqWin, a.clip, a.targets, a.prop, e.SelectionTimestamp)
			case xproto.SelectionNotifyEvent:
				if e.Property == 0 { // the owner did not provide the requested target
					continue
				}
				handleSelectionNotify(c, reqWin, a, e)
			}
		}
	}()
}

// handleSelectionNotify handles the owner's reply to ConvertSelection. From e.Target we
// tell the stage: TARGETS → choose text/image and request them; UTF8/png → read the
// value (with INCR support) and put it into history. Called from the bridge goroutine.
func handleSelectionNotify(c *xgb.Conn, win xproto.Window, a selectionAtoms, e xproto.SelectionNotifyEvent) {
	switch e.Target {
	case a.targets:
		targets := readAtomList(c, win, a.prop)
		switch {
		case hasAtom(targets, a.utf8): // text takes priority over image
			xproto.ConvertSelection(c, win, a.clip, a.utf8, a.prop, e.Time)
		case a.png != 0 && hasAtom(targets, a.png):
			xproto.ConvertSelection(c, win, a.clip, a.png, a.prop, e.Time)
		}
	case a.utf8:
		data, ok := readSelectionBytes(c, win, a.prop, a.incr)
		if !ok || !utf8.Valid(data) {
			return
		}
		txt := string(data)
		glib.IdleAdd(func() bool { ingestText(txt); return false })
	case a.png:
		data, ok := readSelectionBytes(c, win, a.prop, a.incr)
		if !ok {
			return
		}
		glib.IdleAdd(func() bool {
			pix, err := pixbufFromPNG(data)
			if err == nil && pix != nil {
				ingestImage(data, pix)
			}
			return false
		})
	}
}

func internAtom(c *xgb.Conn, name string) xproto.Atom {
	r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
	if err != nil || r == nil {
		return 0
	}
	return r.Atom
}

func hasAtom(list []xproto.Atom, a xproto.Atom) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// readAtomList reads and deletes an atom-list property (the reply to ConvertSelection
// TARGETS). Values are 32-bit atoms; we parse via xgb.Get32 (accounts for the server's
// byte order).
func readAtomList(c *xgb.Conn, win xproto.Window, prop xproto.Atom) []xproto.Atom {
	r, err := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<16).Reply()
	if err != nil || r == nil || r.Format != 32 {
		return nil
	}
	out := make([]xproto.Atom, 0, len(r.Value)/4)
	for i := 0; i+4 <= len(r.Value); i += 4 {
		out = append(out, xproto.Atom(xgb.Get32(r.Value[i:])))
	}
	return out
}

// readSelectionBytes reads and deletes property prop of window win (the result of
// ConvertSelection), returning the raw bytes. Large values arrive via INCR: a property of
// type INCR is the start marker, and deleting the property itself (delete=true) signals
// the owner to send chunks; then for each chunk the owner sends PropertyNotify(NewValue),
// an empty chunk marks the end of the transfer. Called synchronously from the bridge
// goroutine, so it can safely pump events itself.
func readSelectionBytes(c *xgb.Conn, win xproto.Window, prop, incrAtom xproto.Atom) ([]byte, bool) {
	r, err := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
	if err != nil || r == nil || r.Type == 0 {
		return nil, false
	}
	if r.Type != incrAtom {
		if len(r.Value) == 0 {
			return nil, false
		}
		return append([]byte(nil), r.Value...), true // Value is reused — copy it
	}
	// INCR: collect chunks on PropertyNotify until an empty one arrives.
	var buf []byte
	for {
		ev, werr := c.WaitForEvent()
		if werr != nil {
			continue
		}
		if ev == nil { // connection closed
			return nil, false
		}
		pn, ok := ev.(xproto.PropertyNotifyEvent)
		if !ok || pn.Window != win || pn.Atom != prop || pn.State != xproto.PropertyNewValue {
			continue // not our chunk — ignore stray events during INCR
		}
		cr, cerr := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
		if cerr != nil || cr == nil {
			return nil, false
		}
		if len(cr.Value) == 0 { // empty chunk — end
			return buf, len(buf) > 0
		}
		buf = append(buf, cr.Value...)
		if len(buf) > maxSelBytes { // guard against runaway growth
			return nil, false
		}
	}
}

func showPopupWayland() {
	if win != nil {
		return
	}
	wlFinishing = false

	w, err := gtk.WindowNew(gtk.WINDOW_TOPLEVEL) // regular toplevel: receives focus under Wayland
	if err != nil {
		log.Println("WindowNew:", err)
		return
	}
	w.SetDecorated(false)
	w.SetKeepAbove(true)
	w.SetSkipTaskbarHint(true)
	w.SetSkipPagerHint(true)
	w.SetResizable(false)
	// Monitor center. Wayland won't let us set an under-cursor position (mutter ignores
	// move; cursor/active window can't be obtained) — details in the file header. mutter
	// picks the monitor (usually the active/under-cursor one); the client has no say.
	w.SetPosition(gtk.WIN_POS_CENTER_ALWAYS)
	w.SetTypeHint(gdk.WINDOW_TYPE_HINT_DIALOG)
	if screen, err := gdk.ScreenGetDefault(); err == nil && screen.IsComposited() {
		if vis, err := screen.GetRGBAVisual(); err == nil && vis != nil {
			w.SetVisual(vis)
		}
	}

	w.Add(buildPopupBox()) // content constructor shared with X11; sets up listBox/scrolled

	// We intercept Enter/Escape; arrows pass through to the focused ListBox (native navigation).
	w.Connect("key-press-event", func(_ *gtk.Window, ev *gdk.Event) bool {
		switch gdk.EventKeyNewFromEvent(ev).KeyVal() {
		case gdk.KEY_Return, gdk.KEY_KP_Enter:
			finishWayland(true)
			return true
		case gdk.KEY_Escape:
			finishWayland(false)
			return true
		}
		return false
	})
	// Focus loss → close (so the popup doesn't linger in the window list).
	w.Connect("focus-out-event", func(_ *gtk.Window, _ *gdk.Event) bool {
		finishWayland(false)
		return false
	})

	w.ShowAll()
	win = w

	if listBox != nil {
		if row := listBox.GetRowAtIndex(0); row != nil {
			listBox.SelectRow(row)
		}
		listBox.GrabFocus() // so arrow keys move the selection natively
	}

	// safety: close after 15s if something went wrong
	glib.TimeoutAdd(15000, func() bool {
		if win == w {
			finishWayland(false)
		}
		return false
	})
}

// finishWayland closes the popup and, if paste, puts the selected entry into CLIPBOARD
// and injects the paste (after a pause — so focus can return to the previous window).
func finishWayland(paste bool) {
	if wlFinishing || win == nil {
		return
	}
	wlFinishing = true
	w := win
	win = nil

	var it *clipItem
	if paste && listBox != nil {
		if r := listBox.GetSelectedRow(); r != nil {
			if idx := r.GetIndex(); idx >= 0 && idx < len(history) {
				it = history[idx]
			}
		}
	}
	listBox = nil
	scrolled = nil

	// We grab the clipboard WHILE the popup is still alive and focused: set_selection
	// under Wayland requires a fresh input-serial of the active surface. If done after
	// Destroy (focus already gone), mutter rejects the set — and the old clipboard
	// contents get pasted (the "always the same text" bug).
	//
	// We put it into both CLIPBOARD and PRIMARY: we paste via Shift+Insert, and VTE
	// terminals take PRIMARY for it, not CLIPBOARD (GUI fields take CLIPBOARD). Without
	// PRIMARY, the console would paste the old mouse selection instead of the chosen entry.
	paste = paste && it != nil
	if paste {
		if it.kind == kindImage {
			// An image goes only into CLIPBOARD (PRIMARY/terminals don't take it) and we
			// paste with Ctrl+V — Shift+Insert makes no sense for an image.
			setClipboardImage(it.png, it.pix)
		} else {
			// Text — into both selections: Shift+Insert takes CLIPBOARD in GUI, PRIMARY in VTE.
			setClipboard(it.text)
			setPrimary(it.text)
		}
	}

	w.Destroy()

	if paste {
		img := it.kind == kindImage
		glib.TimeoutAdd(wlPasteDelayMs, func() bool {
			if img {
				uinput.InjectPasteCtrlV()
			} else {
				uinput.InjectPaste()
			}
			return false
		})
	}
}
