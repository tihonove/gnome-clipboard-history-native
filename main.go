// clipmgr — нативная история буфера обмена (Win+V) для GNOME/X11.
//
// Резидентный GTK-демон. По Super+V (через GNOME-хоткей → clipmgr --show → сокет)
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
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/gotk3/gotk3/gdk"
	"github.com/gotk3/gotk3/glib"
	"github.com/gotk3/gotk3/gtk"
	"github.com/gotk3/gotk3/pango"

	"github.com/jezek/xgb/xproto"
	"github.com/jezek/xgb/xtest"
	"github.com/jezek/xgbutil"
	"github.com/jezek/xgbutil/ewmh"
	"github.com/jezek/xgbutil/icccm"
	"github.com/jezek/xgbutil/keybind"
)

const (
	listW      = 340 // ширина списка (без учёта рамки/тени)
	popupW     = 372 // оценка полного размера окна (для позиционирования)
	popupH     = 360
	pageStep   = 3   // на сколько прыгать по PageUp/PageDown (≈ число видимых строк)
	maxHistory = 100 // сколько записей держим в памяти
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
.clip-empty {
  padding: 28px 18px;
  color: alpha(@theme_fg_color, 0.55);
}
`

var (
	X        *xgbutil.XUtil
	ctrlKey  xproto.Keycode
	vKey     xproto.Keycode
	shiftKey xproto.Keycode
	spareKey xproto.Keycode // запасной keycode для layout-независимой вставки
	spareKPK byte           // keysyms-per-keycode сервера (глобальное)

	win            *gtk.Window
	listBox        *gtk.ListBox
	scrolled       *gtk.ScrolledWindow
	selIdx         int
	targetWin      xproto.Window
	popupX, popupY int // куда поставили окно (для проверки клика мимо)

	grabTries int

	clipboard *gtk.Clipboard // CLIPBOARD: и слушаем, и владеем им при вставке
	history   []string       // история буфера, свежие сверху (только в памяти)
)

// version зашивается при релизной сборке через -ldflags "-X main.version=…".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--show":
			runClient()
			return
		case "--install":
			runInstall()
			return
		case "--uninstall":
			runUninstall()
			return
		case "--version", "-v":
			fmt.Println("clipmgr", version)
			return
		case "--help", "-h":
			fmt.Println("clipmgr — история буфера (Win+V) для GNOME/X11\n" +
				"  clipmgr             запустить демона\n" +
				"  clipmgr --install   прописать автозапуск и хоткей Super+V, запустить демона\n" +
				"  clipmgr --uninstall убрать автозапуск и хоткей\n" +
				"  clipmgr --show      показать попап (вызывается хоткеем)\n" +
				"  clipmgr --version   версия")
			return
		}
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

// ---------- установка (--install / --uninstall) ----------

const (
	mediaKeysSchema = "org.gnome.settings-daemon.plugins.media-keys"
	customPrefix    = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
	hotkeyBinding   = "<Super>v"
	hotkeyName      = "clipmgr"
)

// runInstall прописывает автозапуск и горячую клавишу на текущий путь бинарника
// и запускает демона. Идемпотентно — можно запускать повторно.
func runInstall() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("не могу определить путь бинарника: %v", err)
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	installAutostart(exe)
	installHotkey(exe)
	startDaemon(exe)
	fmt.Println("Готово. clipmgr в автозапуске, Super+V настроен, демон запущен.")
}

func runUninstall() {
	if err := os.Remove(autostartPath()); err == nil {
		fmt.Println("убран автозапуск:", autostartPath())
	}
	removeHotkey()
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Write([]byte("quit\n"))
		c.Close()
		fmt.Println("демон остановлен")
	}
	fmt.Println("Готово. clipmgr убран из автозапуска и хоткеев.")
}

func autostartPath() string {
	return filepath.Join(xdgConfigHome(), "autostart", "clipmgr.desktop")
}

func installAutostart(exe string) {
	dir := filepath.Join(xdgConfigHome(), "autostart")
	os.MkdirAll(dir, 0o755)
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=clipmgr\n" +
		"Comment=Clipboard history (Win+V) for GNOME/X11\n" +
		"Exec=" + exe + "\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		"NoDisplay=true\n" +
		"Terminal=false\n"
	if err := os.WriteFile(autostartPath(), []byte(content), 0o644); err != nil {
		log.Fatalf("автозапуск: %v", err)
	}
	fmt.Println("автозапуск:", autostartPath())
}

func installHotkey(exe string) {
	cmd := exe + " --show"
	list := gsList()
	for _, p := range list { // уже установлено?
		if unquote(gsGet(customPath(p), "command")) == cmd {
			fmt.Println("хоткей уже настроен:", p)
			return
		}
	}
	slot := freeSlot(list)
	list = append(list, slot)
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(list))
	gsSet(customPath(slot), "name", quote(hotkeyName))
	gsSet(customPath(slot), "command", quote(cmd))
	gsSet(customPath(slot), "binding", quote(hotkeyBinding))
	fmt.Println("хоткей Super+V →", slot)
}

func removeHotkey() {
	list := gsList()
	kept := make([]string, 0, len(list))
	for _, p := range list {
		if unquote(gsGet(customPath(p), "name")) == hotkeyName {
			// сбросить ключи слота
			for _, k := range []string{"name", "command", "binding"} {
				exec.Command("gsettings", "reset", customPath(p), k).Run()
			}
			fmt.Println("убран хоткей:", p)
			continue
		}
		kept = append(kept, p)
	}
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(kept))
}

// startDaemon запускает демона отдельным сеансом, если он ещё не запущен.
func startDaemon(exe string) {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		fmt.Println("демон уже запущен")
		return
	}
	c := exec.Command(exe)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		fmt.Println("не удалось запустить демона:", err, "— стартанёт при следующем входе")
		return
	}
	fmt.Println("демон запущен")
}

// --- helpers для gsettings и путей ---

func xdgConfigHome() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

func customPath(p string) string { return mediaKeysSchema + ".custom-keybinding:" + p }

func gsGet(schema, key string) string {
	out, _ := exec.Command("gsettings", "get", schema, key).Output()
	return strings.TrimSpace(string(out))
}

func gsSet(schema, key, val string) {
	if err := exec.Command("gsettings", "set", schema, key, val).Run(); err != nil {
		log.Printf("gsettings set %s %s: %v", schema, key, err)
	}
}

func gsList() []string { return parseList(gsGet(mediaKeysSchema, "custom-keybindings")) }

// freeSlot возвращает путь первого свободного customN/.
func freeSlot(list []string) string {
	used := map[string]bool{}
	for _, p := range list {
		used[p] = true
	}
	for i := 0; ; i++ {
		cand := fmt.Sprintf("%scustom%d/", customPrefix, i)
		if !used[cand] {
			return cand
		}
	}
}

func parseList(s string) []string {
	var res []string
	for {
		i := strings.IndexByte(s, '\'')
		if i < 0 {
			break
		}
		s = s[i+1:]
		j := strings.IndexByte(s, '\'')
		if j < 0 {
			break
		}
		res = append(res, s[:j])
		s = s[j+1:]
	}
	return res
}

func formatList(items []string) string {
	if len(items) == 0 {
		return "@as []"
	}
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = quote(it)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func quote(s string) string   { return "'" + s + "'" }
func unquote(s string) string { return strings.Trim(s, "'") }

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
	if ss := keybind.StrToKeycodes(X, "Shift_L"); len(ss) > 0 {
		shiftKey = ss[0]
	}
	spareKey = findSpareKeycode()

	gtk.Init(nil)
	applyCSS()
	startClipboardWatch()
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
			switch {
			case strings.HasPrefix(string(buf[:n]), "show"):
				glib.IdleAdd(func() bool { showPopup(); return false })
			case strings.HasPrefix(string(buf[:n]), "quit"):
				glib.IdleAdd(func() bool { gtk.MainQuit(); return false })
			}
		}
	}()
}

// ---------- слушалка буфера ----------

func startClipboardWatch() {
	var err error
	clipboard, err = gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
	if err != nil {
		log.Fatalf("clipboard: %v", err)
	}
	clipboard.Connect("owner-change", func() {
		// WaitForText прямо в обработчике сигнала небезопасен (реентранси) —
		// откладываем на следующий idle в том же GTK-потоке.
		glib.IdleAdd(func() bool {
			if txt, e := clipboard.WaitForText(); e == nil {
				addToHistory(txt)
			}
			return false
		})
	})
}

// addToHistory кладёт запись наверх истории (дедуп, лимит). Только текст, в памяти.
func addToHistory(s string) {
	if strings.TrimSpace(s) == "" {
		return
	}
	for i, e := range history { // дедуп: убрать старую позицию такой же записи
		if e == s {
			history = append(history[:i], history[i+1:]...)
			break
		}
	}
	history = append([]string{s}, history...) // свежее — сверху
	if len(history) > maxHistory {
		history = history[:maxHistory]
	}
	log.Printf("history: %d записей", len(history))
}

// ---------- попап ----------

func showPopup() {
	if win != nil {
		return
	}
	setupSpareKey() // пока попап открыт: 'v' на запасном keycode (приложения успеют подхватить keymap)
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

	if len(history) == 0 {
		ph, _ := gtk.LabelNew("Clipboard is empty.\nCopy something to see it here.")
		ph.SetJustify(gtk.JUSTIFY_CENTER)
		ph.SetHAlign(gtk.ALIGN_CENTER)
		ph.SetVAlign(gtk.ALIGN_CENTER)
		ph.SetSizeRequest(listW, 285) // та же высота, что и у списка
		addClass(ph, "clip-empty")
		outer.PackStart(ph, true, true, 0)
	} else {
		listBox, _ = gtk.ListBoxNew()
		listBox.SetSelectionMode(gtk.SELECTION_BROWSE)
		for _, it := range history {
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
	}

	w.Add(outer)

	x, y := popupXY()
	w.Move(x, y)
	w.ShowAll()

	win = w
	popupX, popupY = x, y
	selIdx = 0
	updateSelection()
	log.Printf("popup показан в (%d,%d), target=%d, записей=%d", x, y, targetWin, len(history))

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

// insidePopup — попал ли клик (в координатах экрана) в прямоугольник окна попапа.
func insidePopup(x, y int) bool {
	if win == nil {
		return false
	}
	w, h := win.GetSize()
	return x >= popupX && x < popupX+w && y >= popupY && y < popupY+h
}

// setSel зажимает индекс в [0, len-1] и, если он изменился, обновляет выделение.
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
		// Захватываем и указатель — чтобы ловить клики мимо окна и закрываться.
		xproto.GrabPointer(X.Conn(), false, X.RootWin(),
			uint16(xproto.EventMaskButtonPress),
			xproto.GrabModeAsync, xproto.GrabModeAsync,
			xproto.Window(0), xproto.Cursor(0), xproto.TimeCurrentTime)
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
					finish(false) // клик мимо окна — закрыть
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

	text := ""
	if paste && selIdx >= 0 && selIdx < len(history) {
		text = history[selIdx]
	}
	w.Destroy()
	listBox = nil
	scrolled = nil
	xproto.GetInputFocus(X.Conn()).Reply()

	if paste && text != "" {
		setClipboard(text)
		pasteInto(isTerminal(targetWin)) // терминалам — Ctrl+Shift+V, остальным — Ctrl+V
	}

	// Вернуть запасной keycode в NoSymbol (иначе mutter резолвит Super+V на него).
	// С задержкой — чтобы приложения успели обработать нажатие вставки; и только
	// если попап не открыли снова.
	glib.TimeoutAdd(300, func() bool {
		if win == nil {
			restoreSpareKey()
		}
		return false
	})
}

// isTerminal определяет, что целевое окно — терминал (по WM_CLASS). Нужно, чтобы
// выбрать комбинацию вставки: терминалы вставляют по Ctrl+Shift+V, а обычные
// GUI-поля — по Ctrl+V.
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
		return true // gnome-terminal, xfce4-terminal, org.gnome.Console (kgx) и т.п.
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

// setClipboard делает демона владельцем CLIPBOARD с текстом s. Пока демон жив, он
// сам обслуживает запросы вставки — поэтому внешний xsel/xclip не нужен.
func setClipboard(s string) {
	if clipboard != nil {
		clipboard.SetText(s)
	}
}

// --- Вставка выбранной записи, независимая от раскладки ---
//
// Задача: синтезировать Ctrl+V (Ctrl+Shift+V для терминалов) так, чтобы вставили
// ЛЮБЫЕ приложения (GTK/Qt/Electron/терминалы) при ЛЮБОЙ активной раскладке
// (у пользователя us,ru,us). Наивный XTEST реального keycode 'v' в русской группе
// даёт «м» — на этой физической клавише кириллица.
//
// Решение (как в xdotool, но нативно и потому быстро, ~единицы мс): держим
// ЗАПАСНОЙ неиспользуемый keycode, замапленный на 'v'/'V' во ВСЕХ группах, и шлём
// именно его — тогда в любой группе на нём выходит 'v'. Реальную клавишу 'v' слать
// нельзя: терминалы (kitty) ведут активную группу сами и в русской видят «м».
//
// Три тонкости, без которых ломается:
//  1. Запасной keycode мапим ТОЛЬКО пока попап открыт (setupSpareKey в showPopup),
//     а после закрытия возвращаем в NoSymbol (restoreSpareKey, см. finish). Если
//     держать 'v' на нём постоянно, mutter резолвит горячую клавишу Super+V именно
//     на этот keycode (в русской раскладке 'v' больше нигде нет) → физический
//     Super+V перестаёт открывать попап. Пока попап открыт, клавиатура и так
//     захвачена, так что Super+V в это время не нужен.
//  2. Мапим при ОТКРЫТИИ попапа (не в момент нажатия Enter), чтобы приложения
//     успели асинхронно перечитать keymap до нажатия — иначе гонка: приложение
//     обработает нажатие со старым keymap и не увидит 'v'.
//  3. Возврат мэппинга — с задержкой после закрытия (см. finish), т.к. Qt/Electron
//     читают событие клавиши асинхронно: вернёшь сразу — увидят уже NoSymbol.

// setupSpareKey мапит запасной keycode на 'v'/'V' во всех группах раскладки
// (см. блок выше). keysymsPerKeycode берём серверный (spareKPK), иначе смещение
// групп ломается и в русской раскладке выходит «м».
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
		ks[i], ks[i+1] = 0x0076, 0x0056 // каждая группа: [v, V]
	}
	xproto.ChangeKeyboardMapping(X.Conn(), 1, spareKey, byte(n), ks)
	xsync()
}

// restoreSpareKey возвращает запасной keycode в NoSymbol — иначе mutter резолвит
// на него Super+V и хоткей перестаёт работать (см. тонкость 1 выше).
func restoreSpareKey() {
	if spareKey == 0 {
		return
	}
	n := int(spareKPK)
	if n < 1 {
		n = 1
	}
	ks := make([]xproto.Keysym, n) // все NoSymbol
	xproto.ChangeKeyboardMapping(X.Conn(), 1, spareKey, byte(n), ks)
	xsync()
}

// pasteInto синтезирует Ctrl+V (Ctrl+Shift+V для терминалов) через XTEST,
// используя запасной keycode (замаплен на 'v' во всех группах в setupSpareKey).
func pasteInto(term bool) {
	v := vKey
	if spareKey != 0 {
		v = spareKey // 'v' в любой группе → раскладко-независимо
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

// findSpareKeycode ищет неиспользуемый keycode (во всех группах NoSymbol), который
// можно временно занять под 'v' для вставки. Заодно запоминает серверный
// keysyms-per-keycode в spareKPK. Возвращает 0, если свободного нет (тогда
// вставка откатится на реальный keycode 'v' — с оговоркой про русскую раскладку).
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

// xsync — round-trip, чтобы сервер применил запрос до следующего шага.
func xsync() {
	xproto.GetInputFocus(X.Conn()).Reply()
}

// fakeKey синтезирует нажатие/отпускание клавиши через XTEST (реальный ввод,
// а не SendEvent — приложения его принимают).
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

func sockPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "clipmgr.sock")
}
