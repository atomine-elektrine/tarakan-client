package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultOllamaHost      = "http://localhost:11434"
	defaultOllamaModel     = "llama3.1"
	openRouterBaseURL      = "https://openrouter.ai/api/v1"
	defaultOpenRouterModel = "openai/gpt-4o-mini"

	// A single repository bundle is bounded so it stays within a model's
	// context window and a review call stays affordable.
	maxFileBytes  = 64 << 10
	maxTotalBytes = 384 << 10
)

// getenv abstracts os.Getenv so detection is testable.
type getenv func(string) string

// detectHTTPProviders returns the configured OpenAI-compatible model endpoints.
// Ollama appears when its daemon is plausibly present (the CLI is installed or
// a host is configured); OpenRouter appears when an API key is set.
func detectHTTPProviders(env getenv) []Provider {
	var providers []Provider

	if ollamaConfigured(env) {
		providers = append(providers, Provider{
			Name:        "ollama",
			Kind:        KindHTTP,
			Description: "Ollama (local)",
			BaseURL:     strings.TrimRight(firstNonEmpty(env("OLLAMA_HOST"), defaultOllamaHost), "/") + "/v1",
			Model:       firstNonEmpty(env("OLLAMA_MODEL"), defaultOllamaModel),
		})
	}

	if env("OPENROUTER_API_KEY") != "" {
		providers = append(providers, Provider{
			Name:        "openrouter",
			Kind:        KindHTTP,
			Description: "OpenRouter",
			BaseURL:     openRouterBaseURL,
			Model:       firstNonEmpty(env("OPENROUTER_MODEL"), defaultOpenRouterModel),
			APIKeyEnv:   "OPENROUTER_API_KEY",
		})
	}

	return providers
}

func ollamaConfigured(env getenv) bool {
	if env("OLLAMA_HOST") != "" || env("OLLAMA_MODEL") != "" {
		return true
	}
	_, err := lookPath("ollama")
	return err == nil
}

// chatMessage, chatRequest, and chatResponse cover the subset of the OpenAI
// chat-completions schema that Ollama and OpenRouter both implement.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func runHTTP(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Model == "" {
		return "", fmt.Errorf("%s: no model configured", provider.Description)
	}

	apiKey := ""
	if provider.APIKeyEnv != "" {
		if apiKey = os.Getenv(provider.APIKeyEnv); apiKey == "" {
			return "", fmt.Errorf("%s: set %s", provider.Description, provider.APIKeyEnv)
		}
	}

	if request.Progress != nil {
		// HTTP backends pack the tree into the prompt (no per-tool stream).
		request.Progress("→ Packing repository context for " + provider.Description)
	}
	bundle, err := gatherRepositoryContext(request.Directory)
	if err != nil {
		return "", fmt.Errorf("read repository for review: %w", err)
	}

	if request.Progress != nil {
		request.Progress("→ Calling " + provider.Description + " (" + provider.Model + ")…")
		request.Progress("… waiting for model (HTTP providers do not stream file reads)")
	}
	payload := chatRequest{
		Model:  provider.Model,
		Stream: false,
		Messages: []chatMessage{
			{Role: "system", Content: reviewInstruction},
			{Role: "user", Content: bundle + "\n\nUser request:\n" + request.Prompt},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	endpoint := strings.TrimRight(provider.BaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+apiKey)
		// OpenRouter attributes traffic by these headers; harmless elsewhere.
		httpRequest.Header.Set("X-Title", "Tarakan")
		httpRequest.Header.Set("HTTP-Referer", "https://tarakan.lol")
	}

	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return "", fmt.Errorf("%s request failed: %w", provider.Description, err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return "", err
	}

	var decoded chatResponse
	_ = json.Unmarshal(responseBody, &decoded)

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("%s returned %s: %s", provider.Description, response.Status, errorDetail(decoded, responseBody))
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return "", fmt.Errorf("%s error: %s", provider.Description, decoded.Error.Message)
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("%s returned no content", provider.Description)
	}

	return strings.TrimSpace(decoded.Choices[0].Message.Content), nil
}

func errorDetail(decoded chatResponse, raw []byte) string {
	if decoded.Error != nil && decoded.Error.Message != "" {
		return decoded.Error.Message
	}
	detail := strings.TrimSpace(string(raw))
	if len(detail) > 500 {
		detail = detail[:500] + "…"
	}
	return detail
}

// gatherRepositoryContext packs the repository's source files into one text
// bundle for a model that cannot read the disk itself. Binary, oversized, and
// vendored files are skipped, and the total is bounded; when the budget runs
// out the bundle is truncated with a note rather than failing.
func gatherRepositoryContext(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("no repository directory")
	}

	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			if skipDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(files)

	var builder strings.Builder
	total := 0
	included := 0
	truncated := false

	for _, path := range files {
		if total >= maxTotalBytes {
			truncated = true
			break
		}

		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 || info.Size() > maxFileBytes {
			continue
		}

		content, err := os.ReadFile(path)
		if err != nil || isBinary(content) {
			continue
		}

		if total+len(content) > maxTotalBytes {
			truncated = true
			break
		}

		relative, err := filepath.Rel(root, path)
		if err != nil {
			relative = path
		}

		fmt.Fprintf(&builder, "=== %s ===\n%s\n\n", filepath.ToSlash(relative), content)
		total += len(content)
		included++
	}

	if included == 0 {
		return "Repository source (no readable text files found):", nil
	}

	header := fmt.Sprintf("Repository source (%d files", included)
	if truncated {
		header += ", truncated to fit the review budget"
	}
	header += "):\n\n"

	return header + strings.TrimRight(builder.String(), "\n"), nil
}

func skipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build", "target",
		".venv", "venv", "__pycache__", ".next", ".turbo", ".cache",
		"coverage", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func isBinary(content []byte) bool {
	limit := len(content)
	if limit > 8000 {
		limit = 8000
	}
	return bytes.IndexByte(content[:limit], 0) >= 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
