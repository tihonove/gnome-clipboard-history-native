//go:build linux

// daemon.go — резидентная часть: единственный инстанс на сокете, инициализация
// бэкенда (X11/Wayland), слушалка буфера и история (только в памяти).
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

const maxHistory = 100 // сколько записей держим в памяти

var (
	clipboard *gtk.Clipboard // CLIPBOARD: и слушаем, и владеем им при вставке
	primary   *gtk.Clipboard // PRIMARY: нужен для вставки Shift+Insert в VTE-терминалы (Wayland)
	history   []string       // история буфера, свежие сверху (только в памяти)

	// Когда мы сами кладём запись в буфер при вставке — не хотим двигать её наверх
	// истории (выбранный элемент должен остаться на месте). Помечаем такой self-set,
	// чтобы owner-change его пропустил.
	selfSetText    string
	selfSetPending bool
)

func runDaemon() {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		log.Fatal("демон уже запущен")
	}

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if isWayland() {
		// Wayland: xgb-грабы/XTEST не работают — вставка через собственное
		// uinput-устройство (см. internal/uinput). История — через XWayland-мост
		// (startClipboardWatchWayland в wayland.go).
		if err := uinput.Init(); err != nil {
			log.Printf("uinput недоступен (%v). Вставка на Wayland отключена — "+
				"выполните один раз: clipmgr --setup-input", err)
		}
	} else {
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
	}

	gtk.Init(nil)
	applyCSS()
	startClipboardWatch()
	startSocketListener()

	log.Printf("daemon (GTK, %s): слушаю сокет %s — жду --show", sessionKind(), sockPath())
	gtk.Main()
	uinput.Close() // no-op на X11
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

// ---------- слушалка буфера ----------

func startClipboardWatch() {
	var err error
	clipboard, err = gtk.ClipboardGet(gdk.SELECTION_CLIPBOARD)
	if err != nil {
		log.Fatalf("clipboard: %v", err)
	}
	if isWayland() {
		// Под GNOME Wayland фоновый GTK owner-change чужие копирования не видит
		// (нет data-control). Историю снимаем через XWayland: mutter зеркалит буфер
		// в X11 CLIPBOARD, и X-клиент получает XFIXES-уведомления — см. wayland.go.
		startClipboardWatchWayland()
		return
	}
	clipboard.Connect("owner-change", func() {
		// WaitForText прямо в обработчике сигнала небезопасен (реентранси) —
		// откладываем на следующий idle в том же GTK-потоке.
		glib.IdleAdd(func() bool {
			if txt, e := clipboard.WaitForText(); e == nil {
				ingestClipboard(txt)
			}
			return false
		})
	})
}

// ingestClipboard кладёт новое содержимое буфера в историю, пропуская наши
// собственные вставки (self-set) — чтобы выбранная запись не прыгала наверх.
// Вызывать только из главного GTK-потока.
func ingestClipboard(txt string) {
	if selfSetPending && txt == selfSetText {
		selfSetPending = false // это наша же вставка — порядок не трогаем
		return
	}
	addToHistory(txt)
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

// setClipboard делает демона владельцем CLIPBOARD с текстом s. Пока демон жив, он
// сам обслуживает запросы вставки — поэтому внешний xsel/xclip не нужен.
// Вызывается только при вставке выбранной записи, поэтому помечаем self-set: пришедший
// следом owner-change с этим текстом не должен двигать запись наверх истории.
func setClipboard(s string) {
	if clipboard != nil {
		selfSetText = s
		selfSetPending = true
		clipboard.SetText(s)
	}
}

// setPrimary делает демона владельцем PRIMARY с текстом s. Нужно на Wayland: VTE-
// терминалы по Shift+Insert вставляют именно PRIMARY (а не CLIPBOARD), поэтому без
// этого в консоль вставлялось бы старое содержимое выделения, а не выбранная запись.
// PRIMARY мы не мониторим (историю снимаем с CLIPBOARD), так что self-set-метка не нужна.
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
	// CLIPMGR_SOCK — отдельный сокет для дев-инстанса, чтобы не толкаться с
	// установленным демоном (общий сокет — единственный конфликт single-instance).
	if s := os.Getenv("CLIPMGR_SOCK"); s != "" {
		return s
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, "clipmgr.sock")
}
