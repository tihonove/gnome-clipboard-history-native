//go:build linux

// install.go — установка/удаление (--install / --uninstall): автозапуск,
// GNOME-хоткей через gsettings custom keybinding и helpers для gsettings и путей.
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
)

const (
	mediaKeysSchema = "org.gnome.settings-daemon.plugins.media-keys"
	customPrefix    = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
	hotkeyBinding   = "<Super><Control>v"
	hotkeyName      = "clipmgr"
)

// runInstall прописывает автозапуск и горячую клавишу на текущий путь бинарника
// и запускает демона. Идемпотентно — можно запускать повторно.
func runInstall() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("не могу определить путь бинарника: %v", err)
	}
	if resolved, e := filepath.EvalSymlinks(exe); e == nil {
		exe = resolved
	}
	installAutostart(exe)
	installHotkey(exe)
	startDaemon(exe)
	fmt.Println("Готово. clipmgr в автозапуске, Super+Ctrl+V настроен, демон запущен.")
}

func runUninstall() {
	if err := os.Remove(autostartPath()); err == nil {
		fmt.Println("убран автозапуск:", autostartPath())
	}
	removeHotkey()
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Write([]byte("quit\n"))
		c.Close()
		fmt.Println("демон остановлен")
	}
	fmt.Println("Готово. clipmgr убран из автозапуска и хоткеев.")
}

func autostartPath() string {
	return filepath.Join(xdgConfigHome(), "autostart", "clipmgr.desktop")
}

func installAutostart(exe string) {
	dir := filepath.Join(xdgConfigHome(), "autostart")
	os.MkdirAll(dir, 0o755)
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=clipmgr\n" +
		"Comment=Clipboard history (Win+V) for GNOME/X11\n" +
		"Exec=" + exe + "\n" +
		"X-GNOME-Autostart-enabled=true\n" +
		"NoDisplay=true\n" +
		"Terminal=false\n"
	if err := os.WriteFile(autostartPath(), []byte(content), 0o644); err != nil {
		log.Fatalf("автозапуск: %v", err)
	}
	fmt.Println("автозапуск:", autostartPath())
}

func installHotkey(exe string) {
	cmd := exe + " --show"
	list := gsList()
	for _, p := range list { // уже установлено? — перевесить binding на актуальный
		if unquote(gsGet(customPath(p), "command")) == cmd {
			gsSet(customPath(p), "binding", quote(hotkeyBinding))
			fmt.Println("хоткей обновлён (Super+Ctrl+V):", p)
			return
		}
	}
	slot := freeSlot(list)
	list = append(list, slot)
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(list))
	gsSet(customPath(slot), "name", quote(hotkeyName))
	gsSet(customPath(slot), "command", quote(cmd))
	gsSet(customPath(slot), "binding", quote(hotkeyBinding))
	fmt.Println("хоткей Super+Ctrl+V →", slot)
}

func removeHotkey() {
	list := gsList()
	kept := make([]string, 0, len(list))
	for _, p := range list {
		if unquote(gsGet(customPath(p), "name")) == hotkeyName {
			// сбросить ключи слота
			for _, k := range []string{"name", "command", "binding"} {
				exec.Command("gsettings", "reset", customPath(p), k).Run()
			}
			fmt.Println("убран хоткей:", p)
			continue
		}
		kept = append(kept, p)
	}
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(kept))
}

// startDaemon запускает демона отдельным сеансом, если он ещё не запущен.
func startDaemon(exe string) {
	if c, err := net.Dial("unix", sockPath()); err == nil {
		c.Close()
		fmt.Println("демон уже запущен")
		return
	}
	c := exec.Command(exe)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		fmt.Println("не удалось запустить демона:", err, "— стартанёт при следующем входе")
		return
	}
	fmt.Println("демон запущен")
}

// --- helpers для gsettings и путей ---

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

// freeSlot возвращает путь первого свободного customN/.
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
