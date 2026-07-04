//go:build linux

// uinput_setup.go — единоразовая привилегированная настройка доступа к /dev/uinput
// (--setup-input / --remove-input), нужная для синтетической вставки на Wayland.
//
// Wayland по дизайну запрещает инжект ввода в чужие окна, поэтому вставку мы делаем
// через собственное устройство /dev/uinput (см. internal/uinput) — ровно как CopyQ
// через ydotool. Устройство по умолчанию доступно только root; чтобы демон (обычный
// пользовательский процесс) мог в него писать, нужно один раз положить udev-правило.
//
// Правило — системный артефакт (ставится root'ом раз на машину). Два канала доставки:
//   - .deb/.rpm: пакет статически кладёт то же правило в /usr/lib/udev/rules.d —
//     тогда доступ есть сразу после установки, код ниже не задействуется;
//   - голый бинарник: clipmgr сам эскалируется (pkexec/sudo) и пишет правило в /etc.
//
// Привилегированную запись делает НАШ ЖЕ бинарник со скрытым сабкомандом
// __setup-input-root — так все операции остаются на Go (os.WriteFile), без хрупких
// shell-heredoc и кавычек.
package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const (
	// Когда правило пишем сами — кладём в /etc (перекрывает дистрибутивные правила).
	udevRulePath    = "/etc/udev/rules.d/60-clipmgr-uinput.rules"
	modulesLoadPath = "/etc/modules-load.d/clipmgr-uinput.conf"
	// Пакет мог положить то же правило сюда — тогда настройка уже сделана.
	pkgUdevRulePath = "/usr/lib/udev/rules.d/60-clipmgr-uinput.rules"
	inputGroup      = "input"
)

// udevRuleContent — единый текст правила (тот же кладёт и .deb). uaccess даёт
// мгновенный ACL активному пользователю (systemd, без релогина); GROUP=input —
// запасной путь для logind, не выдающих ACL на не-seat узел (нужен повторный вход).
const udevRuleContent = `# clipmgr: доступ к /dev/uinput для синтетической вставки на Wayland (Shift+Insert).
# uaccess — мгновенный ACL активному пользователю (systemd, без релогина);
# GROUP=input — запасной путь (нужен повторный вход в сессию).
KERNEL=="uinput", SUBSYSTEM=="misc", MODE="0660", GROUP="input", TAG+="uaccess", OPTIONS+="static_node=uinput"
`

const modulesLoadContent = `# clipmgr: uinput нужен для вставки на Wayland
uinput
`

// resolveExe возвращает абсолютный путь текущего бинарника (с разыменованием симлинков).
// Нужен для автозапуска, хоткея и эскалации (pkexec/sudo требуют полный путь).
func resolveExe() string {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("не могу определить путь бинарника: %v", err)
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	return exe
}

// ruleInstalled — стоит ли уже наше udev-правило (в /etc или положенное пакетом).
func ruleInstalled() bool {
	for _, p := range []string{udevRulePath, pkgUdevRulePath} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// runSetupInput — user-facing (--setup-input): один раз настроить доступ к /dev/uinput.
func runSetupInput() {
	// Доступ уже есть — вставка работает, эскалироваться незачем (не дёргаем sudo зря).
	if uinput.HasAccess() {
		if ruleInstalled() {
			fmt.Println("Доступ к /dev/uinput уже настроен — ничего делать не нужно.")
		} else {
			fmt.Println("Доступ к /dev/uinput уже есть — вставка на Wayland работает, правило не требуется.")
		}
		return
	}
	exe := resolveExe()
	u, err := user.Current()
	if err != nil {
		log.Fatalf("не могу определить пользователя: %v", err)
	}

	if os.Geteuid() == 0 {
		// Уже root (sudo clipmgr --setup-input или deb-postinst) — настраиваем напрямую.
		// Проверить доступ реального пользователя отсюда нельзя (root всегда W_OK),
		// поэтому только сообщаем про возможный релогин.
		runSetupInputPrivileged(u.Username)
		fmt.Println("Готово. Если вставка не заработает сразу — выйдите из сессии и войдите снова.")
		return
	}

	if err := elevateSelf(exe, "__setup-input-root", u.Username); err != nil {
		log.Fatalf("привилегированная настройка не удалась: %v", err)
	}

	// Снова обычный пользователь — можно честно проверить свой доступ.
	if uinput.HasAccess() {
		fmt.Println("Готово. Доступ к /dev/uinput получен — вставка на Wayland заработает.")
		restartDaemon(exe)
	} else {
		fmt.Println("udev-правило установлено, но доступ появится после выхода из сессии и " +
			"повторного входа (членство в группе `input`).")
	}
}

// runSetupInputPrivileged — скрытый __setup-input-root: собственно привилегированные шаги.
func runSetupInputPrivileged(username string) {
	if os.Geteuid() != 0 {
		log.Fatal("__setup-input-root требует root")
	}
	loadModule()
	if err := writeSystemFile(udevRulePath, udevRuleContent); err != nil {
		log.Fatalf("запись правила %s: %v", udevRulePath, err)
	}
	fmt.Println("udev-правило:", udevRulePath)
	if err := writeSystemFile(modulesLoadPath, modulesLoadContent); err != nil {
		log.Fatalf("запись %s: %v", modulesLoadPath, err)
	}
	// Членство в группе input — запасной путь на случай, если uaccess не сработает.
	addUserToInputGroup(username)
	reloadUdev()
}

// runRemoveInput / runRemoveInputPrivileged — снять правило (при --uninstall/--remove-input).
func runRemoveInput() {
	if os.Geteuid() == 0 {
		runRemoveInputPrivileged()
		return
	}
	if !ruleInstalled() {
		fmt.Println("udev-правило не установлено — нечего убирать.")
		return
	}
	if err := elevateSelf(resolveExe(), "__remove-input-root"); err != nil {
		log.Fatalf("не удалось убрать правило: %v", err)
	}
}

func runRemoveInputPrivileged() {
	if os.Geteuid() != 0 {
		log.Fatal("__remove-input-root требует root")
	}
	for _, p := range []string{udevRulePath, modulesLoadPath} {
		if err := os.Remove(p); err == nil {
			fmt.Println("удалено:", p)
		}
	}
	reloadUdev()
	// Членство в группе input не трогаем — безвредно и могло понадобиться другим утилитам.
}

// --- привилегированные шаги (выполняются под root) ---

func writeSystemFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func loadModule() {
	if err := exec.Command("modprobe", "uinput").Run(); err != nil {
		// Не критично: модуль мог быть вкомпилен в ядро.
		log.Printf("modprobe uinput: %v (возможно, вкомпилен в ядро — не критично)", err)
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

func addUserToInputGroup(username string) {
	if username == "" || username == "root" {
		return
	}
	if haveCmd("usermod") {
		if err := exec.Command("usermod", "-aG", inputGroup, username).Run(); err == nil {
			return
		}
	}
	if haveCmd("gpasswd") {
		if err := exec.Command("gpasswd", "-a", username, inputGroup).Run(); err != nil {
			log.Printf("gpasswd -a %s %s: %v", username, inputGroup, err)
		}
	}
}

// --- эскалация и утилиты ---

// elevateSelf перезапускает наш бинарник под root: sudo из терминала, иначе pkexec (GUI).
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
		return fmt.Errorf("нужен sudo или pkexec для настройки прав на %s", uinput.DevPath)
	}
	c := exec.Command(name, full...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

// restartDaemon перезапускает демона, чтобы uinput.Init подхватил новый доступ.
func restartDaemon(exe string) {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Write([]byte("quit\n"))
		c.Close()
		time.Sleep(300 * time.Millisecond) // дать сокету освободиться
	}
	startDaemon(exe)
}

func haveCmd(name string) bool { _, err := exec.LookPath(name); return err == nil }

func isStdinTTY() bool {
	_, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	return err == nil
}
