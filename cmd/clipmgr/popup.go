//go:build linux

// popup.go — общая для обоих бэкендов часть попапа: CSS (тема Yaru), конструктор
// содержимого buildPopupBox и подготовка текста записей к показу.
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
	listW    = 340 // ширина списка (без учёта рамки/тени)
	popupW   = 372 // оценка полного размера окна (для позиционирования)
	popupH   = 360
	pageStep = 3  // на сколько прыгать по PageUp/PageDown (≈ число видимых строк)
	rowH     = 56 // фикс. высота контента строки (≈ 3 текстовые строки); картинку рисуем cover в этот размер
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
  /* тонкая серая рамка вокруг каждого элемента — чтобы записи не сливались */
  border: 1px solid alpha(@theme_fg_color, 0.14);
  border-radius: 8px;
  margin: 2px 8px;
  padding: 8px 10px;
  outline: none;
}
list row:selected {
  /* выделенный — акцентом Yaru поверх серой рамки */
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

// buildPopupBox собирает содержимое попапа (заголовок + список записей или
// плейсхолдер пустоты) и выставляет глобалы listBox/scrolled. Общий для обоих
// бэкендов — X11 (showPopup) и Wayland (showPopupWayland).
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
		ph.SetSizeRequest(listW, 285) // та же высота, что и у списка
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
	scrolled.SetSizeRequest(listW, 285) // видимая часть — ~3.5 записи
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

// rowWidget строит виджет содержимого одной строки списка: для текста — Label
// (обрезка до 3 строк, см. displayText), для картинки — DrawingArea с cover-рендером.
// Высота фиксирована (rowH) для обоих видов, чтобы строки были одинаковыми.
func rowWidget(it *clipItem) gtk.IWidget {
	if it.kind == kindImage {
		return imageRow(it.pix)
	}
	lbl, _ := gtk.LabelNew(displayText(it.text))
	lbl.SetXAlign(0)
	lbl.SetYAlign(0) // текст сверху (короткие оставляют пустоту снизу)
	lbl.SetVAlign(gtk.ALIGN_FILL)
	lbl.SetLineWrap(false)                // без переноса → каждая строка = одна визуальная
	lbl.SetEllipsize(pango.ELLIPSIZE_END) // длинную строку обрезаем многоточием справа
	lbl.SetMaxWidthChars(42)
	lbl.SetSizeRequest(-1, rowH) // та же высота, что у картинок
	return lbl
}

// imageRow рисует превью картинки в фиксированный прямоугольник строки методом cover:
// масштаб «на заполнение» (max из коэффициентов ширины/высоты), центрирование, обрезка
// краёв по границе строки (Clip). Это overflow:hidden — картинка заливает всю строку.
func imageRow(pix *gdk.Pixbuf) gtk.IWidget {
	da, _ := gtk.DrawingAreaNew()
	da.SetSizeRequest(-1, rowH) // ширину растянет строка, высота фиксирована
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
		cr.Clip() // всё вне строки обрезаем
		scale := math.Max(w/pw, h/ph)
		cr.Translate((w-pw*scale)/2, (h-ph*scale)/2) // центрируем обрезаемую картинку
		cr.Scale(scale, scale)
		gtk.GdkCairoSetSourcePixBuf(cr, pix, 0, 0)
		cr.Paint()
		cr.Restore()
		return false
	})
	return da
}

// displayText приводит текст записи РОВНО к 3 строкам: длинные обрезает (с «…»),
// короткие дополняет пустыми строками. Тогда высота каждого элемента одинаковая
// и равна ровно 3 строкам (полный текст храним в history и вставляем целиком).
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
