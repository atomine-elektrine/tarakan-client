package app

import (
	"testing"

	"tarakan-client/internal/agent"
	repoctx "tarakan-client/internal/context"
	"tarakan-client/internal/session"
)

func TestIsAgentStreamLine(t *testing.T) {
	if !isAgentStreamLine("Grok Build: thinking…") {
		t.Fatal("expected agent stream line")
	}
	if isAgentStreamLine("Cloning max/elektrine from http://localhost:4000…") {
		t.Fatal("pipeline step should not be agent stream")
	}
	if isAgentStreamLine("Claiming job #12…") {
		t.Fatal("claim step should not be agent stream")
	}
}

func TestHandleWorkEventAppendsSystemProgress(t *testing.T) {
	m := New(repoctx.Info{Root: t.TempDir(), Name: "demo"}, agent.Registry{}, agent.Provider{Name: "grok", Description: "Grok Build"})
	m.busy = true
	ch := make(chan workEvent)
	m.workEvents = ch

	line := "Fetching job #1…"
	next, cmd := m.handleWorkEvent(workEventMsg{event: workEvent{line: line}})
	m = next.(Model)
	if m.busyStatus != line {
		t.Fatalf("busyStatus = %q", m.busyStatus)
	}
	found := false
	for _, message := range m.transcript.Messages() {
		if message.Role == session.RoleSystem && message.Content == line {
			found = true
		}
	}
	if !found {
		t.Fatal("expected system transcript line")
	}
	if cmd == nil {
		t.Fatal("expected continue listen cmd")
	}

	next, _ = m.handleWorkEvent(workEventMsg{event: workEvent{finished: true, final: noticeMsg{body: "ok"}}})
	m = next.(Model)
	if m.busy {
		t.Fatal("expected idle after final")
	}
}
