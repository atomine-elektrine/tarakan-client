package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// runKimi runs Kimi Code headless in print mode.
//
// With --final-message-only the CLI emits only the assistant answer (best for
// Review Format JSON). When stream-json is available and Progress is set, we
// use streaming so the TUI can show tool activity.
func runKimi(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Path == "" {
		return "", ErrUnavailable
	}

	prompt := securityPrompt(request.Prompt)
	cwd := request.Directory
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Prefer stream-json when the UI wants progress; otherwise quiet text.
	if request.Progress != nil {
		return runKimiStreaming(ctx, provider, prompt, cwd, request.Progress)
	}
	return runKimiFinal(ctx, provider, prompt, cwd)
}

func runKimiFinal(ctx context.Context, provider Provider, prompt, cwd string) (string, error) {
	args := []string{
		"--print",
		"--prompt", prompt,
		"--yolo",
		"--final-message-only",
		"--work-dir", cwd,
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = cwd
	command.Env = subprocessEnvironment(os.Environ())

	output, err := command.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed: %w\n%s", provider.Description, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func runKimiStreaming(ctx context.Context, provider Provider, prompt, cwd string, progress func(string)) (string, error) {
	args := []string{
		"--print",
		"--prompt", prompt,
		"--yolo",
		"--output-format", "stream-json",
		"--work-dir", cwd,
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = cwd
	command.Env = subprocessEnvironment(os.Environ())

	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderrBuf bytes.Buffer
	command.Stderr = &stderrBuf

	if err := command.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", provider.Description, err)
	}

	report := func(line string) {
		if progress == nil {
			return
		}
		if line = strings.TrimSpace(line); line != "" {
			progress(line)
		}
	}

	var (
		textBuf strings.Builder
		mu      sync.Mutex
		final   string
	)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		ev := parseKimiStreamLine(line)
		if !ev.ok {
			continue
		}
		if ev.activity != "" {
			report(ev.activity)
		}
		if ev.text != "" {
			mu.Lock()
			textBuf.WriteString(ev.text)
			mu.Unlock()
		}
		if ev.final != "" {
			final = ev.final
		}
	}

	waitErr := command.Wait()
	out := strings.TrimSpace(final)
	if out == "" {
		mu.Lock()
		out = strings.TrimSpace(textBuf.String())
		mu.Unlock()
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderrBuf.String())
		if detail != "" {
			return out, fmt.Errorf("%s failed: %w\n%s", provider.Description, waitErr, detail)
		}
		return out, fmt.Errorf("%s failed: %w", provider.Description, waitErr)
	}
	return out, nil
}

type kimiStreamEvent struct {
	ok       bool
	activity string
	text     string
	final    string
}

// parseKimiStreamLine maps Kimi print stream-json lines into progress + text.
// The wire format is evolving; we accept common role/type shapes and ignore the rest.
func parseKimiStreamLine(line string) kimiStreamEvent {
	line = strings.TrimSpace(line)
	if line == "" || !strings.HasPrefix(line, "{") {
		return kimiStreamEvent{}
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return kimiStreamEvent{}
	}

	// OpenAI-ish chat chunk: {"choices":[{"delta":{"content":"..."}}]}
	if choices, ok := raw["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if delta, ok := choice["delta"].(map[string]any); ok {
				if content, ok := delta["content"].(string); ok && content != "" {
					return kimiStreamEvent{ok: true, text: content}
				}
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				if content, ok := msg["content"].(string); ok && content != "" {
					return kimiStreamEvent{ok: true, text: content, final: content}
				}
			}
		}
	}

	// role/content messages
	if role, _ := raw["role"].(string); role == "assistant" {
		if content, ok := raw["content"].(string); ok && content != "" {
			return kimiStreamEvent{ok: true, text: content, final: content}
		}
	}

	// type-tagged events (tool use, assistant text)
	typ, _ := raw["type"].(string)
	switch typ {
	case "assistant", "message", "agent_message", "text":
		if content := kimiFirstString(raw, "text", "content", "message"); content != "" {
			return kimiStreamEvent{ok: true, text: content, final: content}
		}
	case "tool_use", "tool_call", "tool":
		name := kimiFirstString(raw, "name", "tool", "tool_name")
		if name == "" {
			if input, ok := raw["input"].(map[string]any); ok {
				name = kimiFirstString(input, "name", "command", "path")
			}
		}
		if name != "" {
			return kimiStreamEvent{ok: true, activity: "Kimi: " + name}
		}
		return kimiStreamEvent{ok: true, activity: "Kimi … (tool)"}
	case "result", "final", "final_message":
		if content := kimiFirstString(raw, "result", "text", "content", "message"); content != "" {
			return kimiStreamEvent{ok: true, text: content, final: content}
		}
	}

	// Tool name at top level without type
	if name := kimiFirstString(raw, "tool_name", "name"); name != "" && raw["type"] == nil {
		if _, isMsg := raw["role"]; !isMsg {
			return kimiStreamEvent{ok: true, activity: "Kimi: " + name}
		}
	}

	return kimiStreamEvent{}
}

func kimiFirstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}
