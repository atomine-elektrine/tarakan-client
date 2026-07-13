package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envFrom(pairs map[string]string) getenv {
	return func(key string) string { return pairs[key] }
}

func TestDetectHTTPProvidersFromEnv(t *testing.T) {
	// Neither configured, and no ollama binary: nothing detected.
	original := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	defer func() { lookPath = original }()

	if got := detectHTTPProviders(envFrom(nil)); len(got) != 0 {
		t.Fatalf("expected no HTTP providers, got %#v", got)
	}

	providers := detectHTTPProviders(envFrom(map[string]string{
		"OLLAMA_MODEL":       "qwen2.5-coder",
		"OLLAMA_HOST":        "http://127.0.0.1:11434",
		"OPENROUTER_API_KEY": "sk-or-test",
		"OPENROUTER_MODEL":   "anthropic/claude-3.5-sonnet",
	}))

	if len(providers) != 2 {
		t.Fatalf("expected ollama + openrouter, got %#v", providers)
	}

	ollama := providers[0]
	if ollama.Name != "ollama" || ollama.Kind != KindHTTP {
		t.Fatalf("unexpected ollama provider: %#v", ollama)
	}
	if ollama.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Fatalf("ollama base URL = %q", ollama.BaseURL)
	}
	if ollama.Model != "qwen2.5-coder" || ollama.APIKeyEnv != "" {
		t.Fatalf("ollama config = %#v", ollama)
	}

	openrouter := providers[1]
	if openrouter.Name != "openrouter" || openrouter.APIKeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("unexpected openrouter provider: %#v", openrouter)
	}
	if openrouter.BaseURL != openRouterBaseURL || openrouter.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("openrouter config = %#v", openrouter)
	}
}

func TestDetectOllamaDefaultsWhenBinaryPresent(t *testing.T) {
	original := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/ollama", nil }
	defer func() { lookPath = original }()

	providers := detectHTTPProviders(envFrom(nil))
	if len(providers) != 1 || providers[0].Name != "ollama" {
		t.Fatalf("expected default ollama, got %#v", providers)
	}
	if providers[0].BaseURL != defaultOllamaHost+"/v1" || providers[0].Model != defaultOllamaModel {
		t.Fatalf("ollama defaults = %#v", providers[0])
	}
}

func TestRunHTTPOllamaNoAuth(t *testing.T) {
	var captured chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("ollama must not send auth, got %q", auth)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"  Found a SQL injection.  "}}]}`)
	}))
	defer server.Close()

	dir := writeRepo(t, map[string]string{"main.go": "package main // exec(userInput)"})

	provider := Provider{Name: "ollama", Kind: KindHTTP, Description: "Ollama", BaseURL: server.URL + "/v1", Model: "llama3.1"}
	out, err := runHTTP(context.Background(), provider, Request{Prompt: "review auth", Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out != "Found a SQL injection." {
		t.Fatalf("output = %q", out)
	}
	if captured.Model != "llama3.1" || len(captured.Messages) != 2 {
		t.Fatalf("request = %#v", captured)
	}
	if captured.Messages[0].Role != "system" || !strings.Contains(captured.Messages[0].Content, "read-only") {
		t.Fatalf("system message = %#v", captured.Messages[0])
	}
	if !strings.Contains(captured.Messages[1].Content, "main.go") ||
		!strings.Contains(captured.Messages[1].Content, "review auth") {
		t.Fatalf("user message missing repo context or prompt: %q", captured.Messages[1].Content)
	}
}

func TestRunHTTPOpenRouterRequiresKey(t *testing.T) {
	dir := writeRepo(t, map[string]string{"a.py": "print(1)"})
	provider := Provider{Name: "openrouter", Kind: KindHTTP, Description: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", Model: "x", APIKeyEnv: "OPENROUTER_API_KEY"}

	os.Unsetenv("OPENROUTER_API_KEY")
	if _, err := runHTTP(context.Background(), provider, Request{Prompt: "p", Directory: dir}); err == nil {
		t.Fatal("expected error when API key is unset")
	}
}

func TestRunHTTPOpenRouterSendsBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-or-secret" {
			t.Errorf("authorization = %q", got)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	dir := writeRepo(t, map[string]string{"a.py": "print(1)"})
	t.Setenv("OPENROUTER_API_KEY", "sk-or-secret")

	provider := Provider{Name: "openrouter", Kind: KindHTTP, Description: "OpenRouter", BaseURL: server.URL, Model: "x", APIKeyEnv: "OPENROUTER_API_KEY"}
	if _, err := runHTTP(context.Background(), provider, Request{Prompt: "p", Directory: dir}); err != nil {
		t.Fatal(err)
	}
}

func TestRunHTTPSurfacesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"message":"model not found"}}`)
	}))
	defer server.Close()

	dir := writeRepo(t, map[string]string{"a.py": "print(1)"})
	provider := Provider{Name: "ollama", Kind: KindHTTP, Description: "Ollama", BaseURL: server.URL, Model: "missing"}
	_, err := runHTTP(context.Background(), provider, Request{Prompt: "p", Directory: dir})
	if err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("expected API error surfaced, got %v", err)
	}
}

func TestGatherRepositoryContextBoundsAndFilters(t *testing.T) {
	dir := writeRepo(t, map[string]string{
		"keep.go":                "package main",
		"nested/util.js":         "export const x = 1",
		"node_modules/dep/i.js":  "should be skipped",
		"bin.dat":                "text\x00binary",
		filepath.Join("big.txt"): strings.Repeat("A", maxFileBytes+1),
	})

	bundle, err := gatherRepositoryContext(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"=== keep.go ===", "=== nested/util.js ==="} {
		if !strings.Contains(bundle, want) {
			t.Errorf("bundle missing %q", want)
		}
	}
	for _, unwanted := range []string{"node_modules", "bin.dat", "big.txt"} {
		if strings.Contains(bundle, unwanted) {
			t.Errorf("bundle should have skipped %q", unwanted)
		}
	}
}

func TestModelIdentifierAndWithModel(t *testing.T) {
	cli := Provider{Name: "claude", Kind: KindCLI}
	if cli.ModelIdentifier() != "claude" {
		t.Fatalf("cli identifier = %q", cli.ModelIdentifier())
	}
	if got := cli.WithModel("x"); got.Model != "" {
		t.Fatalf("WithModel must not touch CLI providers: %#v", got)
	}

	http := Provider{Name: "ollama", Kind: KindHTTP, Model: "llama3.1"}
	if http.ModelIdentifier() != "llama3.1" {
		t.Fatalf("http identifier = %q", http.ModelIdentifier())
	}
	if got := http.WithModel("qwen"); got.Model != "qwen" {
		t.Fatalf("WithModel = %#v", got)
	}
}

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
