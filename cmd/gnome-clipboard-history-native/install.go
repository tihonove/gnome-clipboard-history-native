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

	"github.com/tihonove/gnome-clipboard-history-native/internal/uinput"
)

const (
	mediaKeysSchema = "org.gnome.settings-daemon.plugins.media-keys"
	customPrefix    = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
)

// Боевые значения хоткея — по умолчанию. env-оверрайды поднимают ПАРАЛЛЕЛЬНЫЙ
// дев-инстанс со своим слотом gsettings и своей клавишей, не трогая установленный:
//
//	GCHN_NAME   — имя слота/автозапуска (напр. gnome-clipboard-history-native-dev),
//	GCHN_HOTKEY — клавиша (напр. <Super><Control>b),
//	GCHN_SOCK   — свой сокет (см. sockPath), прокидывается в команду хоткея.
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

// isDevInstance — задан ли GCHN_NAME (дев). У дева нет автозапуска: демона
// запускаем вручную — вся разница именно в этом.
func isDevInstance() bool { return hotkeyName() != "gnome-clipboard-history-native" }

// showCommand — команда gsettings-хоткея. Для дева прокидываем GCHN_SOCK, чтобы
// попап шёл на дев-сокет, а не на боевой.
func showCommand(exe string) string {
	if s := os.Getenv("GCHN_SOCK"); s != "" {
		return "env GCHN_SOCK=" + s + " " + exe + " --show"
	}
	return exe + " --show"
}

// runInstall прописывает автозапуск и горячую клавишу на текущий путь бинарника
// и запускает демона. Идемпотентно — можно запускать повторно.
func runInstall() {
	exe := resolveExe()
	installAutostart(exe)
	installHotkey(exe)
	startDaemon(exe)
	// Wayland: вставка идёт через /dev/uinput. Если доступа нет и правило ещё не
	// положено пакетом — настраиваем один раз (runSetupInput сам эскалируется и
	// перезапускает демона). На X11 uinput не нужен.
	if isWayland() && !uinput.HasAccess() && !ruleInstalled() {
		fmt.Println("\nWayland: для вставки нужен доступ к /dev/uinput — настраиваю (один раз)…")
		runSetupInput()
	}
	if isDevInstance() {
		fmt.Printf("Готово (dev %q). Хоткей %s настроен, демон запущен.\n", hotkeyName(), hotkeyBinding())
	} else {
		fmt.Printf("Готово. gnome-clipboard-history-native в автозапуске, хоткей %s настроен, демон запущен.\n", hotkeyBinding())
	}
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
	if ruleInstalled() {
		fmt.Println("udev-правило /dev/uinput оставлено (могло понадобиться другим). " +
			"Чтобы убрать: gnome-clipboard-history-native --remove-input")
	}
	fmt.Println("Готово. gnome-clipboard-history-native убран из автозапуска и хоткеев.")
}

func autostartPath() string {
	// имя файла — по GCHN_NAME, чтобы дев-инстанс не затирал боевой автозапуск.
	return filepath.Join(xdgConfigHome(), "autostart", hotkeyName()+".desktop")
}

func installAutostart(exe string) {
	if isDevInstance() {
		return // дев запускаем вручную — автозапуск не нужен
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
		log.Fatalf("автозапуск: %v", err)
	}
	fmt.Println("автозапуск:", autostartPath())
}

func installHotkey(exe string) {
	cmd := showCommand(exe)
	name := hotkeyName()
	list := gsList()
	for _, p := range list { // наш слот (по имени) уже есть? — обновить команду/клавишу
		if unquote(gsGet(customPath(p), "name")) == name {
			gsSet(customPath(p), "command", quote(cmd))
			gsSet(customPath(p), "binding", quote(hotkeyBinding()))
			fmt.Printf("хоткей обновлён (%s → %s): %s\n", name, hotkeyBinding(), p)
			return
		}
	}
	slot := freeSlot(list)
	list = append(list, slot)
	gsSet(mediaKeysSchema, "custom-keybindings", formatList(list))
	gsSet(customPath(slot), "name", quote(name))
	gsSet(customPath(slot), "command", quote(cmd))
	gsSet(customPath(slot), "binding", quote(hotkeyBinding()))
	fmt.Printf("хоткей %s → %s: %s\n", hotkeyBinding(), name, slot)
}

func removeHotkey() {
	list := gsList()
	kept := make([]string, 0, len(list))
	for _, p := range list {
		if unquote(gsGet(customPath(p), "name")) == hotkeyName() {
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
