package agent

import (
	"strings"
	"testing"
)

func TestArguments(t *testing.T) {
	tests := []struct {
		provider string
		first    string
	}{
		{provider: "claude", first: "-p"},
		{provider: "codex", first: "exec"},
	}

	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			args, err := arguments(test.provider, "prompt")
			if err != nil {
				t.Fatal(err)
			}
			if len(args) != 2 || args[0] != test.first || args[1] != "prompt" {
				t.Fatalf("arguments = %#v", args)
			}
		})
	}
	// Grok uses a dedicated streaming runner (session id + streaming-json).
	if args, err := arguments("grok", "prompt"); err != nil || len(args) < 2 || args[0] != "-p" {
		t.Fatalf("grok base args = %#v err=%v", args, err)
	}
}

func TestSecurityPromptEnforcesReadOnlyReview(t *testing.T) {
	prompt := securityPrompt("inspect auth")
	for _, expected := range []string{"read-only", "Do not edit files", "inspect auth"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt does not contain %q", expected)
		}
	}
}

func TestSubprocessEnvironmentRemovesTarakanSecrets(t *testing.T) {
	environment := subprocessEnvironment([]string{
		"PATH=/usr/bin",
		"TARAKAN_API_TOKEN=do-not-leak",
		"tarakan_url=https://tarakan.lol",
		"HOME=/home/test",
	})
	joined := strings.Join(environment, "\n")
	if strings.Contains(strings.ToUpper(joined), "TARAKAN_") {
		t.Fatalf("Tarakan variable leaked into subprocess environment: %s", joined)
	}
	for _, expected := range []string{"PATH=/usr/bin", "HOME=/home/test"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment does not contain %q: %s", expected, joined)
		}
	}
}
