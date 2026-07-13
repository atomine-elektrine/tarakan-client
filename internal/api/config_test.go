package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigPrefersExplicitOverEnv(t *testing.T) {
	isolateSavedConfig(t)
	t.Setenv("TARAKAN_URL", "https://env.example")
	t.Setenv("TARAKAN_API_TOKEN", "env-token")

	cfg := LoadConfig("https://cli.example", "cli-token")
	if cfg.BaseURL != "https://cli.example" || cfg.Token != "cli-token" {
		t.Fatalf("cfg = %#v", cfg)
	}

	cfg = LoadConfig("", "")
	if cfg.BaseURL != "https://env.example" || cfg.Token != "env-token" {
		t.Fatalf("env cfg = %#v", cfg)
	}
}

func TestLoadConfigDefaultsURL(t *testing.T) {
	isolateSavedConfig(t)
	t.Setenv("TARAKAN_URL", "")
	t.Setenv("TARAKAN_API_TOKEN", "t")
	cfg := LoadConfig("", "t")
	if cfg.BaseURL != "https://tarakan.lol" {
		t.Fatalf("BaseURL = %q", cfg.BaseURL)
	}
}

func TestConfigWithOverrides(t *testing.T) {
	isolateSavedConfig(t)
	cfg := LoadConfig("https://a.example", "one").WithOverrides("https://b.example", "")
	if cfg.BaseURL != "https://b.example" || cfg.Token != "one" {
		t.Fatalf("cfg = %#v", cfg)
	}
}

func TestMaskedToken(t *testing.T) {
	if got := (Config{}).MaskedToken(); got != "(not set)" {
		t.Fatalf("empty = %q", got)
	}
	if got := (Config{Token: "abcdefghijklmnop"}).MaskedToken(); got != "abcd…mnop" {
		t.Fatalf("masked = %q", got)
	}
}

func TestSavedConfigIsLoadedAndProtected(t *testing.T) {
	isolateSavedConfig(t)
	t.Setenv("TARAKAN_URL", "")
	t.Setenv("TARAKAN_API_TOKEN", "")

	path, err := SaveConfig(Config{BaseURL: "https://saved.example/", Token: "saved-token"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tarakan", "config.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("config dir mode = %v, err = %v", info.Mode().Perm(), err)
	}

	cfg := LoadConfig("", "")
	if cfg.BaseURL != "https://saved.example" || cfg.Token != "saved-token" {
		t.Fatalf("saved cfg = %#v", cfg)
	}

	t.Setenv("TARAKAN_URL", "https://env.example")
	t.Setenv("TARAKAN_API_TOKEN", "env-token")
	cfg = LoadConfig("https://explicit.example", "explicit-token")
	if cfg.BaseURL != "https://explicit.example" || cfg.Token != "explicit-token" {
		t.Fatalf("precedence cfg = %#v", cfg)
	}

	if err := RemoveSavedConfig(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("saved config still exists: %v", err)
	}
	if err := RemoveSavedConfig(); err != nil {
		t.Fatalf("second removal should be harmless: %v", err)
	}
}

func isolateSavedConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}
