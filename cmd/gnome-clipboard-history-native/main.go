//go:build linux

// gnome-clipboard-history-native — native clipboard history (Super+Ctrl+V) for GNOME (X11 + Wayland).
//
// A resident GTK daemon. On Super+Ctrl+V (via GNOME hotkey → --show → socket) it
// shows a popup at the cursor/window: a "Clipboard" header + a scrollable list of
// entries. Each entry is truncated to 3 lines. Selection is an accent outline
// (Yaru accent) like file focus in Nautilus. Up/Down move the selection, Enter
// pastes the selected entry into the previous window, Escape closes.
//
// Input is taken via xgb (GrabKeyboard on root) — GNOME steals focus from a popped-up
// GTK window (focus-stealing prevention). Because of that we handle list navigation
// ourselves and move the GTK widget's selection manually.
package main

import (
	"fmt"
	"os"
)

// version is baked in at release build time via -ldflags "-X main.version=…".
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
		case "--setup-input":
			runSetupInput()
			return
		case "--remove-input":
			runRemoveInput()
			return
		case "__setup-input-root": // hidden: privileged part (via pkexec/sudo)
			runSetupInputPrivileged()
			return
		case "__remove-input-root": // hidden: privileged part
			runRemoveInputPrivileged()
			return
		case "--version", "-v":
			fmt.Println("gnome-clipboard-history-native", version)
			return
		case "--help", "-h":
			fmt.Println("gnome-clipboard-history-native — clipboard history (Super+Ctrl+V) for GNOME (X11 + basic Wayland)\n" +
				"  gnome-clipboard-history-native             start the daemon\n" +
				"  gnome-clipboard-history-native --install   set up autostart and the Super+Ctrl+V hotkey, start the daemon\n" +
				"  gnome-clipboard-history-native --uninstall remove autostart and the hotkey\n" +
				"  gnome-clipboard-history-native --setup-input  set up /dev/uinput access for pasting once (Wayland)\n" +
				"  gnome-clipboard-history-native --remove-input remove the /dev/uinput udev rule\n" +
				"  gnome-clipboard-history-native --show      show the popup (invoked by the hotkey)\n" +
				"  gnome-clipboard-history-native --version   version\n" +
				"\n" +
				"Wayland (GNOME): centered popup, pasting via /dev/uinput (Shift+Insert),\n" +
				"history via an XWayland bridge (mutter mirrors the buffer to the X11 CLIPBOARD).\n" +
				"  * /dev/uinput access is set up automatically on --install\n" +
				"    (or separately: gnome-clipboard-history-native --setup-input; the .deb installs the udev rule itself);\n" +
				"  * history requires XWayland (usually already enabled);\n" +
				"  * configure layout switching via GNOME Tweaks (not Settings),\n" +
				"    otherwise modifiers get \"eaten\" and the hotkey/paste break on the 2nd layout.")
			return
		}
	}
	runDaemon()
}
