package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/atomine-elektrine/tarakan-client/internal/agent"
	"github.com/atomine-elektrine/tarakan-client/internal/api"
	repoctx "github.com/atomine-elektrine/tarakan-client/internal/context"
)

func TestWorkerClaimsRunsAndSubmitsAgentJob(t *testing.T) {
	repository := workerTestRepository(t)
	local := repoctx.Discover(repository)
	var submitted atomic.Int32

	task := api.Task{
		ID: 17, CommitSHA: local.CommitSHA, Kind: "code_review", Capability: "agent",
		Title: "Review authorization", Description: "Inspect the authorization boundary.", Status: "open",
		Repository: api.Repository{Host: "github.com", Owner: "acme", Name: "widget"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []api.Task{task}})
		case r.URL.Path == "/api/requests/17" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(task)
		case r.URL.Path == "/api/requests/17/claim":
			claimed := task
			claimed.Status = "claimed"
			claimed.Lease = &api.Lease{Active: true}
			_ = json.NewEncoder(w).Encode(claimed)
		case r.URL.Path == "/api/requests/17/complete":
			submitted.Add(1)
			var body api.Submission
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode submission: %v", err)
			}
			if body.Document == nil || len(body.Document.Findings) != 1 {
				t.Errorf("unexpected submission: %+v", body)
			}
			result := task
			result.Status = "submitted"
			id := int64(99)
			result.LinkedReviewID = &id
			result.LinkedReview = &api.LinkedReview{ID: id, FindingsCount: 1}
			_ = json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/api/github.com/acme/widget/memory":
			_ = json.NewEncoder(w).Encode(api.RepositoryMemory{Findings: []api.CanonicalFindingMemory{}})
		case r.URL.Path == "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": `{"tarakan_scan_format":1,"findings":[{"file":"main.go","line_start":1,"severity":"high","title":"Missing authorization","description":"The action lacks a guard. Remediation: add an authorization check."}]}`,
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := RunWorker(context.Background(), WorkerOptions{
		APIConfig: api.Config{BaseURL: server.URL, Token: "test-token"},
		Provider:  agent.Provider{Name: "ollama", Kind: agent.KindHTTP, Description: "test model", BaseURL: server.URL + "/v1", Model: "test"},
		Local:     local, Once: true, MaxJobs: 1, StatePath: filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 1 {
		t.Fatalf("submissions = %d", submitted.Load())
	}
}

func TestWorkerEligibilityRequiresAgentCapabilityAndTargetedCheck(t *testing.T) {
	target := int64(9)
	if !workerEligible(api.Task{Kind: "code_review", Capability: "agent"}) {
		t.Fatal("agent report Job should be eligible")
	}
	if !workerEligible(api.Task{Kind: "verify_findings", Capability: "agent", TargetReviewID: &target}) {
		t.Fatal("targeted agent Check Job should be eligible")
	}
	if !workerEligible(api.Task{Kind: "write_fix", Capability: "agent"}) {
		t.Fatal("agent fix Job should be eligible")
	}
	for _, task := range []api.Task{
		{Kind: "code_review", Capability: "human"},
		{Kind: "verify_findings", Capability: "agent"},
		{Kind: "write_fix", Capability: "hybrid"},
	} {
		if workerEligible(task) {
			t.Fatalf("unexpected eligible Job: %+v", task)
		}
	}
}

func TestWorkerProducesStructuredFixArtifact(t *testing.T) {
	repository := workerTestRepository(t)
	local := repoctx.Discover(repository)
	var submitted atomic.Int32

	task := api.Task{
		ID: 18, CommitSHA: local.CommitSHA, Kind: "write_fix", Capability: "agent",
		Title: "Patch authorization", Description: "Add the missing ownership check.", Status: "open",
		Repository: api.Repository{Host: "github.com", Owner: "acme", Name: "widget"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []api.Task{task}})
		case r.URL.Path == "/api/requests/18" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(task)
		case r.URL.Path == "/api/requests/18/claim":
			claimed := task
			claimed.Status = "claimed"
			claimed.Lease = &api.Lease{Active: true}
			_ = json.NewEncoder(w).Encode(claimed)
		case r.URL.Path == "/api/requests/18/complete":
			submitted.Add(1)
			var body api.Submission
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode submission: %v", err)
			}
			if body.Document != nil || !strings.Contains(body.Evidence, "diff --git") || !strings.Contains(body.Evidence, "go test ./...") {
				t.Errorf("unexpected fix submission: %+v", body)
			}
			result := task
			result.Status = "submitted"
			_ = json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": `{"summary":"Add the ownership guard.","patch":"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1,2 @@\n package main\n+// ownership checked","tests":"go test ./..."}`,
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := RunWorker(context.Background(), WorkerOptions{
		APIConfig: api.Config{BaseURL: server.URL, Token: "test-token"},
		Provider:  agent.Provider{Name: "ollama", Kind: agent.KindHTTP, Description: "test model", BaseURL: server.URL + "/v1", Model: "test"},
		Local:     local, Once: true, MaxJobs: 1, StatePath: filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 1 {
		t.Fatalf("submissions = %d", submitted.Load())
	}
}

func TestWorkerVerifiesEveryTargetFinding(t *testing.T) {
	repository := workerTestRepository(t)
	local := repoctx.Discover(repository)
	targetID := int64(31)
	task := api.Task{
		ID: 19, CommitSHA: local.CommitSHA, Kind: "verify_findings", Capability: "agent",
		Title: "Verify report", Description: "Reproduce every finding.", Status: "open",
		Repository:     api.Repository{Host: "github.com", Owner: "acme", Name: "widget"},
		TargetReviewID: &targetID,
		TargetReview: &api.LinkedReview{
			ID: targetID, CommitSHA: local.CommitSHA, FindingsCount: 1,
			Findings: []api.Finding{{CanonicalFindingID: "finding-1", File: "main.go", LineStart: 1, Severity: "high", Title: "Missing guard", Description: "Ownership is not checked."}},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/requests":
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []api.Task{task}})
		case r.URL.Path == "/api/requests/19" && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(task)
		case r.URL.Path == "/api/requests/19/claim":
			claimed := task
			claimed.Status = "claimed"
			claimed.Lease = &api.Lease{Active: true}
			_ = json.NewEncoder(w).Encode(claimed)
		case r.URL.Path == "/api/requests/19/complete":
			var body api.Submission
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Verdict != "confirmed" || !strings.Contains(body.Evidence, "curl reproduction") {
				t.Errorf("unexpected verification submission: %+v", body)
			}
			result := task
			result.Status = "submitted"
			_ = json.NewEncoder(w).Encode(result)
		case r.URL.Path == "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{
				"role": "assistant", "content": `{"checks":[{"finding_id":"finding-1","verdict":"confirmed","notes":"Reproduced the missing ownership check at the pinned commit.","poc":"curl reproduction returned another user's record"}]}`,
			}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := RunWorker(context.Background(), WorkerOptions{
		APIConfig: api.Config{BaseURL: server.URL, Token: "test-token"},
		Provider:  agent.Provider{Name: "ollama", Kind: agent.KindHTTP, Description: "test model", BaseURL: server.URL + "/v1", Model: "test"},
		Local:     local, Once: true, MaxJobs: 1, StatePath: filepath.Join(t.TempDir(), "state.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeterministicWorkerRunID(t *testing.T) {
	repository := api.QueueRepository{Host: "github.com", Owner: "Acme", Name: "Widget"}
	a := deterministicWorkerRunID(repository, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Codex")
	b := deterministicWorkerRunID(repository, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "codex")
	if a != b || a == "" {
		t.Fatalf("run ids differ: %q %q", a, b)
	}
	if a == deterministicWorkerRunID(repository, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "codex") {
		t.Fatal("different commits must not share a run id")
	}
}

func workerTestRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runWorkerGit(t, dir, "init")
	runWorkerGit(t, dir, "config", "user.email", "test@example.com")
	runWorkerGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runWorkerGit(t, dir, "add", "main.go")
	runWorkerGit(t, dir, "commit", "-m", "initial")
	runWorkerGit(t, dir, "remote", "add", "origin", "https://github.com/acme/widget.git")
	return dir
}

func runWorkerGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
