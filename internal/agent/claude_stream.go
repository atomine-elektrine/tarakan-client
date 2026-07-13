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

// runClaude streams Claude Code stream-json events so the TUI can show tool
// use (Read, Bash, Task/subagents) while the run is in progress.
func runClaude(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Path == "" {
		return "", ErrUnavailable
	}
	args := []string{
		"-p", securityPrompt(request.Prompt),
		"--output-format", "stream-json",
		"--verbose",
		// Isolated snapshot; unattended tool use for the review run.
		"--dangerously-skip-permissions",
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = request.Directory
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

	progress := request.Progress
	report := func(line string) {
		if progress == nil {
			return
		}
		if line = strings.TrimSpace(line); line != "" {
			progress(line)
		}
	}
	var (
		lastFooter string
		footerMu   sync.Mutex
	)
	reportFooter := func(line string) {
		if progress == nil {
			return
		}
		footerMu.Lock()
		defer footerMu.Unlock()
		if line == lastFooter {
			return
		}
		lastFooter = line
		progress(line)
	}

	var (
		finalResult string
		textBuf     strings.Builder
		seen        = map[string]struct{}{}
	)
	emit := func(line string) {
		for _, part := range strings.Split(line, "\n") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			key := normalizeActivityKey(part)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			report(part)
		}
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		ev := parseClaudeStreamLine(scanner.Text())
		if !ev.ok {
			continue
		}
		if ev.activity != "" {
			emit(ev.activity)
		}
		if ev.footer != "" {
			reportFooter(ev.footer)
		}
		if ev.assistantText != "" {
			textBuf.WriteString(ev.assistantText)
		}
		if ev.finalResult != "" {
			finalResult = ev.finalResult
		}
	}
	_ = scanner.Err()

	waitErr := command.Wait()
	output := strings.TrimSpace(finalResult)
	if output == "" {
		output = strings.TrimSpace(textBuf.String())
	}
	if waitErr != nil {
		errText := strings.TrimSpace(stderrBuf.String())
		if output == "" && errText != "" {
			output = errText
		}
		return output, fmt.Errorf("%s failed: %w", provider.Description, waitErr)
	}
	return output, nil
}

type claudeStreamEvent struct {
	ok            bool
	activity      string
	footer        string
	assistantText string
	finalResult   string
}

func parseClaudeStreamLine(line string) claudeStreamEvent {
	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return claudeStreamEvent{}
	}
	typ, _ := row["type"].(string)
	switch typ {
	case "system":
		if sub, _ := row["subtype"].(string); sub == "init" {
			return claudeStreamEvent{ok: true, activity: "→ Claude session started"}
		}
	case "assistant":
		msg, _ := row["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		var acts []string
		var texts []string
		footer := ""
		for _, block := range content {
			b, _ := block.(map[string]any)
			switch b["type"] {
			case "tool_use":
				name, _ := b["name"].(string)
				input, _ := b["input"].(map[string]any)
				if label := formatClaudeTool(name, input); label != "" {
					acts = append(acts, "→ "+label)
				}
			case "text":
				if t, _ := b["text"].(string); strings.TrimSpace(t) != "" {
					texts = append(texts, t)
					footer = "… writing response"
				}
			case "thinking":
				footer = "… thinking"
			}
		}
		return claudeStreamEvent{
			ok:            true,
			activity:      strings.Join(acts, "\n"),
			footer:        footer,
			assistantText: strings.Join(texts, ""),
		}
	case "user":
		msg, _ := row["message"].(map[string]any)
		content, _ := msg["content"].([]any)
		for _, block := range content {
			b, _ := block.(map[string]any)
			if b["type"] == "tool_result" {
				return claudeStreamEvent{ok: true, footer: "… tool finished"}
			}
		}
	case "result":
		ev := claudeStreamEvent{ok: true}
		if r, _ := row["result"].(string); strings.TrimSpace(r) != "" {
			ev.finalResult = r
		}
		if isErr, _ := row["is_error"].(bool); isErr {
			if e, _ := row["error"].(string); e != "" {
				ev.activity = "Claude error: " + e
			}
		}
		return ev
	case "stream_event":
		return claudeStreamEvent{ok: true, footer: "… streaming"}
	}
	return claudeStreamEvent{}
}

func formatClaudeTool(name string, input map[string]any) string {
	name = strings.TrimSpace(name)
	if input == nil {
		input = map[string]any{}
	}
	switch name {
	case "Read", "read_file":
		path := firstString(input, "file_path", "path", "target_file")
		if path != "" {
			return "Read " + path
		}
		return "Read file"
	case "Bash", "bash", "Shell":
		cmd := firstString(input, "command")
		desc := firstString(input, "description")
		if desc != "" {
			return "Shell: " + desc
		}
		if cmd != "" {
			return "Shell: " + truncateRunes(cmd, 80)
		}
		return "Shell"
	case "Grep", "grep":
		pat := firstString(input, "pattern")
		path := firstString(input, "path")
		if pat != "" && path != "" {
			return "Grep " + quoteShort(pat) + " in " + path
		}
		if pat != "" {
			return "Grep " + quoteShort(pat)
		}
		return "Grep"
	case "Glob", "glob":
		pat := firstString(input, "pattern", "glob_pattern")
		if pat != "" {
			return "Glob " + pat
		}
		return "Glob"
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		path := firstString(input, "file_path", "path")
		if path != "" {
			return "Edit " + path
		}
		return "Edit file"
	case "Task", "Agent", "TaskCreate":
		desc := firstString(input, "description", "prompt")
		sub := firstString(input, "subagent_type", "agent")
		if sub != "" && desc != "" {
			return "Subagent " + sub + ": " + truncateRunes(desc, 60)
		}
		if sub != "" {
			return "Subagent " + sub
		}
		if desc != "" {
			return "Subagent: " + truncateRunes(desc, 60)
		}
		return "Subagent"
	case "WebSearch", "WebFetch":
		q := firstString(input, "query", "url")
		if q != "" {
			return name + ": " + truncateRunes(q, 60)
		}
		return name
	case "LS", "list_dir":
		path := firstString(input, "path", "target_directory")
		if path != "" {
			return "List " + path
		}
		return "List directory"
	default:
		if name == "" {
			return ""
		}
		for _, k := range []string{"file_path", "path", "command", "query", "description", "pattern"} {
			if v := firstString(input, k); v != "" {
				return name + " " + truncateRunes(v, 60)
			}
		}
		return name
	}
}
