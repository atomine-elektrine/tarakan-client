package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "test-secret-that-must-never-be-logged"

func TestListOpenJobsUsesGlobalQueueRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/requests" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = response.Write([]byte(`{"jobs":[{"id":3,"status":"open","kind":"code_review","capability":"agent","title":"x","repository":{"owner":"a","name":"b"}}],"tasks":[{"id":3}],"requests":[{"id":3}]}`))
	}))
	defer server.Close()

	client := &Client{baseURL: server.URL, token: "secret-token", httpClient: server.Client()}
	jobs, err := client.ListOpenJobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 3 || jobs[0].Repository.Slug() != "a/b" {
		t.Fatalf("jobs = %#v", jobs)
	}
}

func TestListTasksUsesRepositoryRouteAndBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/github/openai/codex/requests" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Fatalf("authorization header = %q", got)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"tasks":[{"id":7,"repository":{"owner":"openai","name":"codex"},"commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capability":"agent","status":"open"}]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := client.ListTasks(context.Background(), "openai", "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != 7 || tasks[0].Repository.Slug() != "openai/codex" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestTaskMutationsMatchContract(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		call   func(*Client) (Task, error)
		body   map[string]string
	}{
		{name: "show", method: http.MethodGet, path: "/api/requests/9", call: func(client *Client) (Task, error) { return client.GetTask(context.Background(), 9) }},
		{name: "claim", method: http.MethodPost, path: "/api/requests/9/claim", call: func(client *Client) (Task, error) { return client.ClaimTask(context.Background(), 9) }},
		{name: "release", method: http.MethodDelete, path: "/api/requests/9/claim", call: func(client *Client) (Task, error) { return client.ReleaseTask(context.Background(), 9) }},
		{name: "renew", method: http.MethodPost, path: "/api/requests/9/claim/renew", call: func(client *Client) (Task, error) { return client.RenewTaskClaim(context.Background(), 9) }},
		{name: "submit", method: http.MethodPost, path: "/api/requests/9/complete", body: map[string]string{"provenance": "human", "summary": "confirmed", "evidence": "test output"}, call: func(client *Client) (Task, error) {
			return client.SubmitTask(context.Background(), 9, Submission{Provenance: "human", Summary: "confirmed", Evidence: "test output"})
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != test.method || request.URL.Path != test.path {
					t.Fatalf("request = %s %s", request.Method, request.URL.Path)
				}
				if test.body != nil {
					var body map[string]string
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					for key, want := range test.body {
						if body[key] != want {
							t.Fatalf("body[%q] = %q, want %q", key, body[key], want)
						}
					}
				}
				_, _ = response.Write([]byte(`{"id":9,"status":"claimed","repository":{"owner":"openai","name":"codex"}}`))
			}))
			defer server.Close()
			client, err := New(server.URL, testToken, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			task, err := test.call(client)
			if err != nil || task.ID != 9 {
				t.Fatalf("task = %#v, err = %v", task, err)
			}
		})
	}
}

func TestTaskResponseAcceptsDevelopmentWrapper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"task":{"id":12,"status":"open"}}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, testToken, server.Client())
	task, err := client.GetTask(context.Background(), 12)
	if err != nil || task.ID != 12 {
		t.Fatalf("task = %#v, err = %v", task, err)
	}
}

func TestTaskResponseDecodesWebContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{
			"id":42,
			"kind":"privacy_review",
			"capability":"hybrid",
			"title":"Map deletion",
			"description":"Trace retained data",
			"status":"submitted",
			"visibility":"restricted",
			"commit_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"commit_committed_at":"2026-07-10T12:00:00Z",
			"repository":{"id":3,"host":"github","owner":"openai","name":"codex","canonical_url":"https://github.com/openai/codex","participation_mode":"maintainer_verified","record_url":"https://tarakan.lol/github/openai/codex"},
			"creator":{"id":8,"handle":"finder"},
			"claimant":{"id":9,"handle":"reviewer"},
			"lease":{"claimed_at":"2026-07-10T12:01:00Z","expires_at":"2026-07-10T14:01:00Z","active":false},
			"contribution":{"id":6,"provenance":"hybrid","summary":"Confirmed","evidence":"steps","contributor":{"id":9,"handle":"reviewer"},"submitted_at":"2026-07-10T12:30:00Z"},
			"completed_at":"2026-07-10T12:30:00Z",
			"inserted_at":"2026-07-10T12:00:00Z",
			"updated_at":"2026-07-10T12:30:00Z",
			"task_url":"https://tarakan.lol/work/42"
		}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, testToken, server.Client())
	task, err := client.GetTask(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if task.Repository.Host != "github" || task.Repository.CanonicalURL != "https://github.com/openai/codex" {
		t.Fatalf("repository = %#v", task.Repository)
	}
	if task.Repository.ParticipationMode != "maintainer_verified" || task.Status != "submitted" || task.Visibility != "restricted" {
		t.Fatalf("repository/status contract = %#v / %q", task.Repository, task.Status)
	}
	if task.Creator == nil || task.Creator.ID != 8 || task.Creator.Handle != "finder" {
		t.Fatalf("creator = %#v", task.Creator)
	}
	if task.Lease == nil || task.Lease.Active {
		t.Fatalf("lease = %#v", task.Lease)
	}
	if task.Contribution == nil || task.Contribution.SubmittedAt == "" || task.Contribution.Contributor.Handle != "reviewer" {
		t.Fatalf("contribution = %#v", task.Contribution)
	}
}

func TestAPIErrorDoesNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusUnauthorized)
		_, _ = response.Write([]byte(`{"error":"missing or invalid API token"}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, testToken, server.Client())
	_, err := client.GetTask(context.Background(), 1)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("unsafe error = %v", err)
	}
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %#v", err)
	}
}

func TestNewRequiresTokenAndTLSAwayFromLoopback(t *testing.T) {
	if _, err := New(DefaultBaseURL, "", nil); !errors.Is(err, ErrTokenRequired) {
		t.Fatalf("missing token error = %v", err)
	}
	if _, err := New("http://tarakan.lol", testToken, nil); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure URL error = %v", err)
	}
}

func TestClientDoesNotFollowRedirectsWithAuthorization(t *testing.T) {
	redirected := false
	destination := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		redirected = true
	}))
	defer destination.Close()

	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Redirect(response, nil, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, _ := New(source.URL, testToken, source.Client())
	_, err := client.GetTask(context.Background(), 1)
	if err == nil || redirected {
		t.Fatalf("err = %v, redirected = %v", err, redirected)
	}
}

func TestListReviewableRepositoriesQueriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/repositories" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "unscanned" {
			t.Fatalf("status = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"repositories":[{"host":"github.com","owner":"snyk-labs","name":"nodejs-goof","status":"unscanned"}]}`))
	}))
	defer server.Close()

	client, err := New(server.URL, testToken, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	repos, err := client.ListReviewableRepositories(context.Background(), "unscanned")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Slug() != "snyk-labs/nodejs-goof" || repos[0].Status != "unscanned" {
		t.Fatalf("repos = %#v", repos)
	}
}

func TestListScansSurfacesFindings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/github/snyk-labs/nodejs-goof/reports" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"scans":[{"id":15,"commit_sha":"add14ba","review_status":"quarantined","verified":false,"details_visible":true,"findings_count":1,"submitter":"modela","findings":[{"file":"app.js","line_start":83,"severity":"high","title":"Hardcoded secret"}]}]}`))
	}))
	defer server.Close()

	client, _ := New(server.URL, testToken, server.Client())
	scans, err := client.ListScans(context.Background(), "snyk-labs", "nodejs-goof")
	if err != nil {
		t.Fatal(err)
	}
	if len(scans) != 1 || scans[0].ID != 15 || len(scans[0].Findings) != 1 || scans[0].Findings[0].File != "app.js" {
		t.Fatalf("scans = %#v", scans)
	}
}

func TestSubmitScanAndVerdictSendExpectedBodies(t *testing.T) {
	t.Run("scan", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/github/snyk-labs/nodejs-goof/reports" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			var body ScanSubmission
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.CommitSHA != "add14ba" || body.Document.Format != 1 || len(body.Document.Findings) != 1 {
				t.Fatalf("body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":16,"review_status":"quarantined"}`))
		}))
		defer server.Close()
		client, _ := New(server.URL, testToken, server.Client())
		scan, err := client.SubmitScan(context.Background(), "snyk-labs", "nodejs-goof", ScanSubmission{
			CommitSHA: "add14ba", Provenance: "agent", ReviewKind: "code_review",
			Document: ScanDocument{Format: 1, Findings: []ScanFinding{{File: "app.js", Severity: "high", Title: "x", Description: "y"}}},
		})
		if err != nil || scan.ID != 16 {
			t.Fatalf("scan = %#v err = %v", scan, err)
		}
	})

	t.Run("verdict", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/github/snyk-labs/nodejs-goof/reports/15/verdict" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			var body Verdict
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Verdict != "confirmed" || body.Provenance != "hybrid" || body.Evidence == "" {
				t.Fatalf("body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":15,"verified":true,"confirmations":[{"verdict":"confirmed","provenance":"hybrid"}]}`))
		}))
		defer server.Close()
		client, _ := New(server.URL, testToken, server.Client())
		scan, err := client.SubmitVerdict(context.Background(), "snyk-labs", "nodejs-goof", 15, Verdict{
			Verdict: "confirmed", Provenance: "hybrid", Notes: "confirmed the finding", Evidence: "poc here",
		})
		if err != nil || !scan.Verified {
			t.Fatalf("scan = %#v err = %v", scan, err)
		}
	})
}

func TestHostAwareReportPathsSupportTarakanHostedRepositories(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/memory"):
			_ = json.NewEncoder(w).Encode(RepositoryMemory{Findings: []CanonicalFindingMemory{}})
		case r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"scans": []Scan{}})
		default:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(Scan{ID: 1})
		}
	}))
	defer server.Close()
	client, err := New(server.URL, testToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, _ = client.GetRepositoryMemoryForHost(ctx, "tarakan.lol", "alice", "demo", "abc")
	_, _ = client.ListScansForHost(ctx, "tarakan.lol", "alice", "demo")
	_, _ = client.SubmitScanForHost(ctx, "tarakan.lol", "alice", "demo", ScanSubmission{})
	for _, got := range paths {
		if !strings.HasPrefix(got, "/api/tarakan.lol/alice/demo/") {
			t.Fatalf("host-aware path = %q", got)
		}
	}
}

func TestRepositoryMemoryAndFindingCheckContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/github/openai/codex/memory":
			if r.URL.Query().Get("commit_sha") != "abc" {
				t.Fatalf("commit_sha = %q", r.URL.Query().Get("commit_sha"))
			}
			_, _ = w.Write([]byte(`{"repository":"openai/codex","findings":[{"public_id":"finding-1","status":"open","file_path":"auth.go","detections_count":7}]}`))

		case r.Method == http.MethodPost && r.URL.Path == "/api/github/openai/codex/findings/finding-1/check":
			var body FindingVerdict
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.CommitSHA != "abc" || body.Verdict != "confirmed" {
				t.Fatalf("body = %#v", body)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"public_id":"finding-1","status":"open"}`))

		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, _ := New(server.URL, testToken, server.Client())
	memory, err := client.GetRepositoryMemory(context.Background(), "openai", "codex", "abc")
	if err != nil || len(memory.Findings) != 1 || memory.Findings[0].DetectionsCount != 7 {
		t.Fatalf("memory = %#v, err = %v", memory, err)
	}
	err = client.SubmitFindingVerdict(context.Background(), "openai", "codex", "finding-1", FindingVerdict{
		CommitSHA: "abc", Verdict: "confirmed", Provenance: "human", Notes: "independent evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewRunIDIsUnique(t *testing.T) {
	first, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRunID()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "run_") {
		t.Fatalf("run ids = %q, %q", first, second)
	}
}
