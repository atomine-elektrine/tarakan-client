package app

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"tarakan-client/internal/agent"
	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
)

func TestInputAcceptsKeypressesAfterNew(t *testing.T) {
	m := New(repoctx.Info{Root: t.TempDir(), Name: "demo"}, agent.Registry{}, agent.Provider{})
	if !m.input.Focused() {
		t.Fatal("textarea should be focused after New so the UI can accept typing")
	}

	updated, _ := m.Update(tea.KeyPressMsg{Text: "h"})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg{Text: "i"})
	m = updated.(Model)

	if got := m.input.Value(); got != "hi" {
		t.Fatalf("typed value = %q, want %q", got, "hi")
	}
}

func TestGuidedTUIStartsWithLoginThenPickup(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	unauthenticated := NewSession(
		repoctx.Info{Root: t.TempDir(), Name: "demo"},
		agent.Registry{},
		agent.Provider{},
		SessionOpts{APIConfig: api.Config{BaseURL: "https://tarakan.lol"}},
	)
	if got := unauthenticated.input.Placeholder; got != "Next: /login" {
		t.Fatalf("unauthenticated placeholder = %q", got)
	}

	authenticated := NewSession(
		repoctx.Info{Root: t.TempDir(), Name: "demo"},
		agent.Registry{},
		agent.Provider{},
		SessionOpts{APIConfig: api.Config{BaseURL: "https://tarakan.lol", Token: "saved"}},
	)
	if got := authenticated.input.Placeholder; !strings.Contains(got, "/pickup") {
		t.Fatalf("authenticated placeholder = %q", got)
	}
}

func TestPlainTextDoesNotLaunchAgent(t *testing.T) {
	m := NewSession(
		repoctx.Info{Root: t.TempDir(), Name: "demo"},
		agent.Registry{},
		agent.Provider{Name: "codex"},
		SessionOpts{APIConfig: api.Config{BaseURL: "https://tarakan.lol", Token: "saved"}},
	)
	m.input.SetValue("scan this directory")

	next, cmd := m.submit()
	got := next.(Model)
	if cmd != nil || got.busy {
		t.Fatal("plain text should not start background agent work")
	}
	messages := got.transcript.Messages()
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "ordinary text does not run an agent") {
		t.Fatalf("last message = %#v", messages)
	}
}

func TestNewWithJobStoresStartJob(t *testing.T) {
	m := NewWithJob(repoctx.Info{Root: t.TempDir(), Name: "demo"}, agent.Registry{}, agent.Provider{Name: "grok"}, 6)
	if m.startJobID != 6 {
		t.Fatalf("startJobID = %d, want 6", m.startJobID)
	}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("Init should schedule start-job when startJobID is set")
	}
}

func TestNewSessionPickup(t *testing.T) {
	m := NewSession(
		repoctx.Info{Root: t.TempDir(), Name: "demo", GitHubOwner: "o", GitHubName: "n"},
		agent.Registry{},
		agent.Provider{Name: "grok"},
		SessionOpts{Pickup: true},
	)
	if !m.startPickup || m.startJobID != 0 {
		t.Fatalf("startPickup=%v startJobID=%d", m.startPickup, m.startJobID)
	}
}
