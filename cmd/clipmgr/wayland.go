//go:build linux

// wayland.go — Wayland-бэкенд попапа и вставки (для GNOME Wayland).
//
// Отличия от X11-пути (x11.go) сведены к минимуму — переиспользуем общий каркас
// (buildPopupBox, setClipboard, история, CSS, сокет/демон). Специфика Wayland:
//   - Попап — обычный GTK_WINDOW_TOPLEVEL: под Wayland он получает фокус клавиатуры,
//     поэтому клавиши читаем штатными GTK-сигналами (никакого xgb-грабa root).
//   - Навигация по списку — нативная: стрелки/PageUp/Home/End уходят в сфокусированный
//     ListBox, перехватываем только Enter/Escape.
//   - Скрытие при потере фокуса — focus-out-event (клика-мимо через pointer-grab нет).
//   - Позиция — по центру: под-курсорное/в-активном-окне позиционирование (как на X11,
//     см. popupXY в x11.go) на нативном Wayland-toplevel НЕВОЗМОЖНО — тот же класс
//     ограничения, что data-control и XTEST ниже. Причины: (1) mutter игнорирует
//     gtk_window_move — координаты toplevel задаёт компоситор, не клиент; (2) курсор не
//     достать надёжно (QueryPointer по XWayland свеж лишь над XWayland-окном, а
//     _NET_ACTIVE_WINDOW для нативных wl-окон = None). Единственный позиционируемый
//     попап — override-redirect через XWayland (GDK_BACKEND=x11), но XGrabKeyboard на
//     root под mutter вернёт Success и не отдаст клавиш (фокус уходит wl-окну) — ровно
//     причина, по которой этот бэкенд на нативном toplevel. Лучшее достижимое —
//     центр на мониторе, который выберет mutter (обычно активный/подкурсорный).
//   - Вставка — uinput Shift+Insert (см. internal/uinput): раскладко-независимо и
//     универсально для терминалов и GUI, поэтому детект окна/терминала не нужен
//     (он под Wayland и невозможен).
//   - История — через XWayland-мост (startClipboardWatchWayland): фоновый wl-путь чужой
//     буфер не видит (нет data-control), но mutter зеркалит буфер в X11 CLIPBOARD, и мы
//     ловим XFIXES-уведомления и читаем селекшн in-process по xgb (как это делает CopyQ).
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

const wlPasteDelayMs = 120 // дать фокусу вернуться на прежнее окно перед инъекцией

var wlFinishing bool

// isWayland — работаем ли мы в Wayland-сессии (иначе X11-путь).
func isWayland() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" && os.Getenv("XDG_SESSION_TYPE") != "x11"
}

func sessionKind() string {
	if isWayland() {
		return "wayland"
	}
	return "x11"
}

// startClipboardWatchWayland — фоновый мониторинг буфера под GNOME Wayland через
// XWayland. Штатный wl-путь чужие копирования в фоне не видит (нет data-control), но
// mutter зеркалит Wayland-буфер в X11 CLIPBOARD, поэтому X-клиент получает XFIXES-
// уведомления о смене владельца (в т.ч. от нативных Wayland-приложений). Значение
// читаем сами (ConvertSelection→SelectionNotify→GetProperty) — так же, как это делает
// CopyQ, без внешнего xsel. Отдельное xgb-соединение (не GTK), крутится в своей
// горутине; addToHistory — через IdleAdd. Требует только наличия XWayland ($DISPLAY).
func startClipboardWatchWayland() {
	c, err := xgb.NewConn() // XWayland по $DISPLAY
	if err != nil {
		log.Printf("Wayland-история недоступна: нет XWayland (%v)", err)
		return
	}
	if err := xfixes.Init(c); err != nil {
		log.Printf("Wayland-история: xfixes init: %v", err)
		c.Close()
		return
	}
	xfixes.QueryVersion(c, 5, 0).Reply() // обязательный хендшейк перед вызовами xfixes
	root := xproto.Setup(c).DefaultScreen(c).Root

	clipAtom := internAtom(c, "CLIPBOARD")
	utf8Atom := internAtom(c, "UTF8_STRING")
	incrAtom := internAtom(c, "INCR")
	propAtom := internAtom(c, "CLIPMGR_SEL") // куда владелец кладёт конвертированное значение
	if clipAtom == 0 || utf8Atom == 0 || propAtom == 0 {
		log.Println("Wayland-история: не удалось получить атомы")
		c.Close()
		return
	}

	// Невидимое окно-реквестор: владелец шлёт ему SelectionNotify и кладёт данные в prop.
	reqWin, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, 0, reqWin, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, 0, nil)

	xfixes.SelectSelectionInput(c, root, clipAtom, xfixes.SelectionEventMaskSetSelectionOwner)
	log.Println("Wayland-история: слежу за буфером через XWayland (XFIXES)")

	go func() {
		for {
			ev, err := c.WaitForEvent()
			if err != nil {
				continue
			}
			if ev == nil { // соединение закрыто
				return
			}
			switch e := ev.(type) {
			case xfixes.SelectionNotifyEvent:
				// владелец CLIPBOARD сменился → запросить текст в наше свойство
				xproto.ConvertSelection(c, reqWin, clipAtom, utf8Atom, propAtom, e.SelectionTimestamp)
			case xproto.SelectionNotifyEvent:
				if e.Property == 0 { // владелец не отдал UTF8 (картинка/пусто)
					continue
				}
				txt, ok := readSelProp(c, reqWin, propAtom, incrAtom)
				if !ok {
					continue
				}
				glib.IdleAdd(func() bool { ingestText(txt); return false })
			}
		}
	}()
}

func internAtom(c *xgb.Conn, name string) xproto.Atom {
	r, err := xproto.InternAtom(c, false, uint16(len(name)), name).Reply()
	if err != nil || r == nil {
		return 0
	}
	return r.Atom
}

// readSelProp читает и удаляет свойство prop окна win (результат ConvertSelection).
// INCR (очень крупные данные) пропускаем — для истории буфера это не встречается.
func readSelProp(c *xgb.Conn, win xproto.Window, prop, incrAtom xproto.Atom) (string, bool) {
	r, err := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
	if err != nil || r == nil || r.Type == 0 {
		return "", false
	}
	if r.Type == incrAtom { // инкрементальная передача — пропускаем
		return "", false
	}
	if len(r.Value) == 0 || !utf8.Valid(r.Value) {
		return "", false
	}
	return string(r.Value), true
}

func showPopupWayland() {
	if win != nil {
		return
	}
	wlFinishing = false

	w, err := gtk.WindowNew(gtk.WINDOW_TOPLEVEL) // обычный toplevel: под Wayland получает фокус
	if err != nil {
		log.Println("WindowNew:", err)
		return
	}
	w.SetDecorated(false)
	w.SetKeepAbove(true)
	w.SetSkipTaskbarHint(true)
	w.SetSkipPagerHint(true)
	w.SetResizable(false)
	// Центр монитора. Под-курсорную позицию Wayland задать не даёт (mutter игнорирует
	// move; курсор/активное окно не достать) — подробности в шапке файла. Монитор
	// выбирает mutter (обычно активный/подкурсорный); клиент на это не влияет.
	w.SetPosition(gtk.WIN_POS_CENTER_ALWAYS)
	w.SetTypeHint(gdk.WINDOW_TYPE_HINT_DIALOG)
	if screen, err := gdk.ScreenGetDefault(); err == nil && screen.IsComposited() {
		if vis, err := screen.GetRGBAVisual(); err == nil && vis != nil {
			w.SetVisual(vis)
		}
	}

	w.Add(buildPopupBox()) // общий с X11 конструктор содержимого; выставляет listBox/scrolled

	// Enter/Escape перехватываем; стрелки пропускаем в сфокусированный ListBox (нативная навигация).
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
	// Потеря фокуса → закрыть (чтобы попап не висел в списке окон).
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
		listBox.GrabFocus() // чтобы стрелки двигали выбор нативно
	}

	// safety: закрыть через 15с, если что-то пошло не так
	glib.TimeoutAdd(15000, func() bool {
		if win == w {
			finishWayland(false)
		}
		return false
	})
}

// finishWayland закрывает попап и, если paste, кладёт выбранную запись в CLIPBOARD и
// инъектит вставку (после паузы — чтобы фокус успел вернуться на прежнее окно).
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

	// Буфер захватываем, ПОКА попап ещё жив и в фокусе: set_selection под Wayland
	// требует свежий input-serial активной поверхности. Если сделать это после
	// Destroy (фокус уже ушёл), mutter отклонит установку — и вставится старое
	// содержимое буфера (баг «всегда один и тот же текст»).
	//
	// Кладём и в CLIPBOARD, и в PRIMARY: вставляем через Shift+Insert, а VTE-терминалы
	// по нему берут PRIMARY, а не CLIPBOARD (GUI-поля берут CLIPBOARD). Без PRIMARY в
	// консоль вставлялась бы старая мышиная выделенка, а не выбранная запись.
	paste = paste && it != nil
	if paste {
		if it.kind == kindImage {
			// Картинку кладём только в CLIPBOARD (PRIMARY/терминалы её не берут) и
			// вставляем Ctrl+V — Shift+Insert для картинки бессмысленен.
			setClipboardImage(it.png, it.pix)
		} else {
			// Текст — в оба селекшна: Shift+Insert в GUI берёт CLIPBOARD, в VTE — PRIMARY.
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
