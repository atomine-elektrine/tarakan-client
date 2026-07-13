//go:build live

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGrokLiveToolProgress(t *testing.T) {
	if _, err := exec.LookPath("grok"); err != nil {
		t.Skip("grok not installed")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, _ := exec.LookPath("grok")
	p := Provider{Name: "grok", Kind: KindCLI, Command: "grok", Description: "Grok Build", Path: path}
	var mu sync.Mutex
	var lines []string
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := Run(ctx, p, Request{
		Prompt:    "Read sample.txt and reply with only the file contents.",
		Directory: dir,
		Progress: func(s string) {
			mu.Lock()
			lines = append(lines, s)
			mu.Unlock()
			t.Log("progress:", s)
		},
	})
	if err != nil {
		t.Fatalf("run: %v out=%q", err, out)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Read") && !strings.Contains(joined, "sample.txt") {
		t.Fatalf("expected read activity in progress:\n%s", joined)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("output = %q", out)
	}
}
