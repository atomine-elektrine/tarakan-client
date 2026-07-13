package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Config is how the client finds the Tarakan host and authenticates.
// CLI flags and interactive /url /token override environment variables and
// the config saved by `tarakan login`.
type Config struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// LoadConfig builds config in this precedence order: explicit values,
// environment variables, values saved by `tarakan login`, then defaults.
func LoadConfig(url, token string) Config {
	saved, _ := LoadSavedConfig()
	return Config{
		BaseURL: firstNonEmpty(url, os.Getenv("TARAKAN_URL"), saved.BaseURL, DefaultBaseURL),
		Token:   firstNonEmpty(token, os.Getenv("TARAKAN_API_TOKEN"), saved.Token),
	}.normalized()
}

// SavedConfigPath returns the per-user file used by `tarakan login`.
func SavedConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tarakan", "config.json"), nil
}

// LoadSavedConfig reads the persisted login, if one exists.
func LoadSavedConfig() (Config, error) {
	path, err := SavedConfigPath()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, err
	}
	return config.normalized(), nil
}

// SaveConfig persists a login in a user-only file. The containing directory
// and file permissions are tightened even when they already exist.
func SaveConfig(config Config) (string, error) {
	config = config.normalized()
	path, err := SavedConfigPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// RemoveSavedConfig logs the client out. It is idempotent.
func RemoveSavedConfig() error {
	path, err := SavedConfigPath()
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// FromEnv is LoadConfig with no explicit overrides (env / defaults only).
func FromEnv() (*Client, error) {
	return LoadConfig("", "").Client()
}

// WithOverrides returns a copy with non-empty url/token applied.
func (c Config) WithOverrides(url, token string) Config {
	if strings.TrimSpace(url) != "" {
		c.BaseURL = url
	}
	if strings.TrimSpace(token) != "" {
		c.Token = token
	}
	return c.normalized()
}

// Client builds an HTTP client from this config.
func (c Config) Client() (*Client, error) {
	return New(c.BaseURL, c.Token, nil)
}

// MaskedToken is safe to show in UI (never the full secret).
func (c Config) MaskedToken() string {
	t := strings.TrimSpace(c.Token)
	if t == "" {
		return "(not set)"
	}
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + "…" + t[len(t)-4:]
}

// Summary is a one-line status for the interactive UI.
func (c Config) Summary() string {
	return "url " + c.BaseURL + "  token " + c.MaskedToken()
}

func (c Config) normalized() Config {
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	c.Token = strings.TrimSpace(c.Token)
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	return c
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
