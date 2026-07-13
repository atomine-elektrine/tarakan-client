package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tarakan-client/internal/api"
)

func TestLoginSavesTokenForFutureRuns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TARAKAN_URL", "")
	t.Setenv("TARAKAN_API_TOKEN", "")
	var stdout, stderr bytes.Buffer

	code := run(
		[]string{"login", "--url", "https://tarakan.example", "--token", "persistent-secret"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	cfg := api.LoadConfig("", "")
	if cfg.BaseURL != "https://tarakan.example" || cfg.Token != "persistent-secret" {
		t.Fatalf("saved cfg = %#v", cfg)
	}
	if strings.Contains(stdout.String(), "persistent-secret") {
		t.Fatalf("login output exposed token: %q", stdout.String())
	}
}

func TestWebLoginSavesExchangedTokenAndLogoutRemovesIt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TARAKAN_API_TOKEN", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/client-auth/start":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("public login request sent Authorization header %q", got)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code":               "trkd_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ",
				"user_code":                 "ABCD-EFGH",
				"verification_uri_complete": serverURL(r) + "/client/authorize/ABCD-EFGH",
				"expires_in":                60,
				"interval":                  1,
			})
		case "/api/client-auth/exchange":
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("public login request sent Authorization header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "browser-issued-secret",
				"token_type": "Bearer",
				"expires_at": "2026-08-12T00:00:00Z",
				"scopes":     []string{"tasks:read"},
			})
		case "/api/client-auth/session":
			if r.Method != http.MethodDelete {
				t.Errorf("logout method = %s", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer browser-issued-secret" {
				t.Errorf("logout Authorization = %q", got)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("TARAKAN_URL", server.URL)
	var stdout, stderr bytes.Buffer

	if code := run([]string{"login", "--no-browser"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("login code = %d, stderr = %q", code, stderr.String())
	}
	if got := api.LoadConfig("", "").Token; got != "browser-issued-secret" {
		t.Fatalf("saved token = %q", got)
	}
	if !strings.Contains(stdout.String(), "ABCD-EFGH") || !strings.Contains(stdout.String(), "/client/authorize/") {
		t.Fatalf("login output = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "browser-issued-secret") {
		t.Fatalf("login output exposed token: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"logout"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("logout code = %d, stderr = %q", code, stderr.String())
	}
	if got := api.LoadConfig("", "").Token; got != "" {
		t.Fatalf("token after logout = %q", got)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
