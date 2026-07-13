package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"tarakan-client/internal/agent"
	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
	"tarakan-client/internal/reviewdoc"
)

// WorkerOptions configures the durable, headless Job consumer.
type WorkerOptions struct {
	APIConfig       api.Config
	Provider        agent.Provider
	Local           repoctx.Info
	Once            bool
	Interval        time.Duration
	MaxJobs         int
	ReviewUnscanned bool
	SkipCritic      bool
	StatePath       string
	Progress        func(string)
}

type workerRecord struct {
	JobID       int64     `json:"job_id"`
	CommitSHA   string    `json:"commit_sha"`
	RunID       string    `json:"run_id,omitempty"`
	Attempts    int       `json:"attempts"`
	Completed   bool      `json:"completed"`
	LastError   string    `json:"last_error,omitempty"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type workerJournal struct {
	Version int                     `json:"version"`
	Jobs    map[string]workerRecord `json:"jobs"`
	path    string
	mu      sync.Mutex
}

// RunWorker continuously claims agent-capability report Jobs and publishes
// their structured results. State is persisted so restarts preserve backoff
// and completed-run knowledge.
func RunWorker(ctx context.Context, opts WorkerOptions) error {
	if opts.Provider.Name == "" {
		return errors.New("worker requires an available agent")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.MaxJobs <= 0 {
		opts.MaxJobs = 100
	}
	if opts.Progress == nil {
		opts.Progress = func(string) {}
	}
	client, err := opts.APIConfig.Client()
	if err != nil {
		return err
	}
	journal, err := loadWorkerJournal(opts.StatePath)
	if err != nil {
		return err
	}
	opts.Progress("Worker state: " + journal.path)

	for {
		processed, passErr := runWorkerPass(ctx, client, journal, opts)
		if opts.Once {
			return passErr
		}
		if passErr != nil {
			opts.Progress("Worker pass error: " + passErr.Error())
		} else if processed == 0 {
			opts.Progress("No eligible agent Jobs; polling again in " + opts.Interval.String())
		}
		timer := time.NewTimer(opts.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func runWorkerPass(ctx context.Context, client *api.Client, journal *workerJournal, opts WorkerOptions) (int, error) {
	tasks, err := retryValue(ctx, func() ([]api.Task, error) { return client.ListOpenJobs(ctx) })
	if err != nil {
		return 0, fmt.Errorf("load Job queue: %w", err)
	}
	processed := 0
	var failures []string
	for _, task := range tasks {
		if processed >= opts.MaxJobs {
			break
		}
		if !workerEligible(task) || !isPickable(task) {
			continue
		}
		key := workerJobKey(task)
		if !journal.ready(key, task) {
			continue
		}
		processed++
		if err := runWorkerJob(ctx, client, journal, key, task, opts); err != nil {
			failures = append(failures, fmt.Sprintf("Job #%d: %v", task.ID, err))
		}
	}
	if opts.ReviewUnscanned && processed < opts.MaxJobs {
		count, unscannedErr := runUnscannedPass(ctx, client, journal, opts, opts.MaxJobs-processed)
		processed += count
		if unscannedErr != nil {
			failures = append(failures, unscannedErr.Error())
		}
	}
	if len(failures) > 0 {
		return processed, errors.New(strings.Join(failures, "; "))
	}
	return processed, nil
}

func runUnscannedPass(ctx context.Context, client *api.Client, journal *workerJournal, opts WorkerOptions, limit int) (int, error) {
	repositories, err := retryValue(ctx, func() ([]api.QueueRepository, error) {
		return client.ListReviewableRepositories(ctx, "unscanned")
	})
	if err != nil {
		return 0, fmt.Errorf("load unscanned queue: %w", err)
	}
	processed := 0
	var failures []string
	for _, repository := range repositories {
		if processed >= limit {
			break
		}
		root, cleanup, err := cloneQueueRepository(repository, client.BaseURL(), opts.Progress)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", repository.Slug(), err))
			continue
		}
		info := repoctx.Discover(root)
		if len(info.CommitSHA) != 40 {
			cleanup()
			failures = append(failures, repository.Slug()+": clone has no full HEAD commit")
			continue
		}
		key := fmt.Sprintf("repository:%s:%s@%s:%s", strings.ToLower(repository.Host), strings.ToLower(repository.Slug()), strings.ToLower(info.CommitSHA), strings.ToLower(opts.Provider.ModelIdentifier()))
		if !journal.readyRepository(key) {
			cleanup()
			continue
		}
		processed++
		runID := deterministicWorkerRunID(repository, info.CommitSHA, opts.Provider.ModelIdentifier())
		beginErr := journal.beginRepository(key, info.CommitSHA, runID)
		if beginErr != nil {
			cleanup()
			failures = append(failures, fmt.Sprintf("%s: %v", repository.Slug(), beginErr))
			continue
		}
		opts.Progress(fmt.Sprintf("Reviewing unscanned repository %s @ %s", repository.Slug(), shortSHA(info.CommitSHA)))
		output, runErr := runAgentInSnapshotContext(ctx, root, info.CommitSHA, opts.Provider, reviewdoc.FormatPrompt, opts.Progress)
		if runErr == nil {
			var doc api.ScanDocument
			doc, runErr = reviewdoc.Parse(output)
			if runErr == nil && !opts.SkipCritic {
				doc, runErr = criticDocument(ctx, root, info.CommitSHA, opts.Provider, doc, opts.Progress)
			}
			if runErr == nil {
				doc, runErr = reconcileDocumentForHostContext(ctx, client, repository.Host, repository.Owner, repository.Name, info.CommitSHA, root, opts.Provider, doc, opts.Progress)
			}
			if runErr == nil {
				runErr = reviewdoc.Validate(doc)
			}
			if runErr == nil {
				scan, submitErr := client.SubmitScanForHost(ctx, repository.Host, repository.Owner, repository.Name, api.ScanSubmission{
					CommitSHA: info.CommitSHA, Provenance: "agent", ReviewKind: "code_review",
					Model: opts.Provider.ModelIdentifier(), PromptVersion: "tarakan-worker/v1",
					RunID: runID, Document: doc,
				})
				if submitErr != nil {
					// Resolve a lost success response through the durable run id.
					scans, listErr := client.ListScansForHost(ctx, repository.Host, repository.Owner, repository.Name)
					for _, existing := range scans {
						if existing.RunID == runID {
							scan = existing
							submitErr = nil
							break
						}
					}
					if listErr != nil {
						submitErr = fmt.Errorf("submit failed (%v), then lookup failed: %w", submitErr, listErr)
					}
				}
				if submitErr == nil {
					opts.Progress(fmt.Sprintf("Published Report #%d with %d finding(s) for %s", scan.ID, scan.FindingsCount, repository.Slug()))
				} else {
					runErr = submitErr
				}
			}
		}
		cleanup()
		if runErr != nil {
			_ = journal.fail(key, runErr)
			failures = append(failures, fmt.Sprintf("%s: %v", repository.Slug(), runErr))
			continue
		}
		if err := journal.complete(key); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", repository.Slug(), err))
		}
	}
	if len(failures) > 0 {
		return processed, errors.New(strings.Join(failures, "; "))
	}
	return processed, nil
}

func cloneQueueRepository(repository api.QueueRepository, apiBase string, progress func(string)) (string, func(), error) {
	remote, err := cloneRemoteURL(api.Repository{
		Host: repository.Host, Owner: repository.Owner, Name: repository.Name,
	}, apiBase)
	if err != nil {
		return "", func() {}, err
	}
	base, err := os.MkdirTemp("", "tarakan-worker-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(base) }
	root := filepath.Join(base, "repository")
	progress("Cloning " + repository.Slug() + "…")
	if err := runGit("", "clone", "--depth=1", "--filter=blob:none", "--", remote, root); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("clone: %w", err)
	}
	return root, cleanup, nil
}

func runWorkerJob(ctx context.Context, client *api.Client, journal *workerJournal, key string, queued api.Task, opts WorkerOptions) (runErr error) {
	record := journal.begin(key, queued)
	if err := journal.save(); err != nil {
		return err
	}
	opts.Progress(fmt.Sprintf("Job #%d attempt %d: %s · %s", queued.ID, record.Attempts, queued.Repository.Slug(), queued.Title))

	task, err := retryValue(ctx, func() (api.Task, error) { return client.GetTask(ctx, queued.ID) })
	if err != nil {
		return journal.fail(key, fmt.Errorf("load: %w", err))
	}
	if !workerEligible(task) {
		return journal.fail(key, errors.New("Job is no longer eligible for agent automation"))
	}
	claimedHere := !isMyActiveClaim(task)
	if _, err := retryValue(ctx, func() (api.Task, error) { return client.ClaimTask(ctx, task.ID) }); err != nil {
		return journal.fail(key, fmt.Errorf("claim: %w", err))
	}

	completed := false
	defer func() {
		if completed || !claimedHere {
			return
		}
		releaseCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := retryValue(releaseCtx, func() (api.Task, error) { return client.ReleaseTask(releaseCtx, task.ID) }); err != nil {
			opts.Progress(fmt.Sprintf("Warning: could not release Job #%d: %v", task.ID, err))
		} else {
			opts.Progress(fmt.Sprintf("Released Job #%d after failure", task.ID))
		}
	}()

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	leaseErrors := make(chan error, 1)
	stopHeartbeat := startLeaseHeartbeat(runCtx, client, task.ID, cancelRun, leaseErrors, opts.Progress)
	defer stopHeartbeat()

	root, cleanup, err := worktreeForTask(opts.Local, task, client.BaseURL(), opts.Progress)
	if err != nil {
		return journal.fail(key, fmt.Errorf("prepare repository: %w", err))
	}
	defer cleanup()
	if task.Kind == "verify_findings" {
		submitted, err := runWorkerVerification(runCtx, client, task, root, opts, leaseErrors)
		if err != nil {
			return journal.fail(key, err)
		}
		completed = true
		if err := journal.complete(key); err != nil {
			return err
		}
		opts.Progress(fmt.Sprintf("Completed Check Job #%d (status %s)", submitted.ID, submitted.Status))
		return nil
	}

	var submission api.Submission
	if task.Kind == "write_fix" {
		output, err := runAgentInSnapshotContext(runCtx, root, task.CommitSHA, opts.Provider, fixPrompt(task), opts.Progress)
		if err != nil {
			return journal.fail(key, fmt.Errorf("fix agent: %w", preferLeaseError(err, leaseErrors)))
		}
		summary, evidence, err := parseFixArtifact(output)
		if err != nil {
			return journal.fail(key, fmt.Errorf("parse fix: %w", err))
		}
		submission = api.Submission{
			Provenance: "agent", Summary: summary, Evidence: evidence,
			Model: opts.Provider.ModelIdentifier(), PromptVersion: "tarakan-worker/fix-v1",
		}
	} else {
		prompt := reviewdoc.TaskFormatPromptForKind(task.Kind, task.Title, task.Description)
		output, err := runAgentInSnapshotContext(runCtx, root, task.CommitSHA, opts.Provider, prompt, opts.Progress)
		if err != nil {
			return journal.fail(key, fmt.Errorf("agent: %w", preferLeaseError(err, leaseErrors)))
		}
		doc, err := reviewdoc.Parse(output)
		if err != nil {
			return journal.fail(key, fmt.Errorf("parse output: %w", err))
		}
		if !opts.SkipCritic {
			doc, err = criticDocument(runCtx, root, task.CommitSHA, opts.Provider, doc, opts.Progress)
			if err != nil {
				return journal.fail(key, fmt.Errorf("critic: %w", preferLeaseError(err, leaseErrors)))
			}
		}
		doc, err = reconcileDocumentForHostContext(runCtx, client, task.Repository.Host, task.Repository.Owner, task.Repository.Name, task.CommitSHA, root, opts.Provider, doc, opts.Progress)
		if err != nil {
			return journal.fail(key, fmt.Errorf("reconcile: %w", preferLeaseError(err, leaseErrors)))
		}
		if err := reviewdoc.Validate(doc); err != nil {
			return journal.fail(key, fmt.Errorf("validate: %w", err))
		}
		submission = api.Submission{
			Provenance:    "agent",
			Summary:       reviewdoc.SummaryFromDocument(doc, 2_000),
			Model:         opts.Provider.ModelIdentifier(),
			PromptVersion: "tarakan-worker/v1",
			Document:      &doc,
		}
	}
	submitted, err := submitJobWithRecovery(runCtx, client, task.ID, submission)
	if err != nil {
		return journal.fail(key, err)
	}
	completed = true
	if err := journal.complete(key); err != nil {
		return err
	}
	if submitted.LinkedReview != nil {
		opts.Progress(fmt.Sprintf("Published Report #%d with %d finding(s) via Job #%d", submitted.LinkedReview.ID, submitted.LinkedReview.FindingsCount, task.ID))
	} else {
		opts.Progress(fmt.Sprintf("Completed Job #%d", task.ID))
	}
	return nil
}

func workerEligible(task api.Task) bool {
	return task.Capability == "agent" &&
		(reviewdoc.FindingKinds[task.Kind] || task.Kind == "write_fix" ||
			task.Kind == "verify_findings" && task.TargetReviewID != nil)
}

func runWorkerVerification(ctx context.Context, client *api.Client, task api.Task, root string, opts WorkerOptions, leaseErrors <-chan error) (api.Task, error) {
	if task.TargetReview == nil || len(task.TargetReview.Findings) == 0 {
		return api.Task{}, errors.New("Check Job has no visible target Report findings")
	}
	target := api.Scan{
		ID: task.TargetReview.ID, CommitSHA: task.TargetReview.CommitSHA,
		FindingsCount: task.TargetReview.FindingsCount, Findings: task.TargetReview.Findings,
		DetailsVisible: true,
	}
	output, err := runAgentInSnapshotContext(ctx, root, task.CommitSHA, opts.Provider, verifyPrompt(target), opts.Progress)
	if err != nil {
		return api.Task{}, fmt.Errorf("verification agent: %w", preferLeaseError(err, leaseErrors))
	}
	checks, err := parseFindingChecks(output, task.CommitSHA)
	if err != nil {
		return api.Task{}, fmt.Errorf("parse checks: %w", err)
	}
	expected := make(map[string]bool, len(target.Findings))
	for _, finding := range target.Findings {
		if finding.CanonicalFindingID == "" {
			return api.Task{}, errors.New("target Report contains a finding without canonical identity")
		}
		expected[finding.CanonicalFindingID] = true
	}
	seen := make(map[string]bool, len(checks))
	for _, check := range checks {
		if !expected[check.findingID] {
			return api.Task{}, fmt.Errorf("agent checked unexpected finding %s", check.findingID)
		}
		if seen[check.findingID] {
			return api.Task{}, fmt.Errorf("agent checked finding %s more than once", check.findingID)
		}
		seen[check.findingID] = true
	}
	if len(seen) != len(expected) {
		return api.Task{}, fmt.Errorf("agent checked %d of %d target findings", len(seen), len(expected))
	}
	verdict := "confirmed"
	var notes, evidence strings.Builder
	for i, check := range checks {
		if check.verdict.Verdict != "confirmed" {
			verdict = "disputed"
		}
		if i > 0 {
			notes.WriteString("\n")
			evidence.WriteString("\n\n")
		}
		fmt.Fprintf(&notes, "[%s] %s: %s", check.findingID, check.verdict.Verdict, check.verdict.Notes)
		fmt.Fprintf(&evidence, "[%s]\n%s", check.findingID, check.verdict.Evidence)
	}
	summary := truncate(notes.String(), 2_000)
	if len([]rune(strings.TrimSpace(summary))) < 20 {
		summary = "Independent agent check completed for every visible finding."
	}
	return submitJobWithRecovery(ctx, client, task.ID, api.Submission{
		Provenance: "agent", Verdict: verdict, Notes: summary, Summary: summary,
		Evidence: truncate(evidence.String(), 10_000),
	})
}

func submitJobWithRecovery(ctx context.Context, client *api.Client, jobID int64, submission api.Submission) (api.Task, error) {
	submitted, err := client.SubmitTask(ctx, jobID, submission)
	if err == nil {
		return submitted, nil
	}
	// A response can be lost after the server commits. Resolve that ambiguity
	// by reading the Job before deciding to retry the mutation.
	latest, getErr := retryValue(ctx, func() (api.Task, error) { return client.GetTask(ctx, jobID) })
	if getErr == nil && latest.Status == "submitted" && latest.LinkedReviewID != nil {
		return latest, nil
	}
	return api.Task{}, fmt.Errorf("submit: %w", err)
}

func criticDocument(ctx context.Context, root, commit string, provider agent.Provider, discovery api.ScanDocument, progress func(string)) (api.ScanDocument, error) {
	progress(fmt.Sprintf("Critic pass: validating %d candidate finding(s)…", len(discovery.Findings)))
	output, err := runAgentInSnapshotContext(ctx, root, commit, provider, reviewdoc.CriticPrompt(discovery), progress)
	if err != nil {
		return api.ScanDocument{}, err
	}
	return reviewdoc.Parse(output)
}

func startLeaseHeartbeat(ctx context.Context, client *api.Client, jobID int64, cancel context.CancelFunc, errorsOut chan<- error, progress func(string)) func() {
	heartbeatCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(20 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				_, err := retryValue(heartbeatCtx, func() (api.Task, error) {
					return client.RenewTaskClaim(heartbeatCtx, jobID)
				})
				if err != nil {
					select {
					case errorsOut <- fmt.Errorf("lease renewal failed: %w", err):
					default:
					}
					cancel()
					return
				}
				progress(fmt.Sprintf("Renewed lease for Job #%d", jobID))
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func preferLeaseError(fallback error, leaseErrors <-chan error) error {
	select {
	case err := <-leaseErrors:
		return err
	default:
		return fallback
	}
}

func retryValue[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	var zero T
	var last error
	for attempt := 0; attempt < 4; attempt++ {
		value, err := operation()
		if err == nil {
			return value, nil
		}
		last = err
		if !retryable(err) || attempt == 3 {
			break
		}
		delay := time.Duration(1<<attempt) * time.Second
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, last
}

func retryable(err error) bool {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

func workerJobKey(task api.Task) string {
	return fmt.Sprintf("job:%d:%s", task.ID, strings.ToLower(task.CommitSHA))
}

func loadWorkerJournal(path string) (*workerJournal, error) {
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(dir, "tarakan", "worker-state.json")
	}
	journal := &workerJournal{Version: 1, Jobs: map[string]workerRecord{}, path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return journal, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worker state: %w", err)
	}
	if err := json.Unmarshal(raw, journal); err != nil {
		return nil, fmt.Errorf("decode worker state: %w", err)
	}
	journal.path = path
	if journal.Jobs == nil {
		journal.Jobs = map[string]workerRecord{}
	}
	return journal, nil
}

func (j *workerJournal) ready(key string, task api.Task) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.Jobs[key]
	if !ok {
		return true
	}
	// A reviewer may request another attempt on the same Job and commit.
	if record.Completed && task.Status == "changes_requested" {
		return true
	}
	return !record.Completed && (record.NextAttempt.IsZero() || !time.Now().Before(record.NextAttempt))
}

func (j *workerJournal) readyRepository(key string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.Jobs[key]
	return !ok || !record.Completed && (record.NextAttempt.IsZero() || !time.Now().Before(record.NextAttempt))
}

func (j *workerJournal) begin(key string, task api.Task) workerRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	record := j.Jobs[key]
	record.JobID = task.ID
	record.CommitSHA = task.CommitSHA
	record.Attempts++
	record.LastError = ""
	record.NextAttempt = time.Time{}
	record.UpdatedAt = time.Now().UTC()
	j.Jobs[key] = record
	return record
}

func deterministicWorkerRunID(repository api.QueueRepository, commitSHA, model string) string {
	identity := strings.Join([]string{
		"tarakan-worker/v1", strings.ToLower(repository.Host), strings.ToLower(repository.Owner),
		strings.ToLower(repository.Name), strings.ToLower(commitSHA), strings.ToLower(model),
	}, "\x1f")
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("worker-v1-%x", digest[:])
}

func (j *workerJournal) beginRepository(key, commitSHA, runID string) error {
	j.mu.Lock()
	record := j.Jobs[key]
	if record.RunID == "" {
		record.RunID = runID
	}
	record.CommitSHA = commitSHA
	record.Attempts++
	record.LastError = ""
	record.NextAttempt = time.Time{}
	record.UpdatedAt = time.Now().UTC()
	j.Jobs[key] = record
	j.mu.Unlock()
	if err := j.save(); err != nil {
		return err
	}
	return nil
}

func (j *workerJournal) fail(key string, err error) error {
	j.mu.Lock()
	record := j.Jobs[key]
	record.LastError = err.Error()
	record.UpdatedAt = time.Now().UTC()
	delay := time.Duration(1<<min(record.Attempts, 6)) * 30 * time.Second
	record.NextAttempt = time.Now().UTC().Add(delay)
	j.Jobs[key] = record
	j.mu.Unlock()
	if saveErr := j.save(); saveErr != nil {
		return fmt.Errorf("%v (also failed to save worker state: %w)", err, saveErr)
	}
	return err
}

func (j *workerJournal) complete(key string) error {
	j.mu.Lock()
	record := j.Jobs[key]
	record.Completed = true
	record.LastError = ""
	record.NextAttempt = time.Time{}
	record.UpdatedAt = time.Now().UTC()
	j.Jobs[key] = record
	j.mu.Unlock()
	return j.save()
}

func (j *workerJournal) save() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(j.path), 0o700); err != nil {
		return fmt.Errorf("create worker state directory: %w", err)
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	temp := j.path + ".tmp"
	if err := os.WriteFile(temp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write worker state: %w", err)
	}
	if err := os.Rename(temp, j.path); err != nil {
		_ = os.Remove(temp)
		return fmt.Errorf("replace worker state: %w", err)
	}
	return nil
}
