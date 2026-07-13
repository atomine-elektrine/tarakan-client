package headless

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"tarakan-client/internal/agent"
	repoctx "tarakan-client/internal/context"
)

func TestRunReportsUnavailableAgent(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), &output, repoctx.Info{Name: "repo", Root: t.TempDir()}, agent.Provider{Name: "codex"}, "review")
	if err == nil {
		t.Fatal("expected unavailable agent error")
	}
	for _, eventType := range []string{"session.started", "session.error"} {
		if !strings.Contains(output.String(), eventType) {
			t.Fatalf("output does not contain %q: %s", eventType, output.String())
		}
	}
}
