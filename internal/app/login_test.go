package app

import (
	"testing"

	"tarakan-client/internal/agent"
	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
)

func TestLoginCommandStartsBrowserAuthorization(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewSession(repoctx.Info{}, agent.Registry{}, agent.Provider{}, SessionOpts{
		APIConfig: api.Config{BaseURL: "https://tarakan.lol"},
	})

	next, cmd := m.executeCommand(command{name: "login"})
	got := next.(Model)
	if !got.busy || got.busyStatus != "Starting browser login…" {
		t.Fatalf("login state: busy=%v status=%q", got.busy, got.busyStatus)
	}
	if cmd == nil {
		t.Fatal("/login should start the device authorization request")
	}
}

func TestSuccessfulLoginUpdatesTUIConfigAndPersistsToken(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := NewSession(repoctx.Info{}, agent.Registry{}, agent.Provider{}, SessionOpts{
		APIConfig: api.Config{BaseURL: "https://tarakan.lol"},
	})
	m.busy = true
	m.pendingLogin = &pendingLogin{config: m.apiConfig}

	next, _ := m.handleLoginPoll(loginPollMsg{
		credential: api.DeviceCredential{Token: "web-issued-token"},
	})
	got := next.(Model)
	if got.busy || got.apiConfig.Token != "web-issued-token" {
		t.Fatalf("login result: busy=%v config=%#v", got.busy, got.apiConfig)
	}
	if saved, err := api.LoadSavedConfig(); err != nil || saved.Token != "web-issued-token" {
		t.Fatalf("saved config = %#v, err = %v", saved, err)
	}
}
