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

// selectionAtoms — набор атомов X-селекшна, нужных мосту (интернируем один раз).
type selectionAtoms struct {
	clip, prop, targets, utf8, png, incr xproto.Atom
}

const maxSelBytes = maxImageBytes // предохранитель на размер одного чтения (в т.ч. INCR)

// startClipboardWatchWayland — фоновый мониторинг буфера под GNOME Wayland через
// XWayland. Штатный wl-путь чужие копирования в фоне не видит (нет data-control), но
// mutter зеркалит Wayland-буфер в X11 CLIPBOARD, поэтому X-клиент получает XFIXES-
// уведомления о смене владельца (в т.ч. от нативных Wayland-приложений). При смене
// владельца спрашиваем TARGETS и берём текст (UTF8_STRING) либо картинку (image/png) —
// так же, как CopyQ, без внешнего xsel. Крупные данные (скриншоты) приходят по INCR —
// читаем их кусками (см. readSelectionBytes). Отдельное xgb-соединение (не GTK),
// крутится в своей горутине; ingest — через IdleAdd. Требует только XWayland ($DISPLAY).
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

	a := selectionAtoms{
		clip:    internAtom(c, "CLIPBOARD"),
		prop:    internAtom(c, "GCHN_SEL"), // куда владелец кладёт конвертированное значение
		targets: internAtom(c, "TARGETS"),
		utf8:    internAtom(c, "UTF8_STRING"),
		png:     internAtom(c, "image/png"),
		incr:    internAtom(c, "INCR"),
	}
	if a.clip == 0 || a.prop == 0 || a.targets == 0 || a.utf8 == 0 {
		log.Println("Wayland-история: не удалось получить атомы")
		c.Close()
		return
	}

	// Невидимое окно-реквестор: владелец шлёт ему SelectionNotify и кладёт данные в prop.
	// PropertyChange нужен для INCR (ждём PropertyNotify на каждый кусок).
	reqWin, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, 0, reqWin, root, 0, 0, 1, 1, 0,
		xproto.WindowClassInputOnly, 0, xproto.CwEventMask,
		[]uint32{xproto.EventMaskPropertyChange})

	xfixes.SelectSelectionInput(c, root, a.clip, xfixes.SelectionEventMaskSetSelectionOwner)
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
				// владелец CLIPBOARD сменился → сперва спросить список доступных таргетов
				xproto.ConvertSelection(c, reqWin, a.clip, a.targets, a.prop, e.SelectionTimestamp)
			case xproto.SelectionNotifyEvent:
				if e.Property == 0 { // владелец не отдал запрошенный таргет
					continue
				}
				handleSelectionNotify(c, reqWin, a, e)
			}
		}
	}()
}

// handleSelectionNotify обрабатывает ответ владельца на ConvertSelection. По e.Target
// понимаем этап: TARGETS → выбираем текст/картинку и запрашиваем их; UTF8/png → читаем
// значение (с поддержкой INCR) и кладём в историю. Вызывается из горутины моста.
func handleSelectionNotify(c *xgb.Conn, win xproto.Window, a selectionAtoms, e xproto.SelectionNotifyEvent) {
	switch e.Target {
	case a.targets:
		targets := readAtomList(c, win, a.prop)
		switch {
		case hasAtom(targets, a.utf8): // текст приоритетнее картинки
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

// readAtomList читает и удаляет свойство-список атомов (ответ на ConvertSelection TARGETS).
// Значения — 32-битные атомы; парсим через xgb.Get32 (учитывает порядок байт сервера).
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

// readSelectionBytes читает и удаляет свойство prop окна win (результат ConvertSelection),
// возвращая сырые байты. Крупные значения приходят по INCR: свойство типа INCR — маркер
// начала, само удаление свойства (delete=true) сигналит владельцу слать куски; дальше
// на каждый кусок владелец шлёт PropertyNotify(NewValue), пустой кусок — конец передачи.
// Вызывается синхронно из горутины моста, поэтому спокойно тянет события сама.
func readSelectionBytes(c *xgb.Conn, win xproto.Window, prop, incrAtom xproto.Atom) ([]byte, bool) {
	r, err := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
	if err != nil || r == nil || r.Type == 0 {
		return nil, false
	}
	if r.Type != incrAtom {
		if len(r.Value) == 0 {
			return nil, false
		}
		return append([]byte(nil), r.Value...), true // Value переиспользуется — копируем
	}
	// INCR: собираем куски по PropertyNotify, пока не придёт пустой.
	var buf []byte
	for {
		ev, werr := c.WaitForEvent()
		if werr != nil {
			continue
		}
		if ev == nil { // соединение закрыто
			return nil, false
		}
		pn, ok := ev.(xproto.PropertyNotifyEvent)
		if !ok || pn.Window != win || pn.Atom != prop || pn.State != xproto.PropertyNewValue {
			continue // не наш кусок — посторонние события во время INCR игнорируем
		}
		cr, cerr := xproto.GetProperty(c, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<20).Reply()
		if cerr != nil || cr == nil {
			return nil, false
		}
		if len(cr.Value) == 0 { // пустой кусок — конец
			return buf, len(buf) > 0
		}
		buf = append(buf, cr.Value...)
		if len(buf) > maxSelBytes { // защита от разбухания
			return nil, false
		}
	}
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
