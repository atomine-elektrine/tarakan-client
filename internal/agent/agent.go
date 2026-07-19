package agent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

var ErrUnavailable = errors.New("agent is unavailable")

// lookPath is indirected so provider detection can be exercised in tests
// without depending on what is installed on the host.
var lookPath = exec.LookPath

// Kind distinguishes an agentic CLI (which reads the repository itself) from
// an HTTP model endpoint (which needs the repository packed into the prompt).
const (
	KindCLI  = "cli"
	KindHTTP = "http"
)

type Provider struct {
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Command     string `json:"command,omitempty"`
	Description string `json:"description"`
	Path        string `json:"path,omitempty"`

	// HTTP providers only.
	BaseURL   string `json:"base_url,omitempty"`
	Model     string `json:"model,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
}

type Request struct {
	Prompt    string
	Directory string
	// Progress, if set, is called with status lines (and agent stderr when streaming).
	// Must be safe to call from a background goroutine.
	Progress func(string)
}

type Registry struct {
	providers []Provider
}

// Detect discovers every review backend available in this environment: the
// agentic CLIs on $PATH, plus the HTTP model endpoints (Ollama, OpenRouter)
// that are configured and reachable.
func Detect() Registry {
	known := []Provider{
		{Name: "claude", Kind: KindCLI, Command: "claude", Description: "Claude Code"},
		{Name: "codex", Kind: KindCLI, Command: "codex", Description: "OpenAI Codex"},
		{Name: "grok", Kind: KindCLI, Command: "grok", Description: "Grok Build"},
		{Name: "kimi", Kind: KindCLI, Command: "kimi", Description: "Kimi Code"},
	}

	available := make([]Provider, 0, len(known)+2)
	for _, provider := range known {
		if path, err := exec.LookPath(provider.Command); err == nil {
			provider.Path = path
			available = append(available, provider)
		}
	}

	available = append(available, detectHTTPProviders(os.Getenv)...)
	return Registry{providers: available}
}

func (r Registry) Providers() []Provider {
	return append([]Provider(nil), r.providers...)
}

func (r Registry) Find(name string) (Provider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, provider := range r.providers {
		if provider.Name == name {
			return provider, true
		}
	}
	// --agent kimi prefers the CLI; fall back to Moonshot HTTP when only the API key is set.
	if name == "kimi" {
		for _, provider := range r.providers {
			if provider.Name == "kimi-http" {
				return provider, true
			}
		}
	}
	return Provider{}, false
}

func (r Registry) Default() (Provider, bool) {
	if len(r.providers) == 0 {
		return Provider{}, false
	}
	return r.providers[0], true
}

// ModelIdentifier is the model string recorded on a submitted review. HTTP
// providers know their exact model; CLI agents report the tool name because
// the underlying model is the CLI's own concern.
func (p Provider) ModelIdentifier() string {
	if p.Kind == KindHTTP {
		return p.Model
	}
	return p.Name
}

// WithModel returns a copy of the provider using a caller-supplied model. It
// only affects HTTP providers; CLI agents choose their own model.
func (p Provider) WithModel(model string) Provider {
	if model == "" || p.Kind != KindHTTP {
		return p
	}
	p.Model = model
	return p
}

func Run(ctx context.Context, provider Provider, request Request) (string, error) {
	switch provider.Kind {
	case KindHTTP:
		return runHTTP(ctx, provider, request)
	default:
		return runCLI(ctx, provider, request)
	}
}

func runCLI(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Path == "" {
		return "", ErrUnavailable
	}

	// CLI agents that expose structured event streams get live tool activity
	// in Progress (same transcript UX for grok / claude / codex / kimi).
	switch provider.Name {
	case "grok":
		return runGrok(ctx, provider, request)
	case "claude":
		return runClaude(ctx, provider, request)
	case "codex":
		return runCodex(ctx, provider, request)
	case "kimi":
		return runKimi(ctx, provider, request)
	}

	args, err := arguments(provider.Name, securityPrompt(request.Prompt))
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = request.Directory
	command.Env = subprocessEnvironment(os.Environ())

	if request.Progress == nil {
		output, err := command.CombinedOutput()
		if err != nil {
			return string(output), fmt.Errorf("%s failed: %w", provider.Description, err)
		}
		return strings.TrimSpace(string(output)), nil
	}

	return runCLIStreaming(command, provider.Description, request.Progress)
}

// runCLIStreaming tees stdout+stderr into the returned buffer while forwarding
// non-empty lines to progress (prefixed) so the TUI can show agent activity.
func runCLIStreaming(command *exec.Cmd, description string, progress func(string)) (string, error) {
	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := command.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", description, err)
	}

	var (
		buf bytes.Buffer
		mu  sync.Mutex
		wg  sync.WaitGroup
	)
	scan := func(r io.Reader, isErr bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		// Agents can emit long JSON lines.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			mu.Lock()
			buf.WriteString(line)
			buf.WriteByte('\n')
			mu.Unlock()
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || progress == nil {
				continue
			}
			// Skip huge JSON blobs in the status line; keep short activity.
			if len(trimmed) > 200 || strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
				if isErr {
					progress(description + " … (working)")
				}
				continue
			}
			if isErr {
				progress(description + ": " + trimmed)
			} else {
				progress(description + ": " + trimmed)
			}
		}
	}
	wg.Add(2)
	go scan(stdout, false)
	go scan(stderr, true)
	wg.Wait()
	waitErr := command.Wait()
	output := strings.TrimSpace(buf.String())
	if waitErr != nil {
		return output, fmt.Errorf("%s failed: %w", description, waitErr)
	}
	return output, nil
}

// subprocessEnvironment prevents the agent process-and therefore untrusted
// repository instructions-from reading credentials for the Tarakan service.
// Provider CLIs keep their normal environment and their own authentication.
func subprocessEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "TARAKAN_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func arguments(provider, prompt string) ([]string, error) {
	switch provider {
	case "claude":
		return []string{"-p", prompt}, nil
	case "codex":
		return []string{"exec", prompt}, nil
	case "grok":
		return []string{"-p", prompt}, nil
	case "kimi":
		// Print mode: non-interactive, auto-approves tools for this invocation.
		return []string{
			"--print",
			"--prompt", prompt,
			"--yolo",
			"--final-message-only",
		}, nil
	default:
		return nil, fmt.Errorf("unknown agent provider %q", provider)
	}
}

// reviewInstruction is the read-only security-review directive shared by every
// backend. CLI agents receive it inline; HTTP models receive it as the system
// message.
const reviewInstruction = "You are contributing a read-only security review of the current repository. " +
	"Do not edit files, commit changes, access secrets, or interact with external systems. " +
	"Clearly separate verified findings from hypotheses and include file paths and evidence."

func securityPrompt(userPrompt string) string {
	return reviewInstruction + "\n\nUser request:\n" + userPrompt
}
