package app

import "testing"

func TestParseCommand(t *testing.T) {
	parsed, ok := parseCommand("/agent codex")
	if !ok {
		t.Fatal("command was not recognized")
	}
	if parsed.name != "agent" || len(parsed.args) != 1 || parsed.args[0] != "codex" {
		t.Fatalf("unexpected command: %#v", parsed)
	}
}

func TestParseCommandRejectsPrompt(t *testing.T) {
	if _, ok := parseCommand("review the auth flow"); ok {
		t.Fatal("ordinary prompt was parsed as a command")
	}
}

func TestParseReportCommand(t *testing.T) {
	parsed, ok := parseCommand("/report 6")
	if !ok {
		t.Fatal("expected /report command")
	}
	if parsed.name != "report" || len(parsed.args) != 1 || parsed.args[0] != "6" {
		t.Fatalf("unexpected command: %#v", parsed)
	}
	parsed, ok = parseCommand("/submit-report")
	if !ok || parsed.name != "submit-report" {
		t.Fatalf("unexpected submit-report: %#v ok=%v", parsed, ok)
	}
}
