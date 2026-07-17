//go:build linux

// x11.go — the entire X11 backend: popup at the cursor (override-redirect), keyboard
// capture via an xgb grab on root with polling, pasting via native XTEST through
// a spare keycode, and window positioning.
package main

import (
	"log"
	"strings"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/icccm"
	"github.com/jezek/xgbutil/keybind"
)

var (
	X        *xgbutil.XUtil
	ctrlKey  xproto.Keycode
	vKey     xproto.Keycode
	shiftKey xproto.Keycode
	spareKey xproto.Keycode // spare keycode for layout-independent pasting
	spareKPK byte           // server's keysyms-per-keycode (global)

	selIdx         int
	targetWin      xproto.Window
	popupX, popupY int // where the window was placed (to detect clicks outside)

	grabTries int
)

// ---------- popup ----------

func showPopup() {
	if win != nil {
		return
	}
	setupSpareKey() // while the popup is open: 'v' on the spare keycode (apps have time to pick up the keymap)
	if tw, err := ewmh.ActiveWindowGet(X); err == nil {
		targetWin = tw
	}

	w, err := gtk.WindowNew(gtk.WINDOW_POPUP) // override-redirect
	if err != nil {
		log.Println("WindowNew:", err)
		return
	}
	w.SetKeepAbove(true)
	w.SetResizable(false)
	// RGBA visual → transparent corners so the window can be rounded (if a compositor is present).
	if screen, err := gdk.ScreenGetDefault(); err == nil && screen.IsComposited() {
		if vis, err := screen.GetRGBAVisual(); err == nil && vis != nil {
			w.SetVisual(vis)
		}
	}

	w.Add(buildPopupBox())

	x, y := popupXY()
	w.Move(x, y)
	w.ShowAll()

	win = w
	popupX, popupY = x, y
	selIdx = 0
	updateSelection()

	grabTries = 0
	tryGrab()

	glib.TimeoutAdd(15000, func() bool {
		if win == w {
			finish(false)
		}
		return false
	})
}

// insidePopup reports whether a click (in screen coordinates) landed inside the popup window's rectangle.
func insidePopup(x, y int) bool {
	if win == nil {
		return false
	}
	w, h := win.GetSize()
	return x >= popupX && x < popupX+w && y >= popupY && y < popupY+h
}

// setSel clamps the index to [0, len-1] and, if it changed, updates the selection.
func setSel(i int) {
	if i < 0 {
		i = 0
	}
	if i > len(history)-1 {
		i = len(history) - 1
	}
	if i != selIdx {
		selIdx = i
		updateSelection()
	}
}

func updateSelection() {
	if listBox == nil {
		return
	}
	row := listBox.GetRowAtIndex(selIdx)
	if row == nil {
		return
	}
	listBox.SelectRow(row)
	row.GrabFocus() // GtkScrolledWindow scrolls the focused child into the visible area

	// Manual fallback scroll (in case focus doesn't move the viewport).
	if scrolled != nil {
		alloc := row.GetAllocation()
		vadj := scrolled.GetVAdjustment()
		y, h := float64(alloc.GetY()), float64(alloc.GetHeight())
		top, page := vadj.GetValue(), vadj.GetPageSize()
		if y < top {
			vadj.SetValue(y)
		} else if y+h > top+page {
			vadj.SetValue(y + h - page)
		}
	}
}

func tryGrab() {
	gk, err := xproto.GrabKeyboard(X.Conn(), false, X.RootWin(), xproto.TimeCurrentTime,
		xproto.GrabModeAsync, xproto.GrabModeAsync).Reply()
	if err == nil && gk.Status == xproto.GrabStatusSuccess {
		// Grab the pointer too — to catch clicks outside the window and close.
		xproto.GrabPointer(X.Conn(), false, X.RootWin(),
			uint16(xproto.EventMaskButtonPress),
			xproto.GrabModeAsync, xproto.GrabModeAsync,
			xproto.Window(0), xproto.Cursor(0), xproto.TimeCurrentTime)
		startKeyPoll()
		return
	}
	grabTries++
	if grabTries >= 100 {
		log.Println("could not grab the keyboard — closing")
		finish(false)
		return
	}
	glib.TimeoutAdd(20, func() bool { tryGrab(); return false })
}

func startKeyPoll() {
	glib.TimeoutAdd(2, func() bool {
		if win == nil {
			return false
		}
		for {
			ev, err := X.Conn().PollForEvent()
			if err != nil || ev == nil {
				break
			}
			switch e := ev.(type) {
			case xproto.KeyPressEvent:
				switch keybind.LookupString(X, e.State, e.Detail) {
				case "Up":
					setSel(selIdx - 1)
				case "Down":
					setSel(selIdx + 1)
				case "Prior", "Page_Up":
					setSel(selIdx - pageStep)
				case "Next", "Page_Down":
					setSel(selIdx + pageStep)
				case "Home":
					setSel(0)
				case "End":
					setSel(len(history) - 1)
				case "Return", "KP_Enter":
					finish(true)
				case "Escape":
					finish(false)
				}
			case xproto.ButtonPressEvent:
				if !insidePopup(int(e.RootX), int(e.RootY)) {
					finish(false) // click outside the window — close
				}
			}
			if win == nil {
				return false
			}
		}
		return win != nil
	})
}

func finish(paste bool) {
	if win == nil {
		return
	}
	w := win
	win = nil
	xproto.UngrabKeyboard(X.Conn(), xproto.TimeCurrentTime)
	xproto.UngrabPointer(X.Conn(), xproto.TimeCurrentTime)

	var it *clipItem
	if paste && selIdx >= 0 && selIdx < len(history) {
		it = history[selIdx]
	}
	w.Hide()                               // remove the window from screen instantly (Destroy is heavier; do it after pasting)
	xproto.GetInputFocus(X.Conn()).Reply() // wait for the ungrab to be processed before pasting

	if it != nil {
		if it.kind == kindImage {
			setClipboardImage(it.png, it.pix)
			pasteInto(false) // paste images with Ctrl+V; terminals don't take them
		} else if it.text != "" {
			term := isTerminal(targetWin) // terminals get Ctrl+Shift+V, everything else Ctrl+V
			txt := it.text
			if term {
				// Don't let a trailing newline auto-run the pasted command in a shell
				// (CopyQ #2573). Only the served copy is trimmed; history keeps the full text.
				txt = strings.TrimRight(txt, "\r\n")
			}
			setClipboard(txt)
			pasteInto(term)
		}
	}
	listBox = nil
	scrolled = nil
	// Heavy window teardown is deferred so the main loop first hands the clipboard
	// to the pasting application (SelectionRequest) instead of waiting on widget destruction.
	glib.IdleAdd(func() bool { w.Destroy(); return false })

	// Return the spare keycode to NoSymbol (otherwise mutter resolves Super+V to it).
	// With a delay — so applications have time to process the paste keypress; and only
	// if the popup hasn't been opened again.
	glib.TimeoutAdd(300, func() bool {
		if win == nil {
			restoreSpareKey()
		}
		return false
	})
}

// isTerminal determines whether the target window is a terminal (by WM_CLASS). Needed to
// pick the paste combination: terminals paste with Ctrl+Shift+V, while ordinary
// GUI fields use Ctrl+V.
func isTerminal(w xproto.Window) bool {
	if w == 0 {
		return false
	}
	cl, err := icccm.WmClassGet(X, w)
	if err != nil || cl == nil {
		return false
	}
	return isTermClass(strings.ToLower(cl.Class)) || isTermClass(strings.ToLower(cl.Instance))
}

func isTermClass(s string) bool {
	if strings.Contains(s, "terminal") || strings.Contains(s, "console") {
		return true // gnome-terminal, xfce4-terminal, org.gnome.Console (kgx), etc.
	}
	switch s {
	case "kitty", "foot", "footclient", "alacritty", "st", "st-256color",
		"xterm", "urxvt", "rxvt", "rxvt-unicode", "konsole", "org.kde.konsole",
		"wezterm", "org.wezfurlong.wezterm", "tilix", "terminator", "guake",
		"kgx", "ghostty", "com.mitchellh.ghostty":
		return true
	}
	return false
}

// --- Layout-independent pasting of the selected entry ---
//
// Goal: synthesize Ctrl+V (Ctrl+Shift+V for terminals) so that ANY application
// (GTK/Qt/Electron/terminals) pastes under ANY active layout
// (the user has us,ru,us). A naive XTEST of the real 'v' keycode in the Russian group
// produces "м" — that physical key carries Cyrillic there.
//
// Solution (like xdotool, but native and therefore fast, ~a few ms): keep a
// SPARE unused keycode mapped to 'v'/'V' in ALL groups, and send exactly
// that one — then in any group it yields 'v'. The real 'v' key can't be
// sent: terminals (kitty) track the active group themselves and in Russian see "м".
//
// Three subtleties, without which it breaks:
//  1. We map the spare keycode ONLY while the popup is open (setupSpareKey in showPopup),
//     and return it to NoSymbol after closing (restoreSpareKey, see finish). If
//     'v' is kept on it permanently, mutter resolves the Super+V hotkey to exactly
//     this keycode (in the Russian layout 'v' exists nowhere else) → the physical
//     Super+V stops opening the popup. While the popup is open the keyboard is
//     grabbed anyway, so Super+V isn't needed during that time.
//  2. We map on popup OPEN (not at the moment Enter is pressed) so applications
//     have time to asynchronously re-read the keymap before the keypress — otherwise a race:
//     the app processes the keypress with the old keymap and doesn't see 'v'.
//  3. Restoring the mapping is delayed after closing (see finish), because Qt/Electron
//     read the key event asynchronously: restore it immediately and they'd already see NoSymbol.

// setupSpareKey maps the spare keycode to 'v'/'V' in all layout groups
// (see the block above). We take the server's keysymsPerKeycode (spareKPK), otherwise the
// group offset breaks and the Russian layout yields "м".
func setupSpareKey() {
	if spareKey == 0 {
		return
	}
	n := int(spareKPK)
	if n < 6 {
		n = 6
	}
	ks := make([]xproto.Keysym, n)
	for i := 0; i+1 < n; i += 2 {
		ks[i], ks[i+1] = 0x0076, 0x0056 // each group: [v, V]
	}
	xproto.ChangeKeyboardMapping(X.Conn(), 1, spareKey, byte(n), ks)
	xsync()
}

// restoreSpareKey returns the spare keycode to NoSymbol — otherwise mutter resolves
// Super+V to it and the hotkey stops working (see subtlety 1 above).
func restoreSpareKey() {
	if spareKey == 0 {
		return
	}
	n := int(spareKPK)
	if n < 1 {
		n = 1
	}
	ks := make([]xproto.Keysym, n) // all NoSymbol
	xproto.ChangeKeyboardMapping(X.Conn(), 1, spareKey, byte(n), ks)
	xsync()
}

// pasteInto synthesizes Ctrl+V (Ctrl+Shift+V for terminals) via XTEST,
// using the spare keycode (mapped to 'v' in all groups by setupSpareKey).
func pasteInto(term bool) {
	v := vKey
	if spareKey != 0 {
		v = spareKey // 'v' in any group → layout-independent
	}
	fakeKey(true, ctrlKey)
	if term {
		fakeKey(true, shiftKey)
	}
	fakeKey(true, v)
	fakeKey(false, v)
	if term {
		fakeKey(false, shiftKey)
	}
	fakeKey(false, ctrlKey)
	xsync()
}

// findSpareKeycode looks for an unused keycode (NoSymbol in all groups) that
// can be temporarily repurposed for 'v' when pasting. It also records the server's
// keysyms-per-keycode in spareKPK. Returns 0 if none is free (then
// pasting falls back to the real 'v' keycode — with the Russian-layout caveat).
func findSpareKeycode() xproto.Keycode {
	setup := xproto.Setup(X.Conn())
	minKc, maxKc := int(setup.MinKeycode), int(setup.MaxKeycode)
	m, err := xproto.GetKeyboardMapping(X.Conn(), setup.MinKeycode, byte(maxKc-minKc+1)).Reply()
	if err != nil {
		return 0
	}
	spareKPK = m.KeysymsPerKeycode
	per := int(m.KeysymsPerKeycode)
	for kc := maxKc; kc >= minKc; kc-- {
		base := (kc - minKc) * per
		empty := true
		for j := 0; j < per && base+j < len(m.Keysyms); j++ {
			if m.Keysyms[base+j] != 0 {
				empty = false
				break
			}
		}
		if empty {
			return xproto.Keycode(kc)
		}
	}
	return 0
}

// xsync — a round-trip so the server applies the request before the next step.
func xsync() {
	xproto.GetInputFocus(X.Conn()).Reply()
}

// fakeKey synthesizes a key press/release via XTEST (real input,
// not SendEvent — applications accept it).
func fakeKey(press bool, code xproto.Keycode) {
	t := byte(xproto.KeyRelease)
	if press {
		t = byte(xproto.KeyPress)
	}
	xtest.FakeInput(X.Conn(), t, byte(code), 0, X.RootWin(), 0, 0, 0)
}

// ---------- positioning ----------

func popupXY() (int, int) {
	mouseX, mouseY := 0, 0
	if p, err := xproto.QueryPointer(X.Conn(), X.RootWin()).Reply(); err == nil {
		mouseX, mouseY = int(p.RootX), int(p.RootY)
	}
	px, py := mouseX, mouseY

	if targetWin != 0 {
		geom, gerr := xproto.GetGeometry(X.Conn(), xproto.Drawable(targetWin)).Reply()
		tr, terr := xproto.TranslateCoordinates(X.Conn(), targetWin, X.RootWin(), 0, 0).Reply()
		if gerr == nil && terr == nil {
			wx, wy := int(tr.DstX), int(tr.DstY)
			ww, wh := int(geom.Width), int(geom.Height)
			inside := mouseX >= wx && mouseX < wx+ww && mouseY >= wy && mouseY < wy+wh
			if !inside {
				px = wx + ww/2 - popupW/2
				py = wy + wh/2 - popupH/2
			}
		}
	}
	return clampToScreen(px, py)
}

func clampToScreen(x, y int) (int, int) {
	scr := X.Screen()
	if x+popupW > int(scr.WidthInPixels) {
		x = int(scr.WidthInPixels) - popupW
	}
	if y+popupH > int(scr.HeightInPixels) {
		y = int(scr.HeightInPixels) - popupH
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}
