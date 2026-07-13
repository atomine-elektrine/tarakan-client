package agent

import (
	"strings"
	"testing"
)

func TestParseClaudeStreamToolUse(t *testing.T) {
	line := `{"type":"assistant","session_id":"s","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"sample.txt"}}]}}`
	ev := parseClaudeStreamLine(line)
	if !ev.ok || !strings.Contains(ev.activity, "Read sample.txt") {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestParseClaudeStreamResult(t *testing.T) {
	line := `{"type":"result","subtype":"success","result":"all good","is_error":false}`
	ev := parseClaudeStreamLine(line)
	if !ev.ok || ev.finalResult != "all good" {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestParseClaudeStreamSubagent(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","input":{"description":"find auth","subagent_type":"Explore"}}]}}`
	ev := parseClaudeStreamLine(line)
	if !ev.ok || !strings.Contains(ev.activity, "Subagent") {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestParseCodexCommandExecution(t *testing.T) {
	line := `{"type":"item.started","item":{"id":"item_0","type":"command_execution","command":"/usr/bin/bash -lc \"sed -n '1,200p' sample.txt\"","status":"in_progress"}}`
	ev := parseCodexStreamLine(line)
	if !ev.ok || !strings.Contains(ev.activity, "Shell:") || !strings.Contains(ev.activity, "sample.txt") {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestParseCodexAgentMessage(t *testing.T) {
	line := `{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"hello world"}}`
	ev := parseCodexStreamLine(line)
	if !ev.ok || ev.message != "hello world" {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestSimplifyCodexCommand(t *testing.T) {
	got := simplifyCodexCommand(`/usr/bin/bash -lc "ls -la"`)
	if got != "ls -la" {
		t.Fatalf("got %q", got)
	}
}

func TestFormatClaudeToolBash(t *testing.T) {
	got := formatClaudeTool("Bash", map[string]any{"command": "git status", "description": "check git"})
	if got != "Shell: check git" {
		t.Fatalf("got %q", got)
	}
}
