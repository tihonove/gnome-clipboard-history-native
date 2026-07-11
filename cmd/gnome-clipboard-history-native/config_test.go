//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setupCfg points the config lookup at a temp dir via XDG_CONFIG_HOME + GCHN_NAME and
// returns that dir. GCHN_HOTKEY is cleared so the config path is exercised.
func setupCfg(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("GCHN_NAME", "gchn-unittest")
	t.Setenv("GCHN_HOTKEY", "")
	dir := filepath.Join(root, "gchn-unittest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConfigHotkey_Formats(t *testing.T) {
	cases := []struct{ name, file, body, want string }{
		{"yaml", "config.yaml", "hotkey: \"<Super><Control>b\"\n", "<Super><Control>b"},
		{"json", "config.json", "{ \"hotkey\": \"<Super><Control>m\" }\n", "<Super><Control>m"},
		{"toml", "config.toml", "hotkey = \"<Super><Control>j\"\n", "<Super><Control>j"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := setupCfg(t)
			write(t, dir, c.file, c.body)
			if got := configHotkey(); got != c.want {
				t.Fatalf("configHotkey() = %q, want %q", got, c.want)
			}
			if got := hotkeyBinding(); got != c.want {
				t.Fatalf("hotkeyBinding() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConfigHotkey_MissingFile(t *testing.T) {
	setupCfg(t) // dir exists but no config.* file
	if got := configHotkey(); got != "" {
		t.Fatalf("configHotkey() with no file = %q, want \"\"", got)
	}
	if got := hotkeyBinding(); got != defaultHotkey {
		t.Fatalf("hotkeyBinding() with no file = %q, want default %q", got, defaultHotkey)
	}
}

func TestConfigHotkey_Broken(t *testing.T) {
	dir := setupCfg(t)
	write(t, dir, "config.yaml", "hotkey: \"<Super : broken\n  : : :\n")
	// Must not panic; falls back cleanly.
	if got := configHotkey(); got != "" {
		t.Fatalf("configHotkey() on broken file = %q, want \"\"", got)
	}
	if got := hotkeyBinding(); got != defaultHotkey {
		t.Fatalf("hotkeyBinding() on broken file = %q, want default %q", got, defaultHotkey)
	}
}

func TestConfigHotkey_EmptyValue(t *testing.T) {
	dir := setupCfg(t)
	write(t, dir, "config.yaml", "hotkey: \"\"\n")
	if got := hotkeyBinding(); got != defaultHotkey {
		t.Fatalf("hotkeyBinding() on empty hotkey = %q, want default %q", got, defaultHotkey)
	}
}

func TestHotkeyBinding_EnvOverridesConfig(t *testing.T) {
	dir := setupCfg(t)
	write(t, dir, "config.yaml", "hotkey: \"<Super><Control>b\"\n")
	t.Setenv("GCHN_HOTKEY", "<Super><Control>z")
	if got := hotkeyBinding(); got != "<Super><Control>z" {
		t.Fatalf("hotkeyBinding() with env set = %q, want env value", got)
	}
}

func TestConfigFileExists(t *testing.T) {
	dir := setupCfg(t)
	if configFileExists() {
		t.Fatal("configFileExists() = true with no file")
	}
	write(t, dir, "config.toml", "hotkey = \"<Super>v\"\n")
	if !configFileExists() {
		t.Fatal("configFileExists() = false with config.toml present")
	}
}
