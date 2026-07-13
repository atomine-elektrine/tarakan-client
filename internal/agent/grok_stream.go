package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// runGrok runs Grok Build headless with streaming-json and surfaces live tool
// activity (reads, shell, subagents) via Progress by tailing the session log.
func runGrok(ctx context.Context, provider Provider, request Request) (string, error) {
	if provider.Path == "" {
		return "", ErrUnavailable
	}
	sessionID := newSessionUUID()
	cwd := request.Directory
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	args := []string{
		"-p", securityPrompt(request.Prompt),
		"--output-format", "streaming-json",
		"--session-id", sessionID,
		// Unattended inside Tarakan's disposable snapshot.
		// Important: "dontAsk" DENIES tools that would prompt (shell, etc.) and
		// ends the turn as permission_cancelled. Use bypassPermissions so the
		// agent can actually finish a security review.
		"--always-approve",
		"--permission-mode", "bypassPermissions",
	}
	command := exec.CommandContext(ctx, provider.Path, args...)
	command.Dir = cwd
	command.Env = subprocessEnvironment(os.Environ())

	stdout, err := command.StdoutPipe()
	if err != nil {
		return "", err
	}
	// Keep stderr for failure diagnostics; do not mix into the answer.
	var stderrBuf bytes.Buffer
	command.Stderr = &stderrBuf

	if err := command.Start(); err != nil {
		return "", fmt.Errorf("%s failed to start: %w", provider.Description, err)
	}

	// Independent of parent cancel so we can drain session logs after exit.
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	var (
		textBuf    strings.Builder
		textMu     sync.Mutex
		lastFooter string
		footerMu   sync.Mutex
	)
	progress := request.Progress
	report := func(line string) {
		if progress == nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		progress(line)
	}
	// Footer-only pulse for token-level chatter (starts with ellipsis).
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

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		watchGrokSessionActivity(watchCtx, cwd, sessionID, report)
	}()

	// Heartbeat so the TUI does not look frozen during long reasoning.
	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		n := 0
		for {
			select {
			case <-heartbeatDone:
				return
			case <-ticker.C:
				n++
				reportFooter(fmt.Sprintf("… Grok still working (%ds)", n*8))
			}
		}
	}()

	// Parse streaming-json for the final answer (and light live text pulse).
	var stopReason string
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var event struct {
			Type       string          `json:"type"`
			Data       json.RawMessage `json:"data"`
			Msg        string          `json:"message"`
			StopReason string          `json:"stopReason"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		switch event.Type {
		case "text":
			chunk := jsonString(event.Data)
			if chunk == "" {
				continue
			}
			textMu.Lock()
			textBuf.WriteString(chunk)
			textMu.Unlock()
			reportFooter("… writing response")
		case "thought":
			reportFooter("… thinking")
		case "error":
			msg := event.Msg
			if msg == "" {
				msg = string(event.Data)
			}
			if msg != "" {
				report("Grok error: " + msg)
			}
		case "end":
			stopReason = event.StopReason
			// finished
		}
	}
	_ = scanner.Err()
	close(heartbeatDone)

	waitErr := command.Wait()
	// Let the session tail catch trailing tool events, then stop watching.
	select {
	case <-watchDone:
	case <-time.After(1200 * time.Millisecond):
		watchCancel()
		<-watchDone
	}

	textMu.Lock()
	output := strings.TrimSpace(textBuf.String())
	textMu.Unlock()
	if waitErr != nil {
		errText := strings.TrimSpace(stderrBuf.String())
		if output == "" && errText != "" {
			output = errText
		}
		return output, fmt.Errorf("%s failed: %w", provider.Description, waitErr)
	}
	// Permission / user cancel ends the turn without a useful document.
	if isGrokCancelledStop(stopReason) || looksLikePermissionCancel(output) {
		msg := "Grok turn was cancelled (often a tool permission). Re-run; shell tools need bypassPermissions."
		if stopReason != "" {
			msg = "Grok turn cancelled (" + stopReason + ")"
		}
		report(msg)
		if output == "" {
			return output, fmt.Errorf("%s", msg)
		}
		return output, fmt.Errorf("%s", msg)
	}
	if output == "" {
		return "", fmt.Errorf("%s finished with no response text", provider.Description)
	}
	return output, nil
}

func isGrokCancelledStop(reason string) bool {
	r := strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(r, "cancel") || strings.Contains(r, "permission")
}

func looksLikePermissionCancel(output string) bool {
	o := strings.ToLower(output)
	return strings.Contains(o, "user cancelled") || strings.Contains(o, "permission_cancelled")
}

// watchGrokSessionActivity tails ~/.grok/sessions/<cwd-key>/<id>/updates.jsonl
// and emits human-readable tool/subagent lines.
func watchGrokSessionActivity(ctx context.Context, cwd, sessionID string, report func(string)) {
	path := grokUpdatesPath(cwd, sessionID)
	// Wait for the file (session may take a moment to create).
	deadline := time.Now().Add(30 * time.Second)
	for {
		if ctx.Err() != nil {
			return
		}
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	seen := make(map[string]struct{})
	idleRounds := 0
	for {
		if ctx.Err() != nil {
			return
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				idleRounds++
				// Parent closes watch shortly after process exit; keep reading
				// a bit so late tool_completed lines show up.
				if idleRounds > 40 {
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
				continue
			}
			return
		}
		idleRounds = 0
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if msg, ok := formatGrokUpdateLine(line); ok {
			key := normalizeActivityKey(msg)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			report(msg)
		}
	}
}

func grokUpdatesPath(cwd, sessionID string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".grok", "sessions", encodeGrokSessionDir(cwd), sessionID, "updates.jsonl")
}

// encodeGrokSessionDir matches Grok's session folder naming: each / → %2F.
func encodeGrokSessionDir(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	return strings.ReplaceAll(cwd, "/", "%2F")
}

// formatGrokUpdateLine turns one updates.jsonl row into a UI status line.
func formatGrokUpdateLine(raw string) (string, bool) {
	var row struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		return "", false
	}
	u := row.Params.Update
	if u == nil {
		return "", false
	}
	sessionUpdate, _ := u["sessionUpdate"].(string)
	switch sessionUpdate {
	case "tool_call", "tool_call_update":
		return formatGrokToolUpdate(u)
	default:
		return "", false
	}
}

func formatGrokToolUpdate(u map[string]any) (string, bool) {
	rawInput, _ := u["rawInput"].(map[string]any)
	meta, _ := u["_meta"].(map[string]any)
	toolMeta, _ := meta["x.ai/tool"].(map[string]any)
	name, _ := toolMeta["name"].(string)
	title, _ := u["title"].(string)
	title = strings.TrimSpace(title)
	if name == "" {
		name = title
	}
	// Always prefer structured input when present so grep titles that are just
	// the raw pattern ("password|secret|…") become "Grep `password|…`".
	label := formatGrokToolFromInput(name, rawInput)
	if label == "" {
		label = formatGrokToolFromInput(title, rawInput)
	}
	if label == "" && title != "" && !isBareToolName(title) && !looksLikeRegexPattern(title) {
		label = title
	}
	if label == "" {
		// Incomplete early event (title=grep, no input yet) - skip.
		return "", false
	}
	status, _ := u["status"].(string)
	switch status {
	case "completed":
		return "✓ " + label, true
	case "failed", "error", "cancelled":
		return "✗ " + label, true
	default:
		return "→ " + label, true
	}
}

func isBareToolName(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "read_file", "list_dir", "grep", "run_terminal_command", "spawn_subagent",
		"web_search", "search_replace", "write", "read", "bash", "shell", "task":
		return true
	default:
		return false
	}
}

// looksLikeRegexPattern is true when Grok uses the grep pattern as the tool title
// (e.g. "password|secret|api_key") instead of "Grep …".
func looksLikeRegexPattern(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	// Human titles usually include a verb + path ("Read `foo`"); bare patterns
	// are full of alternation / escapes.
	return strings.Contains(s, "|") || strings.Contains(s, `\.`) || strings.Contains(s, ".*") ||
		strings.Contains(s, `\(`) || strings.HasPrefix(s, "^")
}

func formatGrokToolFromInput(name string, input map[string]any) string {
	name = strings.TrimSpace(name)
	if input == nil {
		input = map[string]any{}
	}
	switch name {
	case "read_file", "ReadFile":
		path := firstString(input, "target_file", "path", "file")
		if path != "" {
			return "Read " + path
		}
		return ""
	case "list_dir":
		path := firstString(input, "target_directory", "path")
		if path != "" {
			return "List " + path
		}
		return ""
	case "grep", "Grep":
		pat := firstString(input, "pattern")
		path := firstString(input, "path")
		glob := firstString(input, "glob")
		if pat != "" && path != "" {
			return "Grep " + quoteShort(pat) + " in " + path
		}
		if pat != "" && glob != "" {
			return "Grep " + quoteShort(pat) + " (" + glob + ")"
		}
		if pat != "" {
			return "Grep " + quoteShort(pat)
		}
		return ""
	case "run_terminal_command":
		cmd := firstString(input, "command")
		desc := firstString(input, "description")
		if desc != "" {
			return "Shell: " + desc
		}
		if cmd != "" {
			return "Shell: " + truncateRunes(cmd, 80)
		}
		return ""
	case "spawn_subagent", "Task":
		desc := firstString(input, "description", "prompt")
		kind := firstString(input, "subagent_type", "agent")
		if kind != "" && desc != "" {
			return "Subagent " + kind + ": " + truncateRunes(desc, 60)
		}
		if kind != "" {
			return "Subagent " + kind
		}
		if desc != "" {
			return "Subagent: " + truncateRunes(desc, 60)
		}
		return ""
	case "web_search":
		q := firstString(input, "query")
		if q != "" {
			return "Web search: " + truncateRunes(q, 60)
		}
		return "Web search"
	case "search_replace", "write":
		path := firstString(input, "file_path", "path")
		if path != "" {
			return "Edit " + path
		}
		return "Edit file"
	default:
		if name == "" {
			return ""
		}
		// Generic: show tool name + one interesting arg if any.
		for _, k := range []string{"path", "target_file", "command", "query", "description"} {
			if v := firstString(input, k); v != "" {
				return name + " " + truncateRunes(v, 60)
			}
		}
		return name
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				if s := strings.TrimSpace(t); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func quoteShort(s string) string {
	s = truncateRunes(s, 40)
	return "`" + s + "`"
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// normalizeActivityKey collapses "→ Read `x`" / "✓ Read x" into one dedupe key
// so we do not double-log the same tool under slightly different titles.
func normalizeActivityKey(msg string) string {
	msg = strings.TrimSpace(msg)
	for _, p := range []string{"→ ", "✓ ", "✗ "} {
		msg = strings.TrimPrefix(msg, p)
	}
	msg = strings.ReplaceAll(msg, "`", "")
	return strings.ToLower(strings.Join(strings.Fields(msg), " "))
}

func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.Trim(string(raw), `"`)
}

func newSessionUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; fall back to time-based uniqueness.
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
