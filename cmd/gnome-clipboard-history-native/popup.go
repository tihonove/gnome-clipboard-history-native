//go:build linux

// popup.go — the part of the popup shared by both backends: CSS (Yaru theme), the
// buildPopupBox content constructor, and preparing entry text for display.
package main

import (
	"log"
	"math"
	"strings"

	"github.com/gotk3/gotk3/cairo"
	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"
)

const (
	listW    = 340 // list width (excluding the frame/shadow)
	popupW   = 372 // estimate of the full window size (for positioning)
	popupH   = 360
	pageStep = 3  // how far to jump on PageUp/PageDown (≈ number of visible rows)
	rowH     = 56 // fixed row content height (≈ 3 text lines); images are cover-rendered to this size
)

const cssData = `
window { background-color: transparent; }
.clip-frame {
  background-color: @theme_bg_color;
  border: 1px solid alpha(@theme_fg_color, 0.18);
  border-radius: 12px;
  box-shadow: 0 3px 12px rgba(0,0,0,0.35);
  margin: 10px;
}
.clip-header {
  font-weight: bold;
  padding: 10px 14px 6px 14px;
  color: @theme_fg_color;
}
list { background-color: transparent; }
list row {
  background-color: transparent;
  /* a thin gray border around each item — so entries don't blend together */
  border: 1px solid alpha(@theme_fg_color, 0.14);
  border-radius: 8px;
  margin: 2px 8px;
  padding: 8px 10px;
  outline: none;
}
list row:selected {
  /* selected — Yaru accent on top of the gray border */
  border-color: @theme_selected_bg_color;
  background-color: alpha(@theme_selected_bg_color, 0.16);
}
.clip-empty {
  padding: 28px 18px;
  color: alpha(@theme_fg_color, 0.55);
}
`

var (
	win      *gtk.Window
	listBox  *gtk.ListBox
	scrolled *gtk.ScrolledWindow
)

func applyCSS() {
	prov, err := gtk.CssProviderNew()
	if err != nil {
		log.Println("css provider:", err)
		return
	}
	if err := prov.LoadFromData(cssData); err != nil {
		log.Println("css load:", err)
		return
	}
	if screen, err := gdk.ScreenGetDefault(); err == nil {
		gtk.AddProviderForScreen(screen, prov, gtk.STYLE_PROVIDER_PRIORITY_USER)
	}
}

// buildPopupBox assembles the popup content (header + list of entries or an
// empty-state placeholder) and sets the listBox/scrolled globals. Shared by both
// backends — X11 (showPopup) and Wayland (showPopupWayland).
func buildPopupBox() *gtk.Box {
	outer, _ := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0)
	addClass(outer, "clip-frame")

	header, _ := gtk.LabelNew("Clipboard")
	header.SetXAlign(0)
	addClass(header, "clip-header")
	outer.PackStart(header, false, false, 0)

	if len(history) == 0 {
		ph, _ := gtk.LabelNew("Clipboard is empty.\nCopy something to see it here.")
		ph.SetJustify(gtk.JUSTIFY_CENTER)
		ph.SetHAlign(gtk.ALIGN_CENTER)
		ph.SetVAlign(gtk.ALIGN_CENTER)
		ph.SetSizeRequest(listW, 285) // the same height as the list
		addClass(ph, "clip-empty")
		outer.PackStart(ph, true, true, 0)
		return outer
	}

	listBox, _ = gtk.ListBoxNew()
	listBox.SetSelectionMode(gtk.SELECTION_BROWSE)
	for _, it := range history {
		listBox.Add(rowWidget(it))
	}

	scrolled, _ = gtk.ScrolledWindowNew(nil, nil)
	scrolled.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
	scrolled.SetSizeRequest(listW, 285) // visible part — ~3.5 entries
	scrolled.Add(listBox)
	outer.PackStart(scrolled, true, true, 0)
	return outer
}

func addClass(w interface {
	GetStyleContext() (*gtk.StyleContext, error)
}, cls string) {
	if sc, err := w.GetStyleContext(); err == nil {
		sc.AddClass(cls)
	}
}

// rowWidget builds the content widget for one list row: for text — a Label
// (truncated to 3 lines, see displayText), for an image — a DrawingArea with a cover-render.
// The height is fixed (rowH) for both kinds, so rows are uniform.
func rowWidget(it *clipItem) gtk.IWidget {
	if it.kind == kindImage {
		return imageRow(it.pix)
	}
	lbl, _ := gtk.LabelNew(displayText(it.text))
	lbl.SetXAlign(0)
	lbl.SetYAlign(0) // text at the top (short ones leave empty space at the bottom)
	lbl.SetVAlign(gtk.ALIGN_FILL)
	lbl.SetLineWrap(false)                // no wrapping → each line = one visual line
	lbl.SetEllipsize(pango.ELLIPSIZE_END) // a long line is truncated with an ellipsis on the right
	lbl.SetMaxWidthChars(42)
	lbl.SetSizeRequest(-1, rowH) // the same height as images
	return lbl
}

// imageRow draws an image preview into the fixed row rectangle using the cover method:
// scale "to fill" (max of the width/height ratios), centering, and clipping the edges
// to the row boundary (Clip). This is overflow:hidden — the image fills the whole row.
func imageRow(pix *gdk.Pixbuf) gtk.IWidget {
	da, _ := gtk.DrawingAreaNew()
	da.SetSizeRequest(-1, rowH) // width is stretched by the row, height is fixed
	da.SetHExpand(true)
	da.Connect("draw", func(_ *gtk.DrawingArea, cr *cairo.Context) bool {
		if pix == nil {
			return false
		}
		w := float64(da.GetAllocatedWidth())
		h := float64(da.GetAllocatedHeight())
		pw := float64(pix.GetWidth())
		ph := float64(pix.GetHeight())
		if pw <= 0 || ph <= 0 {
			return false
		}
		cr.Save()
		cr.Rectangle(0, 0, w, h)
		cr.Clip() // clip everything outside the row
		scale := math.Max(w/pw, h/ph)
		cr.Translate((w-pw*scale)/2, (h-ph*scale)/2) // center the clipped image
		cr.Scale(scale, scale)
		gtk.GdkCairoSetSourcePixBuf(cr, pix, 0, 0)
		cr.Paint()
		cr.Restore()
		return false
	})
	return da
}

// displayText forces entry text to EXACTLY 3 lines: long ones are truncated (with "…"),
// short ones are padded with empty lines. Then every item's height is uniform and
// equals exactly 3 lines (the full text is stored in history and pasted in full).
func displayText(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = []string{lines[0], lines[1], lines[2] + " …"}
	}
	for len(lines) < 3 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}
