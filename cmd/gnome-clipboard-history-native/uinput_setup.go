//go:build linux

// uinput_setup.go — one-time privileged setup of access to /dev/uinput
// (--setup-input / --remove-input), needed for synthetic pasting on Wayland.
//
// By design Wayland forbids injecting input into other apps' windows, so we paste
// through our own /dev/uinput device (see internal/uinput) — exactly like CopyQ
// via ydotool. The device is root-only by default; for the daemon (an ordinary
// user process) to write to it, a udev rule must be installed once.
//
// The rule is a system artifact (installed by root once per machine). Two delivery channels:
//   - .deb/.rpm: the package statically ships the same rule to /usr/lib/udev/rules.d —
//     then access is available right after install and the code below is not used;
//   - bare binary: gnome-clipboard-history-native escalates itself (pkexec/sudo) and writes the rule to /etc.
//
// The privileged write is performed by OUR OWN binary via the hidden subcommand
// __setup-input-root — this keeps all operations in Go (os.WriteFile), without fragile
// shell heredocs and quoting.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const (
	// When we write the rule ourselves — put it in /etc (overrides distro rules).
	udevRulePath    = "/etc/udev/rules.d/60-gnome-clipboard-history-native-uinput.rules"
	modulesLoadPath = "/etc/modules-load.d/gnome-clipboard-history-native-uinput.conf"
	// The package may have placed the same rule here — then setup is already done.
	pkgUdevRulePath = "/usr/lib/udev/rules.d/60-gnome-clipboard-history-native-uinput.rules"
)

// udevRuleContent — the single rule text (the .deb ships the same one). uaccess grants
// an instant ACL to the user of the active local session (systemd, no re-login and
// no group) — the only mechanism, with no fallback paths.
const udevRuleContent = `# gnome-clipboard-history-native: access to /dev/uinput for synthetic pasting on Wayland (Shift+Insert).
# uaccess — an instant ACL for the user of the active local session (systemd, no re-login needed).
KERNEL=="uinput", SUBSYSTEM=="misc", TAG+="uaccess", OPTIONS+="static_node=uinput"
`

const modulesLoadContent = `# gnome-clipboard-history-native: uinput is needed for pasting on Wayland
uinput
`

// resolveExe returns the absolute path of the current binary (resolving symlinks).
// Needed for autostart, the hotkey, and escalation (pkexec/sudo require a full path).
func resolveExe() string {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("cannot determine binary path: %v", err)
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	return exe
}

// ruleInstalled — whether our udev rule is already present (in /etc or installed by the package).
func ruleInstalled() bool {
	for _, p := range []string{udevRulePath, pkgUdevRulePath} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// runSetupInput — user-facing (--setup-input): set up access to /dev/uinput once.
func runSetupInput() {
	// Access already exists — pasting works, no need to escalate (don't invoke sudo needlessly).
	if uinput.HasAccess() {
		if ruleInstalled() {
			fmt.Println("Access to /dev/uinput is already configured — nothing to do.")
		} else {
			fmt.Println("Access to /dev/uinput already exists — pasting on Wayland works, no rule needed.")
		}
		return
	}
	exe := resolveExe()

	if os.Geteuid() == 0 {
		// Already root (sudo gnome-clipboard-history-native --setup-input or deb postinst) — set up directly.
		runSetupInputPrivileged()
		fmt.Println("Done. Access to /dev/uinput is configured (uaccess).")
		return
	}

	if err := elevateSelf(exe, "__setup-input-root"); err != nil {
		log.Fatalf("privileged setup failed: %v", err)
	}

	// An ordinary user again — we can honestly check our own access.
	if uinput.HasAccess() {
		fmt.Println("Done. Access to /dev/uinput obtained — pasting on Wayland will work.")
		restartDaemon(exe)
	} else {
		fmt.Println("The udev rule is installed, but the ACL hasn't been granted yet — try logging out of the session and back in.")
	}
}

// runSetupInputPrivileged — hidden __setup-input-root: the actual privileged steps.
func runSetupInputPrivileged() {
	if os.Geteuid() != 0 {
		log.Fatal("__setup-input-root requires root")
	}
	loadModule()
	if err := writeSystemFile(udevRulePath, udevRuleContent); err != nil {
		log.Fatalf("writing rule %s: %v", udevRulePath, err)
	}
	fmt.Println("udev rule:", udevRulePath)
	if err := writeSystemFile(modulesLoadPath, modulesLoadContent); err != nil {
		log.Fatalf("writing %s: %v", modulesLoadPath, err)
	}
	reloadUdev()
}

// runRemoveInput / runRemoveInputPrivileged — remove the rule (on --uninstall/--remove-input).
func runRemoveInput() {
	if os.Geteuid() == 0 {
		runRemoveInputPrivileged()
		return
	}
	if !ruleInstalled() {
		fmt.Println("The udev rule is not installed — nothing to remove.")
		return
	}
	if err := elevateSelf(resolveExe(), "__remove-input-root"); err != nil {
		log.Fatalf("could not remove the rule: %v", err)
	}
}

func runRemoveInputPrivileged() {
	if os.Geteuid() != 0 {
		log.Fatal("__remove-input-root requires root")
	}
	for _, p := range []string{udevRulePath, modulesLoadPath} {
		if err := os.Remove(p); err == nil {
			fmt.Println("removed:", p)
		}
	}
	reloadUdev()
	// We leave input-group membership alone — it's harmless and may be needed by other tools.
}

// --- privileged steps (run as root) ---

func writeSystemFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func loadModule() {
	if err := exec.Command("modprobe", "uinput").Run(); err != nil {
		// Not critical: the module may be compiled into the kernel.
		log.Printf("modprobe uinput: %v (may be compiled into the kernel — not critical)", err)
	}
}

func reloadUdev() {
	if err := exec.Command("udevadm", "control", "--reload-rules").Run(); err != nil {
		log.Printf("udevadm control --reload-rules: %v", err)
	}
	if err := exec.Command("udevadm", "trigger", "--subsystem-match=misc", "--sysname-match=uinput").Run(); err != nil {
		log.Printf("udevadm trigger: %v", err)
	}
}

// --- escalation and utilities ---

// elevateSelf re-runs our binary as root: sudo from a terminal, otherwise pkexec (GUI).
func elevateSelf(exe string, args ...string) error {
	full := append([]string{exe}, args...)
	var name string
	switch {
	case isStdinTTY() && haveCmd("sudo"):
		name = "sudo"
	case haveCmd("pkexec"):
		name = "pkexec"
	case haveCmd("sudo"):
		name = "sudo"
	default:
		return fmt.Errorf("sudo or pkexec is required to configure permissions on %s", uinput.DevPath)
	}
	c := exec.Command(name, full...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// restartDaemon restarts the daemon so uinput.Init picks up the new access.
func restartDaemon(exe string) {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Write([]byte("quit\n"))
		c.Close()
		time.Sleep(300 * time.Millisecond) // give the socket time to free up
	}
	startDaemon(exe)
}

func haveCmd(name string) bool { _, err := exec.LookPath(name); return err == nil }

func isStdinTTY() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}
