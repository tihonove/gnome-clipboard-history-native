//go:build linux

// Package uinput — key injection through our own virtual keyboard device
// /dev/uinput. Used ONLY on the Wayland backend: under native Wayland XTEST
// doesn't work, while kernel-level input via uinput reaches native Wayland
// windows, XWayland, and the console alike.
//
// We create the device once at daemon startup (Init) and reuse it for
// every paste — this removes creation latency and the "udev/libinput hasn't
// picked up the device yet" race (exactly why ydotool keeps a long-lived daemon). Requires
// write access to /dev/uinput (otherwise a udev rule).
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

// Kernel ABI constants (linux/uinput.h, input-event-codes.h) — stable, we define them
// ourselves since x/sys/unix doesn't always export them.
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

// DevPath — path to the kernel device for input injection.
const DevPath = "/dev/uinput"

var uinputFile *os.File

// HasAccess reports whether we have write access to /dev/uinput (node exists and W_OK).
// Also false if the uinput module isn't loaded (no node) — we treat that too as
// "setup needed" (see gnome-clipboard-history-native --setup-input).
func HasAccess() bool { return unix.Access(DevPath, unix.W_OK) == nil }

// uinputUserDev — struct uinput_user_dev from linux/uinput.h (legacy creation path).
type uinputUserDev struct {
	Name         [80]byte
	ID           struct{ Bustype, Vendor, Product, Version uint16 }
	FFEffectsMax uint32
	Absmax       [64]int32
	Absmin       [64]int32
	Absfuzz      [64]int32
	Absflat      [64]int32
}

// Init opens /dev/uinput, registers the needed keys, and creates the device.
func Init() error {
	f, err := os.OpenFile(DevPath, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", DevPath, err)
	}
	fd := int(f.Fd())

	// which events/keys the device supports
	if err := unix.IoctlSetInt(fd, uiSetEvbit, evKey); err != nil {
		f.Close()
		return fmt.Errorf("UI_SET_EVBIT: %w", err)
	}
	for _, k := range []int{
		keyLeftShift, keyInsert, // Shift+Insert — the primary method
		keyLeftCtrl, keyV, // Ctrl+V — fallback (GCHN_PASTE=ctrlv)
	} {
		if err := unix.IoctlSetInt(fd, uiSetKeybit, k); err != nil {
			f.Close()
			return fmt.Errorf("UI_SET_KEYBIT %d: %w", k, err)
		}
	}

	var dev uinputUserDev
	copy(dev.Name[:], "gchn-virtual-kbd")
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

	// let udev/the compositor pick up the new device
	time.Sleep(200 * time.Millisecond)
	uinputFile = f
	log.Println("uinput: virtual keyboard created")
	return nil
}

// Close destroys the virtual device. No-op if Init was never called (X11).
func Close() {
	if uinputFile == nil {
		return
	}
	_ = unix.IoctlSetInt(int(uinputFile.Fd()), uiDevDestroy, 0) // best-effort teardown
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
		return fmt.Errorf("uinput not initialized")
	}
	ev := inputEvent{Type: typ, Code: code, Value: value}
	b := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))[:]
	_, err := uinputFile.Write(b)
	return err
}

func syn() error { return emit(evSyn, synReport, 0) }

// combo emits modifier+key: press mod, press key, syn; release key, release mod, syn.
// Write errors are deliberately ignored: mid-combo — there's nothing to roll back.
func combo(mod, key uint16) {
	if uinputFile == nil {
		log.Println("uinput unavailable — paste skipped")
		return
	}
	_ = emit(evKey, mod, 1)
	_ = emit(evKey, key, 1)
	_ = syn()
	_ = emit(evKey, key, 0)
	_ = emit(evKey, mod, 0)
	_ = syn()
}

func injectShiftInsert() { combo(keyLeftShift, keyInsert) }
func injectCtrlV()       { combo(keyLeftCtrl, keyV) }

// InjectPaste chooses the paste method on Wayland. By default Shift+Insert —
// layout-independent and works in both terminals and GUIs. IMPORTANT: GUI fields take
// CLIPBOARD via it, while VTE terminals take PRIMARY, so the caller
// (finishWayland) puts the selected entry into BOTH selections. Hidden env override
// GCHN_PASTE=ctrlv — for an app that doesn't understand Shift+Insert.
func InjectPaste() {
	if os.Getenv("GCHN_PASTE") == "ctrlv" {
		injectCtrlV()
		return
	}
	injectShiftInsert()
}

// InjectPasteCtrlV — force Ctrl+V. Needed for pasting an image: Shift+Insert
// in terminals takes PRIMARY (we don't put the image there), while GUI applications, where
// the image is actually pasted, understand Ctrl+V.
func InjectPasteCtrlV() { injectCtrlV() }
