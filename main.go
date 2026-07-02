// clipmgr — спайк №4: список истории (статичные данные) в стиле Windows/Nautilus.
//
// Резидентный GTK-демон. По Super+B (через GNOME-хоткей → clipmgr --show → сокет)
// показывает у курсора/окна попап: заголовок "Clipboard" + прокручиваемый список
// записей. Каждая запись обрезается до 3 строк. Выделение — акцентная обводка
// (Yaru accent) как фокус файла в Nautilus. Up/Down двигают выделение, Enter
// вставляет выбранное в прежнее окно, Escape закрывает.
//
// Ввод берём через xgb (GrabKeyboard на root) — у всплывшего GTK-окна GNOME
// отбирает фокус (focus-stealing prevention). Навигацию по списку из-за этого
// делаем сами и вручную двигаем выделение GTK-виджета.
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
	"github.com/gotk3/gotk3/pango"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/keybind"
)

const (
	listW  = 340 // ширина списка (без учёта рамки/тени)
	popupW = 372 // оценка полного размера окна (для позиционирования)
	popupH = 360
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
  border: 2px solid transparent;
  border-radius: 8px;
  margin: 2px 8px;
  padding: 8px 10px;
  outline: none;
}
list row:selected {
  border-color: @theme_selected_bg_color;
  background-color: alpha(@theme_selected_bg_color, 0.14);
}
`

var (
	X       *xgbutil.XUtil
	ctrlKey xproto.Keycode
	vKey    xproto.Keycode

	win       *gtk.Window
	listBox   *gtk.ListBox
	scrolled  *gtk.ScrolledWindow
	selIdx    int
	targetWin xproto.Window

	grabTries int

	items = clipItems()
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--show" {
		runClient()
		return
	}
	runDaemon()
}

// ---------- клиент ----------

func runClient() {
	c, err := net.Dial("unix", sockPath())
	if err != nil {
		log.Fatalf("демон не запущен (%v)", err)
	}
	defer c.Close()
	c.Write([]byte("show\n"))
}

// ---------- демон ----------

func runDaemon() {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		log.Fatal("демон уже запущен")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	var err error
	X, err = xgbutil.NewConn()
	if err != nil {
		log.Fatalf("нет соединения с X: %v", err)
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

	gtk.Init(nil)
	applyCSS()
	startSocketListener()

	log.Println("daemon (GTK): слушаю сокет", sockPath(), "— жду --show")
	gtk.Main()
}

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
			if strings.HasPrefix(string(buf[:n]), "show") {
				glib.IdleAdd(func() bool { showPopup(); return false })
			}
		}
	}()
}

// ---------- попап ----------

func showPopup() {
	if win != nil {
		return
	}
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
	// RGBA-визуал → прозрачные углы, чтобы скруглить окно (если есть композитор).
	if screen, err := gdk.ScreenGetDefault(); err == nil && screen.IsComposited() {
		if vis, err := screen.GetRGBAVisual(); err == nil && vis != nil {
			w.SetVisual(vis)
		}
	}

	outer, _ := gtk.BoxNew(gtk.ORIENTATION_VERTICAL, 0)
	addClass(outer, "clip-frame")

	header, _ := gtk.LabelNew("Clipboard")
	header.SetXAlign(0)
	addClass(header, "clip-header")
	outer.PackStart(header, false, false, 0)

	listBox, _ = gtk.ListBoxNew()
	listBox.SetSelectionMode(gtk.SELECTION_BROWSE)
	for _, it := range items {
		lbl, _ := gtk.LabelNew(displayText(it))
		lbl.SetXAlign(0)
		lbl.SetYAlign(0) // текст сверху (короткие оставляют пустоту снизу)
		lbl.SetVAlign(gtk.ALIGN_FILL)
		lbl.SetLineWrap(false)                // без переноса → каждая строка = одна визуальная
		lbl.SetEllipsize(pango.ELLIPSIZE_END) // длинную строку обрезаем многоточием справа
		lbl.SetMaxWidthChars(42)
		listBox.Add(lbl)
	}

	scrolled, _ = gtk.ScrolledWindowNew(nil, nil)
	scrolled.SetPolicy(gtk.POLICY_NEVER, gtk.POLICY_AUTOMATIC)
	scrolled.SetSizeRequest(listW, 285) // видимая часть — ~3.5 записи
	scrolled.Add(listBox)
	outer.PackStart(scrolled, true, true, 0)

	w.Add(outer)

	x, y := popupXY()
	w.Move(x, y)
	w.ShowAll()

	win = w
	selIdx = 0
	updateSelection()
	log.Printf("popup показан в (%d,%d), target=%d, записей=%d", x, y, targetWin, len(items))

	grabTries = 0
	tryGrab()

	glib.TimeoutAdd(15000, func() bool {
		if win == w {
			finish(false)
		}
		return false
	})
}

func addClass(w interface {
	GetStyleContext() (*gtk.StyleContext, error)
}, cls string) {
	if sc, err := w.GetStyleContext(); err == nil {
		sc.AddClass(cls)
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
	row.GrabFocus() // GtkScrolledWindow подтягивает сфокусированного ребёнка в видимую зону

	// Подстраховка-скролл вручную (на случай, если фокус не двигает вьюпорт).
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
		startKeyPoll()
		return
	}
	grabTries++
	if grabTries >= 100 {
		log.Println("не удалось захватить клавиатуру — закрываю")
		finish(false)
		return
	}
	glib.TimeoutAdd(20, func() bool { tryGrab(); return false })
}

func startKeyPoll() {
	glib.TimeoutAdd(8, func() bool {
		if win == nil {
			return false
		}
		for {
			ev, err := X.Conn().PollForEvent()
			if err != nil || ev == nil {
				break
			}
			kp, ok := ev.(xproto.KeyPressEvent)
			if !ok {
				continue
			}
			switch keybind.LookupString(X, kp.State, kp.Detail) {
			case "Up":
				if selIdx > 0 {
					selIdx--
					updateSelection()
				}
			case "Down":
				if selIdx < len(items)-1 {
					selIdx++
					updateSelection()
				}
			case "Return", "KP_Enter":
				finish(true)
			case "Escape":
				finish(false)
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

	text := ""
	if paste && selIdx >= 0 && selIdx < len(items) {
		text = items[selIdx]
	}
	w.Destroy()
	listBox = nil
	scrolled = nil
	xproto.GetInputFocus(X.Conn()).Reply()

	if paste && text != "" {
		setClipboard(text)
		pasteCtrlV()
	}
}

func setClipboard(s string) {
	clip, err := gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
	if err != nil {
		log.Println("clipboard:", err)
		return
	}
	clip.SetText(s)
}

func pasteCtrlV() {
	fakeKey(true, ctrlKey)
	fakeKey(true, vKey)
	fakeKey(false, vKey)
	fakeKey(false, ctrlKey)
	xproto.GetInputFocus(X.Conn()).Reply()
}

func fakeKey(press bool, code xproto.Keycode) {
	t := byte(xproto.KeyRelease)
	if press {
		t = byte(xproto.KeyPress)
	}
	xtest.FakeInput(X.Conn(), t, byte(code), 0, X.RootWin(), 0, 0, 0)
}

// ---------- позиционирование ----------

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

// displayText приводит запись РОВНО к 3 строкам: длинные обрезает (с «…»),
// короткие дополняет пустыми строками. Тогда высота каждого элемента одинаковая
// и равна ровно 3 строкам (полный текст храним в items и вставляем целиком).
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

func sockPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "clipmgr.sock")
}

// ---------- статичные данные (строки 1..10 вперемешку) ----------

func clipItems() []string {
	return []string{
		// 3 строки
		"ул. Пушкина, д. 10, кв. 5\nМосква, 101000\nРоссия",
		// 10 очень длинных строк
		"Строка 1: очень длинный текст, который заведомо шире окна и должен переноситься либо обрезаться — abcdefghijklmnopqrstuvwxyz 0123456789\n" +
			"Строка 2: ещё одна длиннющая строка про clipboard-историю, которую целиком в попап не влезть никак, поэтому увидим только начало\n" +
			"Строка 3: lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor incididunt ut labore et dolore magna aliqua\n" +
			"Строка 4: /home/tihonove/projects/clipmgr/very/deep/nested/path/that/keeps/going/and/going/until/it/overflows/the/window/edge.txt\n" +
			"Строка 5: SELECT * FROM clips WHERE content LIKE '%очень длинная строка для проверки переноса и обрезки в списке истории%' LIMIT 1000\n" +
			"Строка 6: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
			"Строка 7: https://example.com/very/long/url?with=lots&of=query&parameters=that&make=it&super=long&and=unwieldy&indeed=true\n" +
			"Строка 8: восьмая строка, тоже намеренно длинная, чтобы проверить, что высота элемента остаётся ровно три строки в списке\n" +
			"Строка 9: девятая строка с кучей текста для наглядности переноса и обрезки длинного многострочного содержимого буфера\n" +
			"Строка 10: и наконец десятая, финальная длинная строка этого большого элемента истории буфера обмена — конец",
		// 1 строка
		"https://github.com/tihonove/clipmgr",
		// 7 строк
		"func popupXY() (int, int) {\n\tmouseX, mouseY := 0, 0\n\tif p, err := QueryPointer(); err == nil {\n\t\tmouseX, mouseY = p.X, p.Y\n\t}\n\treturn clampToScreen(mouseX, mouseY)\n}",
		// 2 строки
		"API_KEY=sk-9f2a3b7c1d4e5f6a7b8c9d0e\nENDPOINT=https://api.example.com/v1",
		// 10 строк
		"{\n  \"id\": 42,\n  \"name\": \"clipmgr\",\n  \"tags\": [\"go\", \"gtk\", \"x11\"],\n  \"active\": true,\n  \"count\": 10,\n  \"owner\": \"tihonove\",\n  \"created\": \"2026-07-03\",\n  \"nested\": { \"a\": 1 }\n}",
		// 5 строк
		"cd ~/projects/clipmgr\ngo build -o clipmgr .\npkill clipmgr\nsetsid ./clipmgr &\ntail -f /tmp/clipmgr.log",
		// 9 строк
		"[Desktop Entry]\nType=Application\nName=clipmgr\nComment=Clipboard history daemon\nExec=/home/tihonove/projects/clipmgr/clipmgr\nX-GNOME-Autostart-enabled=true\nNoDisplay=true\nTerminal=false\nCategories=Utility;",
		// 4 строки
		"С уважением,\nЕвгений Тихонов\ntihonov.ea@gmail.com\n+7 900 000-00-00",
		// 8 строк
		"## TODO\n- [x] хоткей через GNOME\n- [x] демон + сокет\n- [x] тема Yaru\n- [ ] история буфера\n- [ ] иконки картинок\n- [ ] инсталлятор\n- [ ] терминал-aware вставка",
		// 6 строк
		"SELECT id, name, created\nFROM clips\nWHERE owner = 'tihonove'\n  AND active = true\nORDER BY created DESC\nLIMIT 10;",
	}
}
