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

// runCodex streams Codex exec --json events (command executions, messages)
// into Progress for live TUI visibility.
func runCodex(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Path == "" {
		return "", ErrUnavailable
	}
	args := []string{
		"exec",
		"--json",
		"--skip-git-repo-check",
		// Read-only sandbox matches Tarakan's isolated review snapshot.
		"--sandbox", "read-only",
		securityPrompt(request.Prompt),
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = request.Directory
	command.Env = subprocessEnvironment(os.Environ())
	// Codex may try to read stdin when it thinks input is piped.
	command.Stdin = bytes.NewReader(nil)

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
		lastMessage string
		seen        = map[string]struct{}{}
	)
	emit := func(line string) {
		key := normalizeActivityKey(line)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		report(line)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		ev := parseCodexStreamLine(scanner.Text())
		if !ev.ok {
			continue
		}
		if ev.activity != "" {
			emit(ev.activity)
		}
		if ev.footer != "" {
			reportFooter(ev.footer)
		}
		if ev.message != "" {
			lastMessage = ev.message
		}
	}
	_ = scanner.Err()

	waitErr := command.Wait()
	output := strings.TrimSpace(lastMessage)
	if waitErr != nil {
		errText := strings.TrimSpace(stderrBuf.String())
		if output == "" && errText != "" {
			output = errText
		}
		return output, fmt.Errorf("%s failed: %w", provider.Description, waitErr)
	}
	return output, nil
}

type codexStreamEvent struct {
	ok       bool
	activity string
	footer   string
	message  string
}

func parseCodexStreamLine(line string) codexStreamEvent {
	var row map[string]any
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		return codexStreamEvent{}
	}
	typ, _ := row["type"].(string)
	switch typ {
	case "thread.started":
		return codexStreamEvent{ok: true, activity: "→ Codex session started"}
	case "turn.started":
		return codexStreamEvent{ok: true, footer: "… turn started"}
	case "turn.completed":
		return codexStreamEvent{ok: true, footer: "… turn completed"}
	case "item.started", "item.completed":
		item, _ := row["item"].(map[string]any)
		if item == nil {
			return codexStreamEvent{}
		}
		return formatCodexItem(typ == "item.completed", item)
	case "error":
		if msg, _ := row["message"].(string); msg != "" {
			return codexStreamEvent{ok: true, activity: "Codex error: " + msg}
		}
	}
	return codexStreamEvent{}
}

func formatCodexItem(completed bool, item map[string]any) codexStreamEvent {
	itemType, _ := item["type"].(string)
	prefix := "→ "
	if completed {
		prefix = "✓ "
	}
	switch itemType {
	case "command_execution":
		cmd := firstString(item, "command")
		// Codex often wraps: /usr/bin/bash -lc "..."
		cmd = simplifyCodexCommand(cmd)
		status, _ := item["status"].(string)
		if status == "failed" || status == "error" {
			prefix = "✗ "
		}
		if cmd != "" {
			return codexStreamEvent{ok: true, activity: prefix + "Shell: " + truncateRunes(cmd, 100)}
		}
		return codexStreamEvent{ok: true, activity: prefix + "Shell"}
	case "file_change", "file_edit":
		path := firstString(item, "path", "file", "filename")
		if path != "" {
			return codexStreamEvent{ok: true, activity: prefix + "Edit " + path}
		}
		return codexStreamEvent{ok: true, activity: prefix + "Edit file"}
	case "agent_message", "message":
		text := firstString(item, "text", "content")
		if text != "" {
			return codexStreamEvent{ok: true, footer: "… writing response", message: text}
		}
		return codexStreamEvent{ok: true, footer: "… writing response"}
	case "reasoning", "thought":
		return codexStreamEvent{ok: true, footer: "… thinking"}
	case "mcp_tool_call", "tool_call":
		name := firstString(item, "name", "tool", "tool_name")
		if name != "" {
			return codexStreamEvent{ok: true, activity: prefix + name}
		}
	case "todo_list", "web_search":
		return codexStreamEvent{ok: true, activity: prefix + itemType}
	default:
		if itemType != "" {
			// Unknown item types still surface so new Codex versions stay visible.
			summary := itemType
			if cmd := firstString(item, "command", "path", "text"); cmd != "" {
				summary += " " + truncateRunes(cmd, 60)
			}
			return codexStreamEvent{ok: true, activity: prefix + summary}
		}
	}
	return codexStreamEvent{}
}

func simplifyCodexCommand(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// /usr/bin/bash -lc "real command"
	for _, prefix := range []string{`/usr/bin/bash -lc "`, `bash -lc "`, `/bin/bash -lc "`} {
		if strings.HasPrefix(cmd, prefix) && strings.HasSuffix(cmd, `"`) {
			inner := strings.TrimSuffix(strings.TrimPrefix(cmd, prefix), `"`)
			return strings.ReplaceAll(inner, `\"`, `"`)
		}
	}
	return cmd
}
