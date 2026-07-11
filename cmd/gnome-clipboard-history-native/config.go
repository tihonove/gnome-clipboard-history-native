//go:build linux

// config.go — user config file (currently just the hotkey combo). Read via viper so
// config.json / config.yaml / config.toml all work interchangeably (format is picked by
// extension). The daemon watches the config DIRECTORY (not a single file, via fsnotify)
// and re-applies the hotkey to GNOME gsettings on the fly — no restart, no relogin. mutter
// picks up a gsettings custom-keybinding change live.
//
// Watching the directory (rather than viper.WatchConfig, which only follows a file that
// already existed at startup) is what makes appear/delete robust: a config created later is
// caught, and a deleted config falls back to the default binding. Nothing here ever calls
// log.Fatal — a config problem must never take the daemon down.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// defaultHotkey — the production binding used when there's no config and no GCHN_HOTKEY.
const defaultHotkey = "<Super><Control>v"

// configDir is ~/.config/<name>/. hotkeyName() keeps a dev instance (GCHN_NAME) from
// colliding with the installed one.
func configDir() string {
	return filepath.Join(xdgConfigHome(), hotkeyName())
}

// newConfig returns a viper that looks for config.{json,yaml,yml,toml} (base name "config")
// in configDir. The format is inferred from whichever file exists.
func newConfig() *viper.Viper {
	v := viper.New()
	v.SetConfigName("config")
	v.AddConfigPath(configDir())
	v.SetDefault("hotkey", defaultHotkey)
	return v
}

// configHotkey reads the hotkey from the config file. Returns "" when there's no config or
// it can't be read/parsed, so callers fall back to the default. Never panics — a broken
// config must not crash the daemon or the --install CLI.
func configHotkey() (hk string) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("config: recovered while reading hotkey: %v", r)
			hk = ""
		}
	}()
	v := newConfig()
	if err := v.ReadInConfig(); err != nil {
		return "" // no config file (or unreadable) — caller uses the default
	}
	return v.GetString("hotkey")
}

// configFileExists reports whether any config.{json,yaml,yml,toml} is present.
func configFileExists() bool {
	return newConfig().ReadInConfig() == nil
}

// watchConfig makes the daemon react to config edits on the fly. It watches the config
// DIRECTORY, so creating, editing, deleting or format-swapping the file are all handled the
// same way: on any event we recompute the desired binding (hotkeyBinding(): env → config →
// default) and, if it changed, re-apply it to gsettings via installHotkey. Applying only
// shells out to gsettings (no GTK), so running from the fsnotify goroutine is safe — no
// glib.IdleAdd needed. Any setup error degrades gracefully (daemon keeps working, just
// without live reload).
func watchConfig() {
	if os.Getenv("GCHN_HOTKEY") != "" {
		return // env pins the hotkey; the config file is ignored entirely
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("config: can't create %s, live reload disabled: %v", dir, err)
		return
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config: fsnotify unavailable, live reload disabled: %v", err)
		return
	}
	if err := w.Add(dir); err != nil {
		log.Printf("config: can't watch %s, live reload disabled: %v", dir, err)
		w.Close()
		return
	}
	exe := resolveExe()
	go func() {
		last := hotkeyBinding()
		for {
			select {
			case _, ok := <-w.Events:
				if !ok {
					return
				}
				want := hotkeyBinding()
				if want == "" || want == last {
					continue
				}
				last = want
				log.Printf("config: hotkey → %s (re-applying to gsettings)", want)
				installHotkey(exe)
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("config: watch error: %v", err)
			}
		}
	}()
	log.Printf("config: watching %s for hotkey changes (live)", dir)
}

// writeDefaultConfig drops a starter config.yaml (with an explanatory comment) when the user
// has no config yet — so there's something to edit and for the daemon to watch. YAML is used
// for the comment; json/toml also work if the user replaces the file. Skipped for a dev
// instance (its hotkey is pinned via GCHN_HOTKEY and the config is ignored).
func writeDefaultConfig() {
	if isDevInstance() || configFileExists() {
		return
	}
	dir := configDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("config: can't create %s: %v", dir, err)
		return
	}
	path := filepath.Join(dir, "config.yaml")
	content := "# gnome-clipboard-history-native config\n" +
		"# hotkey — GNOME accelerator syntax; applied live on save (no restart).\n" +
		"# Examples: <Super><Control>v, <Super>v, <Control><Alt>h\n" +
		"# You may use config.json or config.toml instead — the format is picked by extension.\n" +
		"hotkey: \"" + hotkeyBinding() + "\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		log.Printf("config: can't write %s: %v", path, err)
		return
	}
	fmt.Println("config:", path)
}
