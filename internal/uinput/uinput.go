//go:build linux

// Package uinput — инъекция клавиш через собственное виртуальное клавиатурное
// устройство /dev/uinput. Используется ТОЛЬКО на Wayland-бэкенде: под нативным
// Wayland XTEST не действует, а kernel-level ввод через uinput доходит и до
// нативных Wayland-окон, и до XWayland, и до консоли.
//
// Устройство создаём один раз при старте демона (Init) и переиспользуем на
// каждую вставку — это убирает латентность создания и гонку «udev/libinput ещё не
// подхватил устройство» (ровно поэтому у ydotool демон долгоживущий). Требует прав
// на запись в /dev/uinput (иначе udev-правило).
package uinput

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Константы ABI ядра (linux/uinput.h, input-event-codes.h) — стабильны, задаём сами,
// т.к. x/sys/unix их не всегда экспортирует.
const (
	evSyn     = 0x00
	evKey     = 0x01
	synReport = 0x00

	keyLeftCtrl  = 29
	keyLeftShift = 42
	keyV         = 47
	keyInsert    = 110

	// ioctl: UINPUT_IOCTL_BASE='U'(0x55); _IOW(0x55,100/101,sizeof(int)); _IO(0x55,1/2).
	uiSetEvbit   = 0x40045564 // UI_SET_EVBIT
	uiSetKeybit  = 0x40045565 // UI_SET_KEYBIT
	uiDevCreate  = 0x5501     // UI_DEV_CREATE
	uiDevDestroy = 0x5502     // UI_DEV_DESTROY
)

var uinputFile *os.File

// uinputUserDev — struct uinput_user_dev из linux/uinput.h (legacy-путь создания).
type uinputUserDev struct {
	Name         [80]byte
	ID           struct{ Bustype, Vendor, Product, Version uint16 }
	FFEffectsMax uint32
	Absmax       [64]int32
	Absmin       [64]int32
	Absfuzz      [64]int32
	Absflat      [64]int32
}

// Init открывает /dev/uinput, регистрирует нужные клавиши и создаёт устройство.
func Init() error {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open /dev/uinput: %w", err)
	}
	fd := int(f.Fd())

	// какие события/клавиши устройство умеет
	if err := unix.IoctlSetInt(fd, uiSetEvbit, evKey); err != nil {
		f.Close()
		return fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	for _, k := range []int{
		keyLeftShift, keyInsert, // Shift+Insert — основной способ
		keyLeftCtrl, keyV, // Ctrl+V — fallback (CLIPMGR_PASTE=ctrlv)
	} {
		if err := unix.IoctlSetInt(fd, uiSetKeybit, k); err != nil {
			f.Close()
			return fmt.Errorf("UI_SET_KEYBIT %d: %w", k, err)
		}
	}

	var dev uinputUserDev
	copy(dev.Name[:], "clipmgr-virtual-kbd")
	dev.ID.Bustype = 0x03 // BUS_USB
	dev.ID.Vendor = 0x1234
	dev.ID.Product = 0x5678
	dev.ID.Version = 1
	if err := binary.Write(f, binary.LittleEndian, &dev); err != nil {
		f.Close()
		return fmt.Errorf("write uinput_user_dev: %w", err)
	}
	if err := unix.IoctlSetInt(fd, uiDevCreate, 0); err != nil {
		f.Close()
		return fmt.Errorf("UI_DEV_CREATE: %w", err)
	}

	// дать udev/компоситору подхватить новое устройство
	time.Sleep(200 * time.Millisecond)
	uinputFile = f
	log.Println("uinput: виртуальная клавиатура создана")
	return nil
}

// Close уничтожает виртуальное устройство. No-op, если Init не вызывался (X11).
func Close() {
	if uinputFile == nil {
		return
	}
	unix.IoctlSetInt(int(uinputFile.Fd()), uiDevDestroy, 0)
	uinputFile.Close()
	uinputFile = nil
}

// inputEvent — struct input_event (64-bit): timeval(16) + type + code + value.
type inputEvent struct {
	Sec   int64
	Usec  int64
	Type  uint16
	Code  uint16
	Value int32
}

func emit(typ, code uint16, value int32) error {
	if uinputFile == nil {
		return fmt.Errorf("uinput не инициализирован")
	}
	ev := inputEvent{Type: typ, Code: code, Value: value}
	b := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))[:]
	_, err := uinputFile.Write(b)
	return err
}

func syn() error { return emit(evSyn, synReport, 0) }

// combo эмитит модификатор+клавишу: press mod, press key, syn; release key, release mod, syn.
func combo(mod, key uint16) {
	if uinputFile == nil {
		log.Println("uinput недоступен — вставка пропущена")
		return
	}
	emit(evKey, mod, 1)
	emit(evKey, key, 1)
	syn()
	emit(evKey, key, 0)
	emit(evKey, mod, 0)
	syn()
}

func injectShiftInsert() { combo(keyLeftShift, keyInsert) }
func injectCtrlV()       { combo(keyLeftCtrl, keyV) }

// InjectPaste выбирает способ вставки на Wayland. По умолчанию Shift+Insert —
// раскладко-независимо, вставляет CLIPBOARD и в терминалах, и в GUI. Скрытый
// env-override CLIPMGR_PASTE=ctrlv — на случай приложения, не понимающего Shift+Insert.
func InjectPaste() {
	if os.Getenv("CLIPMGR_PASTE") == "ctrlv" {
		injectCtrlV()
		return
	}
	injectShiftInsert()
}
