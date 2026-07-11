//go:build linux

// install.go — install/uninstall (--install / --uninstall): autostart,
// GNOME hotkey via a gsettings custom keybinding, and helpers for gsettings and paths.
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

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const (
	mediaKeysSchema = "org.gnome.settings-daemon.plugins.media-keys"
	customPrefix    = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
)

// Production hotkey values — the defaults. env overrides bring up a PARALLEL
// dev instance with its own gsettings slot and its own key, without touching the installed one:
//
//	GCHN_NAME   — slot/autostart name (e.g. gnome-clipboard-history-native-dev),
//	GCHN_HOTKEY — key (e.g. <Super><Control>b),
//	GCHN_SOCK   — its own socket (see sockPath), passed into the hotkey command.
func hotkeyName() string {
	if n := os.Getenv("GCHN_NAME"); n != "" {
		return n
	}
	return "gnome-clipboard-history-native"
}

func hotkeyBinding() string {
	if b := os.Getenv("GCHN_HOTKEY"); b != "" {
		return b
	}
	return "<Super><Control>v"
}

// isDevInstance — whether GCHN_NAME is set (dev). The dev instance has no autostart: the daemon
// is started manually — that's the whole difference.
func isDevInstance() bool { return hotkeyName() != "gnome-clipboard-history-native" }

// showCommand — the gsettings hotkey command. For the dev instance we pass GCHN_SOCK so
// the popup goes to the dev socket rather than the production one.
func showCommand(exe string) string {
	if s := os.Getenv("GCHN_SOCK"); s != "" {
		return "env GCHN_SOCK=" + s + " " + exe + " --show"
	}
	return exe + " --show"
}

// runInstall registers autostart and the hotkey pointing at the current binary path
// and starts the daemon. Idempotent — safe to run repeatedly.
func runInstall() {
	exe := resolveExe()
	installAutostart(exe)
	installHotkey(exe)
	startDaemon(exe)
	// Wayland: paste goes through /dev/uinput. If there's no access and the rule hasn't
	// been placed by the package yet — set it up once (runSetupInput escalates itself and
	// restarts the daemon). On X11 uinput is not needed.
	if isWayland() && !uinput.HasAccess() && !ruleInstalled() {
		fmt.Println("\nWayland: paste needs access to /dev/uinput — setting it up (one time)…")
		runSetupInput()
	}
	if isDevInstance() {
		fmt.Printf("Done (dev %q). Hotkey %s configured, daemon started.\n", hotkeyName(), hotkeyBinding())
	} else {
		fmt.Printf("Done. gnome-clipboard-history-native is in autostart, hotkey %s configured, daemon started.\n", hotkeyBinding())
	}
}

func runUninstall() {
	if err := os.Remove(autostartPath()); err == nil {
		fmt.Println("removed autostart:", autostartPath())
	}
	removeHotkey()
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Write([]byte("quit\n"))
		c.Close()
		fmt.Println("daemon stopped")
	}
	if ruleInstalled() {
		fmt.Println("udev rule for /dev/uinput left in place (it may be needed by others). " +
			"To remove it: gnome-clipboard-history-native --remove-input")
	}
	fmt.Println("Done. gnome-clipboard-history-native removed from autostart and hotkeys.")
}

func autostartPath() string {
	// file name — based on GCHN_NAME, so a dev instance doesn't overwrite the production autostart.
	return filepath.Join(xdgConfigHome(), "autostart", hotkeyName()+".desktop")
}

func installAutostart(exe string) {
	if isDevInstance() {
		return // the dev instance is started manually — no autostart needed
	}
	dir := filepath.Join(xdgConfigHome(), "autostart")
	os.MkdirAll(dir, 0o755)
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=gnome-clipboard-history-native\n" +
		"Comment=Clipboard history (Super+Ctrl+V) for GNOME on X11 & Wayland\n" +
		"Exec=" + exe + "\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		"NoDisplay=true\n" +
		"Terminal=false\n"
	if err := os.WriteFile(autostartPath(), []byte(content), 0o644); err != nil {
		log.Fatalf("autostart: %v", err)
	}
	fmt.Println("autostart:", autostartPath())
}

func installHotkey(exe string) {
	cmd := showCommand(exe)
	name := hotkeyName()
	list := gsList()
	for _, p := range list { // does our slot (by name) already exist? — update command/key
		if unquote(gsGet(customPath(p), "name")) == name {
			gsSet(customPath(p), "command", quote(cmd))
			gsSet(customPath(p), "binding", quote(hotkeyBinding()))
			fmt.Printf("hotkey updated (%s → %s): %s\n", name, hotkeyBinding(), p)
			return
		}
	}
	slot := freeSlot(list)
	list = append(list, slot)
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(list))
	gsSet(customPath(slot), "name", quote(name))
	gsSet(customPath(slot), "command", quote(cmd))
	gsSet(customPath(slot), "binding", quote(hotkeyBinding()))
	fmt.Printf("hotkey %s → %s: %s\n", hotkeyBinding(), name, slot)
}

func removeHotkey() {
	list := gsList()
	kept := make([]string, 0, len(list))
	for _, p := range list {
		if unquote(gsGet(customPath(p), "name")) == hotkeyName() {
			// reset the slot's keys
			for _, k := range []string{"name", "command", "binding"} {
				exec.Command("gsettings", "reset", customPath(p), k).Run()
			}
			fmt.Println("removed hotkey:", p)
			continue
		}
		kept = append(kept, p)
	}
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(kept))
}

// startDaemon starts the daemon in a separate session, if it isn't already running.
func startDaemon(exe string) {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		fmt.Println("daemon is already running")
		return
	}
	c := exec.Command(exe)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		fmt.Println("failed to start the daemon:", err, "— it will start at the next login")
		return
	}
	fmt.Println("daemon started")
}

// --- helpers for gsettings and paths ---

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

// freeSlot returns the path of the first free customN/.
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
