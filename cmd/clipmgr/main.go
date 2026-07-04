//go:build linux

// clipmgr — нативная история буфера обмена (Win+V) для GNOME/X11.
//
// Резидентный GTK-демон. По Super+V (через GNOME-хоткей → clipmgr --show → сокет)
// показывает у курсора/окна попап: заголовок "Clipboard" + прокручиваемый список
// записей. Каждая запись обрезается до 3 строк. Выделение — акцентная обводка
// (Yaru accent) как фокус файла в Nautilus. Up/Down двигают выделение, Enter
// вставляет выбранное в прежнее окно, Escape закрывает.
//
// Ввод берём через xgb (GrabKeyboard на root) — у всплывшего GTK-окна GNOME
// отбирает фокус (focus-stealing prevention). Навигацию по списку из-за этого
// делаем сами и вручную двигаем выделение GTK-виджета.
package main

import (
	"fmt"
	"os"
)

// version зашивается при релизной сборке через -ldflags "-X main.version=…".
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
		case "--version", "-v":
			fmt.Println("clipmgr", version)
			return
		case "--help", "-h":
			fmt.Println("clipmgr — история буфера (Super+Ctrl+V) для GNOME (X11 + базовый Wayland)\n" +
				"  clipmgr             запустить демона\n" +
				"  clipmgr --install   прописать автозапуск и хоткей Super+Ctrl+V, запустить демона\n" +
				"  clipmgr --uninstall убрать автозапуск и хоткей\n" +
				"  clipmgr --show      показать попап (вызывается хоткеем)\n" +
				"  clipmgr --version   версия\n" +
				"\n" +
				"Wayland (GNOME): попап по центру, вставка через /dev/uinput (Shift+Insert),\n" +
				"история — через XWayland-мост (mutter зеркалит буфер в X11 CLIPBOARD).\n" +
				"  * нужен доступ на запись к /dev/uinput (иначе udev-правило);\n" +
				"  * для истории нужен XWayland (обычно уже включён);\n" +
				"  * переключение раскладки настроить через GNOME Tweaks (не Settings),\n" +
				"    иначе модификаторы «съедаются» и хоткей/вставка ломаются на 2-й раскладке.")
			return
		}
	}
	runDaemon()
}
