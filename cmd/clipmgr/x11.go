//go:build linux

// x11.go — X11-бэкенд целиком: попап у курсора (override-redirect), захват
// клавиатуры через xgb-grab на root с поллингом, вставка нативным XTEST через
// запасной keycode и позиционирование окна.
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
	spareKey xproto.Keycode // запасной keycode для layout-независимой вставки
	spareKPK byte           // keysyms-per-keycode сервера (глобальное)

	selIdx         int
	targetWin      xproto.Window
	popupX, popupY int // куда поставили окно (для проверки клика мимо)

	grabTries int
)

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

	var it *clipItem
	if paste && selIdx >= 0 && selIdx < len(history) {
		it = history[selIdx]
	}
	w.Hide()                               // мгновенно убрать окно с экрана (Destroy — тяжелее, делаем после вставки)
	xproto.GetInputFocus(X.Conn()).Reply() // дождаться обработки ungrab до вставки

	if it != nil {
		if it.kind == kindImage {
			setClipboardImage(it.png, it.pix)
			pasteInto(false) // картинку вставляем Ctrl+V; терминалы её не берут
		} else if it.text != "" {
			setClipboard(it.text)
			pasteInto(isTerminal(targetWin)) // терминалам — Ctrl+Shift+V, остальным — Ctrl+V
		}
	}
	listBox = nil
	scrolled = nil
	// Тяжёлый teardown окна — отложенно, чтобы главный цикл сначала отдал буфер
	// вставляющему приложению (SelectionRequest), а не ждал разрушения виджетов.
	glib.IdleAdd(func() bool { w.Destroy(); return false })

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
