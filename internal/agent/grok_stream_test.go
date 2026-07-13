package agent

import (
	"strings"
	"testing"
)

func TestFormatGrokUpdateLineToolCall(t *testing.T) {
	raw := `{"timestamp":1,"method":"session/update","params":{"sessionId":"x","update":{"sessionUpdate":"tool_call_update","toolCallId":"c1","kind":"read","title":"Read sample.txt","locations":[{"path":"sample.txt"}],"status":"in_progress","rawInput":{"target_file":"sample.txt"},"_meta":{"x.ai/tool":{"name":"read_file"}}}}}`
	msg, ok := formatGrokUpdateLine(raw)
	if !ok {
		t.Fatal("expected tool line")
	}
	if !strings.Contains(msg, "Read") || !strings.Contains(msg, "sample.txt") {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.HasPrefix(msg, "→ ") {
		t.Fatalf("expected arrow prefix, got %q", msg)
	}
}

func TestFormatGrokUpdateLineGrepPatternTitle(t *testing.T) {
	// Grok often sets title to the raw pattern; we should still show "Grep …".
	raw := `{"params":{"update":{"sessionUpdate":"tool_call_update","title":"password|secret|api_key","rawInput":{"variant":"Grep","pattern":"password|secret|api_key","glob":"*.ex"},"_meta":{"x.ai/tool":{"name":"grep"}}}}}`
	msg, ok := formatGrokUpdateLine(raw)
	if !ok || !strings.Contains(msg, "Grep") || strings.HasPrefix(msg, "→ password") {
		t.Fatalf("msg=%q ok=%v", msg, ok)
	}
}

func TestFormatGrokUpdateLineSkipsBareToolName(t *testing.T) {
	raw := `{"params":{"update":{"sessionUpdate":"tool_call","title":"grep"}}}`
	if _, ok := formatGrokUpdateLine(raw); ok {
		t.Fatal("bare grep title without input should be skipped")
	}
}

func TestFormatGrokUpdateLineCompleted(t *testing.T) {
	raw := `{"params":{"update":{"sessionUpdate":"tool_call_update","title":"Execute ls -la","status":"completed"}}}`
	msg, ok := formatGrokUpdateLine(raw)
	if !ok || !strings.HasPrefix(msg, "✓ ") {
		t.Fatalf("msg=%q ok=%v", msg, ok)
	}
}

func TestFormatGrokToolFromInputSubagent(t *testing.T) {
	got := formatGrokToolFromInput("spawn_subagent", map[string]any{
		"subagent_type": "explore",
		"description":   "find auth handlers",
	})
	if !strings.Contains(got, "Subagent") || !strings.Contains(got, "explore") {
		t.Fatalf("got %q", got)
	}
}

func TestEncodeGrokSessionDir(t *testing.T) {
	got := encodeGrokSessionDir("/tmp/foo")
	if got != "%2Ftmp%2Ffoo" && !strings.HasSuffix(got, "%2Ftmp%2Ffoo") {
		// Abs path may prefix differently; require slash encoding.
		if !strings.Contains(got, "%2F") {
			t.Fatalf("got %q", got)
		}
	}
}

func TestNewSessionUUIDShape(t *testing.T) {
	id := newSessionUUID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("uuid = %q", id)
	}
}
