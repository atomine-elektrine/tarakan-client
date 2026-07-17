package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/atomine-elektrine/tarakan-client/internal/agent"
	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/reviewdoc"
	"github.com/atomine-elektrine/tarakan-client/internal/session"
	"github.com/atomine-elektrine/tarakan-client/internal/snapshot"
)

// workEvent is one live update from a background job (progress line and/or final result).
type workEvent struct {
	line     string
	footer   bool // if true, only update footer (don't spam transcript)
	final    tea.Msg
	finished bool
}

// workEventMsg is delivered on the Bubble Tea thread from listenWorkEvents.
type workEventMsg struct {
	event workEvent
}

// Pending, human-reviewable artifacts. Nothing is sent to Tarakan until the
// contributor issues an explicit submit command.

type pendingEvidence struct {
	taskID   int64
	evidence string
}

type pendingScan struct {
	host, owner, name, commit string
	model                     string
	runID                     string
	document                  api.ScanDocument
}

// pendingJobReport is a structured Review Format result for a claimed Job.
// Published via /submit-report (same wire path as `tarakan report --job`).
type pendingJobReport struct {
	taskID        int64
	title         string
	model         string
	promptVersion string
	document      api.ScanDocument
}

type pendingVerdict struct {
	host, owner, name string
	scanID            int64
	checks            []findingCheck
}

type findingCheck struct {
	findingID string
	verdict   api.FindingVerdict
}

// Async result messages routed through Update.

type noticeMsg struct {
	body string
	err  error
}

type evidenceReadyMsg struct {
	taskID   int64
	evidence string
	err      error
}

type reviewReadyMsg struct {
	host, owner, name, commit string
	model                     string
	runID                     string
	document                  api.ScanDocument
	raw                       string
	err                       error
}

type jobReportReadyMsg struct {
	taskID        int64
	title         string
	model         string
	promptVersion string
	document      api.ScanDocument
	raw           string
	err           error
}

type verdictReadyMsg struct {
	host, owner, name string
	scanID            int64
	checks            []findingCheck
	raw               string
	err               error
}

// startJobMsg kicks off /report for a job after the TUI mounts (CLI --job).
type startJobMsg struct {
	id int64
}

// startPickupMsg lists open jobs and starts /report on the next claimable one.
type startPickupMsg struct{}

// pickedJobMsg is returned after auto-pickup selects a job id.
type pickedJobMsg struct {
	id    int64
	title string
	err   error
	empty bool
}

func (m Model) executeWorkCommand(cmd command) (tea.Model, tea.Cmd) {
	switch cmd.name {
	case "jobs":
		return m.startWork("Loading review tasks…", m.cmdJobs())
	case "task":
		id, ok := m.requireID(cmd, "/task <id>")
		if !ok {
			return m.done()
		}
		return m.startWork("Loading task…", m.cmdTask(id))
	case "claim", "release":
		id, ok := m.requireID(cmd, "/"+cmd.name+" <id>")
		if !ok {
			return m.done()
		}
		status := map[string]string{"claim": "Claiming task…", "release": "Releasing task…"}[cmd.name]
		return m.startWork(status, m.cmdMutateTask(cmd.name, id))
	case "queue":
		return m.startWork("Loading the review queue…", m.cmdQueue())
	case "scans":
		return m.requireRepo(func(owner, name string) (tea.Model, tea.Cmd) {
			return m.startWork("Loading reviews…", m.cmdScans(owner, name))
		})
	case "report":
		// /report with no id → auto-pick next claimable report job.
		// /report <id> → that job.
		if len(cmd.args) == 0 {
			return m.beginPickup()
		}
		id, ok := m.requireID(cmd, "/report [job id]")
		if !ok {
			return m.done()
		}
		return m.beginReportJob(id)
	case "pickup":
		return m.beginPickup()
	case "submit-report":
		return m.handleSubmitJobReport()
	case "run":
		id, ok := m.requireID(cmd, "/run <id>")
		if !ok {
			return m.done()
		}
		return m.startProgressWork(fmt.Sprintf("Running agent on task #%d…", id), m.runRunTask(id))
	case "submit":
		return m.handleSubmitTask(cmd)
	case "review":
		return m.requireRepo(func(owner, name string) (tea.Model, tea.Cmd) {
			if !m.hasAgent() {
				return m.notice("No agent CLI selected. Use /agent to pick one.")
			}
			return m.startProgressWork("Starting ad-hoc review…", m.runReview(owner, name))
		})
	case "submit-review":
		return m.handleSubmitReview()
	case "verify":
		id, ok := m.requireID(cmd, "/verify <scan id>")
		if !ok {
			return m.done()
		}
		return m.requireRepo(func(owner, name string) (tea.Model, tea.Cmd) {
			if !m.hasAgent() {
				return m.notice("No agent CLI selected. Use /agent to pick one.")
			}
			return m.startProgressWork(fmt.Sprintf("Starting verify for review #%d…", id), m.runVerify(owner, name, id))
		})
	case "submit-verdict":
		return m.handleSubmitVerdict()
	}
	return m.done()
}

func (m Model) beginReportJob(id int64) (tea.Model, tea.Cmd) {
	if m.apiConfig.Token == "" {
		return m.notice("Sign in first with /login.")
	}
	if !m.hasAgent() {
		return m.notice("No agent CLI selected. Use /agent to pick one (claude, codex, grok).")
	}
	return m.startProgressWork(
		fmt.Sprintf("Starting report job #%d…", id),
		m.runReportJob(id),
	)
}

func (m Model) beginPickup() (tea.Model, tea.Cmd) {
	if m.apiConfig.Token == "" {
		return m.notice("Sign in first with /login.")
	}
	if !m.hasAgent() {
		return m.notice("No agent CLI selected. Use /agent to pick one (claude, codex, grok).")
	}
	return m.startProgressWork(
		"Looking for an open report job in the global queue…",
		m.runPickupJob(),
	)
}

// handleWorkEvent applies live progress lines while a background job runs, then
// dispatches the final result when finished.
func (m Model) handleWorkEvent(message workEventMsg) (tea.Model, tea.Cmd) {
	ev := message.event
	if ev.line != "" {
		m.busyStatus = ev.line
		if !ev.footer {
			m.transcript.Append(session.RoleSystem, ev.line)
		}
		m.refreshTranscript()
		m.resize(m.width, m.height)
	}
	if !ev.finished {
		return m, listenWorkEvents(m.workEvents)
	}
	m.workEvents = nil
	m.busyStatus = ""
	if ev.final == nil {
		m.busy = false
		m.updateInputHint()
		m.refreshTranscript()
		return m, nil
	}
	return m.handleWorkMessage(ev.final)
}

func (m Model) handleWorkMessage(message tea.Msg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.busyStatus = ""
	m.workEvents = nil
	switch message := message.(type) {
	case pickedJobMsg:
		if message.err != nil {
			m.transcript.Append(session.RoleSystem, "Error: "+message.err.Error())
			break
		}
		if message.empty {
			m.transcript.Append(session.RoleSystem,
				"No report jobs to work (none open, and no active claim of yours). Use /jobs or /report <id>.")
			break
		}
		m.transcript.Append(session.RoleSystem, fmt.Sprintf(
			"Picked job #%d (%s). Claiming and running agent…", message.id, message.title))
		m.refreshTranscript()
		return m.beginReportJob(message.id)
	case noticeMsg:
		if message.err != nil {
			m.transcript.Append(session.RoleSystem, "Error: "+message.err.Error())
		} else {
			m.transcript.Append(session.RoleSystem, message.body)
		}
	case evidenceReadyMsg:
		if message.err != nil {
			m.transcript.Append(session.RoleSystem, "Error: "+message.err.Error())
		} else {
			m.pendingEvidence = &pendingEvidence{taskID: message.taskID, evidence: message.evidence}
			m.transcript.Append(session.RoleAgent, message.evidence)
			m.transcript.Append(session.RoleSystem, fmt.Sprintf(
				"Agent evidence ready for task %d. Review it above, then:\n  /submit %d <summary>", message.taskID, message.taskID))
		}
	case reviewReadyMsg:
		if message.err != nil {
			body := "Error: " + message.err.Error()
			if strings.TrimSpace(message.raw) != "" {
				body += "\n\nAgent output:\n" + strings.TrimSpace(message.raw)
			}
			m.transcript.Append(session.RoleSystem, body)
		} else {
			m.pendingScan = &pendingScan{host: message.host, owner: message.owner, name: message.name, commit: message.commit, model: message.model, runID: message.runID, document: message.document}
			m.transcript.Append(session.RoleSystem, formatDocument(message.document)+
				"\n\nReview the findings above, then /submit-review to record them (or /review again).")
		}
	case jobReportReadyMsg:
		if message.err != nil {
			body := "Error: " + message.err.Error()
			if strings.TrimSpace(message.raw) != "" {
				body += "\n\nAgent output:\n" + strings.TrimSpace(message.raw)
			}
			m.transcript.Append(session.RoleSystem, body)
		} else {
			m.pendingJobReport = &pendingJobReport{
				taskID:        message.taskID,
				title:         message.title,
				model:         message.model,
				promptVersion: message.promptVersion,
				document:      message.document,
			}
			m.transcript.Append(session.RoleSystem, fmt.Sprintf(
				"Job #%d (%s) - claimed and reviewed.\n%s\n\nReview the findings, then:\n  /submit-report\nto publish the Report and complete the job (or /report %d to re-run).",
				message.taskID, message.title, formatDocument(message.document), message.taskID))
		}
	case verdictReadyMsg:
		if message.err != nil {
			body := "Error: " + message.err.Error()
			if strings.TrimSpace(message.raw) != "" {
				body += "\n\nAgent output:\n" + strings.TrimSpace(message.raw)
			}
			m.transcript.Append(session.RoleSystem, body)
		} else {
			m.pendingVerdict = &pendingVerdict{host: message.host, owner: message.owner, name: message.name, scanID: message.scanID, checks: message.checks}
			var summary strings.Builder
			fmt.Fprintf(&summary, "Proposed %d per-finding check(s) for review %d:\n", len(message.checks), message.scanID)
			for _, check := range message.checks {
				fmt.Fprintf(&summary, "  %s · %s · %s\n", check.findingID, check.verdict.Verdict, check.verdict.Notes)
			}
			summary.WriteString("\n/submit-verdict to record each check.")
			m.transcript.Append(session.RoleSystem, summary.String())
		}
	}
	m.updateInputHint()
	m.refreshTranscript()
	m.resize(m.width, m.height)
	return m, nil
}

// --- command builders (each returns a tea.Cmd running off the UI thread) ---

func (m Model) cmdJobs() tea.Cmd {
	owner, name, ok := m.repoSlug()
	return m.withClient(func(client *api.Client) tea.Msg {
		if !ok {
			return noticeMsg{err: errNoRepo}
		}
		tasks, err := client.ListTasks(context.Background(), owner, name)
		if err != nil {
			return noticeMsg{err: err}
		}
		if len(tasks) == 0 {
			return noticeMsg{body: "No open tasks for " + owner + "/" + name + "."}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Open tasks for %s/%s:\n", owner, name)
		for _, t := range tasks {
			fmt.Fprintf(&b, "  #%d [%s] %s - %s (%s)\n", t.ID, t.Status, t.Kind, t.Title, t.Capability)
		}
		return noticeMsg{body: strings.TrimRight(b.String(), "\n")}
	})
}

func (m Model) cmdTask(id int64) tea.Cmd {
	return m.withClient(func(client *api.Client) tea.Msg {
		t, err := client.GetTask(context.Background(), id)
		if err != nil {
			return noticeMsg{err: err}
		}
		body := fmt.Sprintf("Task #%d - %s\n%s/%s @ %s\nstatus %s · kind %s · capability %s\n\n%s",
			t.ID, t.Title, t.Repository.Owner, t.Repository.Name, shortSHA(t.CommitSHA),
			t.Status, t.Kind, t.Capability, t.Description)
		return noticeMsg{body: body}
	})
}

func (m Model) cmdMutateTask(action string, id int64) tea.Cmd {
	return m.withClient(func(client *api.Client) tea.Msg {
		var t api.Task
		var err error
		if action == "claim" {
			t, err = client.ClaimTask(context.Background(), id)
		} else {
			t, err = client.ReleaseTask(context.Background(), id)
		}
		if err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{body: fmt.Sprintf("Task #%d is now %s.", t.ID, t.Status)}
	})
}

func (m Model) cmdQueue() tea.Cmd {
	return m.withClient(func(client *api.Client) tea.Msg {
		repos, err := client.ListReviewableRepositories(context.Background(), "unscanned")
		if err != nil {
			return noticeMsg{err: err}
		}
		if len(repos) == 0 {
			return noticeMsg{body: "The review queue is empty."}
		}
		var b strings.Builder
		b.WriteString("Repositories awaiting review:\n")
		for _, r := range repos {
			fmt.Fprintf(&b, "  %s (%s)\n", r.Slug(), valueOr(r.PrimaryLanguage, "?"))
		}
		return noticeMsg{body: strings.TrimRight(b.String(), "\n")}
	})
}

func (m Model) cmdScans(owner, name string) tea.Cmd {
	host := m.repository.Host
	return m.withClient(func(client *api.Client) tea.Msg {
		scans, err := client.ListScansForHost(context.Background(), host, owner, name)
		if err != nil {
			return noticeMsg{err: err}
		}
		if len(scans) == 0 {
			return noticeMsg{body: "No visible reviews for " + owner + "/" + name + "."}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "Reviews of %s/%s:\n", owner, name)
		for _, s := range scans {
			state := s.ReviewStatus
			if s.Verified {
				state = "verified/" + state
			}
			fmt.Fprintf(&b, "  #%d by @%s - %d finding(s), %s [%s]\n", s.ID, valueOr(s.Submitter, "?"), s.FindingsCount, state, s.Visibility)
			if s.DetailsVisible {
				for _, f := range s.Findings {
					fmt.Fprintf(&b, "      [%s] %s%s - %s\n", f.Severity, f.File, findingLines(f), f.Title)
				}
			}
		}
		return noticeMsg{body: strings.TrimRight(b.String(), "\n")}
	})
}

func (m Model) runRunTask(id int64) func(report func(string)) tea.Msg {
	root, commit := m.repository.Root, m.repository.Commit
	provider := m.selected
	return m.withClientProgress(func(client *api.Client, report func(string)) tea.Msg {
		report(fmt.Sprintf("Loading task #%d…", id))
		task, err := client.GetTask(context.Background(), id)
		if err != nil {
			return evidenceReadyMsg{taskID: id, err: err}
		}
		if task.Capability != "agent" {
			return evidenceReadyMsg{taskID: id, err: fmt.Errorf("task %d needs %s work; /run only automates agent-capability tasks", id, valueOr(task.Capability, "?"))}
		}
		if len(task.CommitSHA) == 40 {
			commit = task.CommitSHA
		}
		report(fmt.Sprintf("Task #%d · %s · pin %s", id, task.Title, shortSHA(commit)))
		output, err := runAgentInSnapshot(root, commit, provider, taskPrompt(task), report)
		if err != nil {
			return evidenceReadyMsg{taskID: id, err: err}
		}
		if reviewdoc.FindingKinds[task.Kind] {
			doc, parseErr := reviewdoc.Parse(output)
			if parseErr != nil {
				return evidenceReadyMsg{taskID: id, err: parseErr}
			}
			doc, err = reconcileDocumentForHostContext(context.Background(), client, task.Repository.Host, task.Repository.Owner, task.Repository.Name, commit, root, provider, doc, report)
			if err != nil {
				return evidenceReadyMsg{taskID: id, err: err}
			}
			encoded, marshalErr := json.MarshalIndent(doc, "", "  ")
			if marshalErr != nil {
				return evidenceReadyMsg{taskID: id, err: marshalErr}
			}
			output = string(encoded)
		}
		return evidenceReadyMsg{taskID: id, evidence: output}
	})
}

func (m Model) runPickupJob() func(report func(string)) tea.Msg {
	localOwner, localName, _ := m.repoSlug()
	return m.withClientProgress(func(client *api.Client, report func(string)) tea.Msg {
		report("Fetching global job queue…")
		tasks, err := client.ListOpenJobs(context.Background())
		if err != nil {
			return pickedJobMsg{err: fmt.Errorf("global job queue: %w", err)}
		}
		report(fmt.Sprintf("Queue returned %d job(s); picking a report job…", len(tasks)))
		task, found := pickReportJobPreferring(tasks, localOwner, localName)
		if !found {
			return pickedJobMsg{empty: true}
		}
		title := task.Title
		if slug := task.Repository.Slug(); slug != "" {
			title = slug + " · " + title
		}
		if task.Status == "claimed" {
			title = title + " [your claim]"
		}
		return pickedJobMsg{id: task.ID, title: title}
	})
}

// runReportJob claims a Job (if needed), runs the agent for Review Format JSON,
// and returns a pending structured report for human review before publish.
func (m Model) runReportJob(id int64) func(report func(string)) tea.Msg {
	local := m.repository
	provider := m.selected
	modelID := provider.ModelIdentifier()
	return m.withClientProgress(func(client *api.Client, report func(string)) tea.Msg {
		report(fmt.Sprintf("Fetching job #%d…", id))
		task, err := client.GetTask(context.Background(), id)
		if err != nil {
			return jobReportReadyMsg{taskID: id, err: err}
		}
		slug := task.Repository.Slug()
		report(fmt.Sprintf("Job #%d · %s · %s · %s @ %s",
			id, valueOr(slug, "?"), task.Kind, task.Title, shortSHA(task.CommitSHA)))
		if !reviewdoc.FindingKinds[task.Kind] {
			return jobReportReadyMsg{taskID: id, err: fmt.Errorf(
				"job %d kind %q is not a Report job; use /run for prose tasks or tarakan check for verify_findings",
				id, valueOr(task.Kind, "?"))}
		}
		commit := task.CommitSHA
		if len(commit) < 40 {
			return jobReportReadyMsg{taskID: id, err: fmt.Errorf("job %d has no full commit SHA to pin", id)}
		}
		claimedHere := !isMyActiveClaim(task)
		keepClaim := false
		report(fmt.Sprintf("Claiming job #%d…", id))
		// No-op if we already hold the claim; errors if someone else does.
		if _, err := client.ClaimTask(context.Background(), id); err != nil {
			return jobReportReadyMsg{taskID: id, err: fmt.Errorf("claim job: %w", err)}
		}
		defer func() {
			if claimedHere && !keepClaim {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := client.ReleaseTask(ctx, id); err != nil {
					report(fmt.Sprintf("Warning: release failed for job #%d: %v", id, err))
				} else {
					report(fmt.Sprintf("Released job #%d after failed run", id))
				}
			}
		}()
		report(fmt.Sprintf("Claim held on job #%d", id))
		root, cleanup, err := worktreeForTask(local, task, client.BaseURL(), report)
		if err != nil {
			return jobReportReadyMsg{taskID: id, title: task.Title, model: modelID, err: err}
		}
		defer cleanup()

		prompt := reviewdoc.TaskFormatPromptForKind(task.Kind, task.Title, task.Description)
		output, err := runAgentInSnapshot(root, commit, provider, prompt, report)
		if err != nil {
			return jobReportReadyMsg{taskID: id, title: task.Title, model: modelID, raw: output, err: err}
		}
		report("Parsing Review Format from agent output…")
		doc, err := reviewdoc.Parse(output)
		if err != nil {
			return jobReportReadyMsg{taskID: id, title: task.Title, model: modelID, raw: output, err: err}
		}
		doc, err = reconcileDocumentForHostContext(context.Background(), client, task.Repository.Host, task.Repository.Owner, task.Repository.Name, commit, root, provider, doc, report)
		if err != nil {
			return jobReportReadyMsg{taskID: id, title: task.Title, model: modelID, raw: output, err: err}
		}
		report(fmt.Sprintf("Parsed %d finding(s) for job #%d", len(doc.Findings), id))
		keepClaim = true
		return jobReportReadyMsg{
			taskID:        id,
			title:         task.Title,
			model:         modelID,
			promptVersion: "github.com/atomine-elektrine/tarakan-client/v2",
			document:      doc,
		}
	})
}

func (m Model) runReview(owner, name string) func(report func(string)) tea.Msg {
	root, commit, provider := m.repository.Root, m.repository.Commit, m.selected
	host := m.repository.Host
	model := provider.ModelIdentifier()
	return m.withClientProgress(func(client *api.Client, report func(string)) tea.Msg {
		report(fmt.Sprintf("Ad-hoc review of %s/%s @ %s…", owner, name, shortSHA(commit)))
		output, err := runAgentInSnapshot(root, commit, provider, reviewdoc.FormatPrompt, report)
		if err != nil {
			return reviewReadyMsg{host: host, owner: owner, name: name, commit: commit, model: model, raw: output, err: err}
		}
		report("Parsing Scan Format…")
		doc, err := reviewdoc.Parse(output)
		if err != nil {
			return reviewReadyMsg{host: host, owner: owner, name: name, commit: commit, model: model, raw: output, err: err}
		}
		doc, err = reconcileDocumentForHostContext(context.Background(), client, host, owner, name, commit, root, provider, doc, report)
		if err != nil {
			return reviewReadyMsg{host: host, owner: owner, name: name, commit: commit, model: model, raw: output, err: err}
		}
		runID, err := api.NewRunID()
		if err != nil {
			return reviewReadyMsg{host: host, owner: owner, name: name, commit: commit, model: model, err: err}
		}
		return reviewReadyMsg{host: host, owner: owner, name: name, commit: commit, model: model, runID: runID, document: doc}
	})
}

func (m Model) runVerify(owner, name string, scanID int64) func(report func(string)) tea.Msg {
	root, commit, provider := m.repository.Root, m.repository.Commit, m.selected
	host := m.repository.Host
	return m.withClientProgress(func(client *api.Client, report func(string)) tea.Msg {
		report(fmt.Sprintf("Loading review #%d on %s/%s…", scanID, owner, name))
		scans, err := client.ListScansForHost(context.Background(), host, owner, name)
		if err != nil {
			return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, err: err}
		}
		var target *api.Scan
		for i := range scans {
			if scans[i].ID == scanID {
				target = &scans[i]
			}
		}
		if target == nil {
			return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, err: fmt.Errorf("review %d is not visible here (need a reviews:read reviewer-tier token)", scanID)}
		}
		if !target.DetailsVisible || len(target.Findings) == 0 {
			return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, err: fmt.Errorf("review %d has no visible findings to verify", scanID)}
		}
		for _, finding := range target.Findings {
			if finding.CanonicalFindingID == "" {
				return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, err: fmt.Errorf("review %d has not been assimilated into canonical finding memory", scanID)}
			}
		}
		if len(target.CommitSHA) == 40 {
			commit = target.CommitSHA
		}
		report(fmt.Sprintf("Verifying review #%d (%d finding(s)) @ %s…", scanID, len(target.Findings), shortSHA(commit)))
		output, err := runAgentInSnapshot(root, commit, provider, verifyPrompt(*target), report)
		if err != nil {
			return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, raw: output, err: err}
		}
		report("Parsing verdict…")
		checks, err := parseFindingChecks(output, commit)
		if err != nil {
			return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, raw: output, err: err}
		}
		return verdictReadyMsg{host: host, owner: owner, name: name, scanID: scanID, checks: checks}
	})
}

// --- submit handlers (confirm-then-send, using stored pending state) ---

func (m Model) handleSubmitTask(cmd command) (tea.Model, tea.Cmd) {
	// Prefer structured job report if pending for this id (after /report).
	if len(cmd.args) >= 1 {
		if id, err := strconv.ParseInt(cmd.args[0], 10, 64); err == nil {
			if m.pendingJobReport != nil && m.pendingJobReport.taskID == id {
				return m.handleSubmitJobReport()
			}
		}
	}
	if len(cmd.args) < 2 {
		return m.notice("Usage: /submit <id> <summary text>  (or /submit-report after /report)")
	}
	id, err := strconv.ParseInt(cmd.args[0], 10, 64)
	if err != nil {
		return m.notice("First argument must be a task id.")
	}
	if m.pendingEvidence == nil || m.pendingEvidence.taskID != id {
		return m.notice(fmt.Sprintf("No pending agent evidence for task %d. Run /run %d or /report %d first.", id, id, id))
	}
	summary := strings.Join(cmd.args[1:], " ")
	evidence := m.pendingEvidence.evidence
	m.pendingEvidence = nil
	return m.startWork("Submitting contribution…", m.withClient(func(client *api.Client) tea.Msg {
		t, err := client.SubmitTask(context.Background(), id, api.Submission{Provenance: "agent", Summary: summary, Evidence: evidence})
		if err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{body: fmt.Sprintf("Submitted task #%d for independent review (status %s).", t.ID, t.Status)}
	}))
}

func (m Model) handleSubmitJobReport() (tea.Model, tea.Cmd) {
	if m.pendingJobReport == nil {
		return m.notice("No pending job report. Run /report <job id> first.")
	}
	p := *m.pendingJobReport
	m.pendingJobReport = nil
	doc := p.document
	summary := reviewdoc.SummaryFromDocument(doc, 2_000)
	return m.startWork("Publishing Report and completing job…", m.withClient(func(client *api.Client) tea.Msg {
		t, err := client.SubmitTask(context.Background(), p.taskID, api.Submission{
			// Agent-produced Review Format via /report; human only gates publish.
			// "hybrid" would mean substantial human rewrite of the findings.
			Provenance:    "agent",
			Summary:       summary,
			Model:         p.model,
			PromptVersion: p.promptVersion,
			Document:      &doc,
		})
		if err != nil {
			return noticeMsg{err: err}
		}
		body := fmt.Sprintf("Published Report via Job #%d (status %s).", t.ID, t.Status)
		if t.LinkedReview != nil {
			body = fmt.Sprintf("Published Report #%d (%d findings, %s) via Job #%d.",
				t.LinkedReview.ID, t.LinkedReview.FindingsCount, t.LinkedReview.ReviewStatus, t.ID)
		}
		return noticeMsg{body: body}
	}))
}

func (m Model) handleSubmitReview() (tea.Model, tea.Cmd) {
	if m.pendingScan == nil {
		return m.notice("No pending review. Run /review first.")
	}
	p := *m.pendingScan
	m.pendingScan = nil
	return m.startWork("Submitting review…", m.withClient(func(client *api.Client) tea.Msg {
		scan, err := client.SubmitScanForHost(context.Background(), p.host, p.owner, p.name, api.ScanSubmission{
			CommitSHA: p.commit, Provenance: "agent", ReviewKind: "code_review",
			Model: p.model, PromptVersion: "github.com/atomine-elektrine/tarakan-client/v2", RunID: p.runID, Document: p.document,
		})
		if err != nil {
			return noticeMsg{err: err}
		}
		return noticeMsg{body: fmt.Sprintf("Submitted review #%d: %d finding(s), %s.", scan.ID, scan.FindingsCount, scan.ReviewStatus)}
	}))
}

func reconcileDocumentForHostContext(
	ctx context.Context,
	client *api.Client,
	host, owner, name, commit, root string,
	provider agent.Provider,
	discovery api.ScanDocument,
	report func(string),
) (api.ScanDocument, error) {
	memory, err := client.GetRepositoryMemoryForHost(ctx, host, owner, name, commit)
	if err != nil {
		return api.ScanDocument{}, fmt.Errorf("load repository memory: %w", err)
	}
	if len(memory.Findings) == 0 || len(discovery.Findings) == 0 {
		return discovery, nil
	}
	report(fmt.Sprintf("Reconciling %d finding(s) against %d canonical issue(s)…", len(discovery.Findings), len(memory.Findings)))
	output, err := runAgentInSnapshotContext(
		ctx,
		root,
		commit,
		provider,
		reviewdoc.ReconciliationPrompt(memory, discovery),
		report,
	)
	if err != nil {
		return api.ScanDocument{}, err
	}
	return reviewdoc.Parse(output)
}

func (m Model) handleSubmitVerdict() (tea.Model, tea.Cmd) {
	if m.pendingVerdict == nil {
		return m.notice("No pending verdict. Run /verify <scan id> first.")
	}
	p := *m.pendingVerdict
	m.pendingVerdict = nil
	return m.startWork("Submitting per-finding checks…", m.withClient(func(client *api.Client) tea.Msg {
		for _, check := range p.checks {
			check.verdict.Provenance = "agent"
			if err := client.SubmitFindingVerdictForHost(context.Background(), p.host, p.owner, p.name, check.findingID, check.verdict); err != nil {
				return noticeMsg{err: fmt.Errorf("check finding %s: %w", check.findingID, err)}
			}
		}
		return noticeMsg{body: fmt.Sprintf("Recorded %d agent check(s) from review #%d. Agent-only checks add corroboration but do not create verification quorum.",
			len(p.checks), p.scanID)}
	}))
}

// --- small helpers ---

func (m Model) startWork(status string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.busy = true
	m.busyStatus = status
	m.transcript.Append(session.RoleSystem, status)
	m.refreshTranscript()
	m.resize(m.width, m.height)
	return m, cmd
}

// startProgressWork runs work on a background goroutine and streams status
// lines into the transcript/footer until a final result message arrives.
func (m Model) startProgressWork(initial string, run func(report func(string)) tea.Msg) (tea.Model, tea.Cmd) {
	m.busy = true
	m.busyStatus = initial
	m.transcript.Append(session.RoleSystem, initial)
	m.refreshTranscript()
	m.resize(m.width, m.height)

	// Large buffer so rapid tool events (many greps) are not dropped while the
	// Bubble Tea loop catches up - dropping made the TUI look "stuck".
	ch := make(chan workEvent, 512)
	m.workEvents = ch

	go func() {
		var lastFooter string
		report := func(line string) {
			line = strings.TrimSpace(line)
			if line == "" {
				return
			}
			// Agent stream lines only refresh the footer; major steps go to transcript.
			footerOnly := isAgentStreamLine(line)
			if footerOnly {
				if line == lastFooter {
					return
				}
				lastFooter = line
			}
			// Prefer blocking briefly over silent drops so activity keeps flowing.
			select {
			case ch <- workEvent{line: line, footer: footerOnly}:
			case <-time.After(2 * time.Second):
				// UI stalled; drop this line but keep the agent running.
			}
		}
		final := run(report)
		// Final must never be dropped.
		ch <- workEvent{finished: true, final: final}
		close(ch)
	}()

	return m, listenWorkEvents(ch)
}

// isAgentStreamLine is true for high-frequency chatter that should only update
// the footer (not flood the transcript). Tool/subagent lines like "→ Read …"
// return false so they appear in the log.
func isAgentStreamLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	// Live tool / subagent activity from Grok session stream → transcript.
	if strings.HasPrefix(trimmed, "→ ") || strings.HasPrefix(trimmed, "✓ ") || strings.HasPrefix(trimmed, "✗ ") {
		return false
	}
	// Token-level thinking / writing pulse.
	if strings.HasPrefix(trimmed, "…") || strings.HasPrefix(trimmed, "...") {
		return true
	}
	// Legacy CLI stderr prefixes.
	for _, prefix := range []string{
		"Grok Build:", "Claude Code:", "OpenAI Codex:",
		"Ollama:", "OpenRouter:",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	if i := strings.Index(line, ": "); i > 0 && i < 40 {
		rest := line[i+2:]
		if strings.HasPrefix(rest, "packing ") || strings.HasPrefix(rest, "calling ") || rest == "… (working)" {
			return true
		}
	}
	return false
}

func listenWorkEvents(ch <-chan workEvent) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return workEventMsg{event: workEvent{finished: true}}
		}
		return workEventMsg{event: ev}
	}
}

// withClientProgress is like withClient but passes a progress reporter into the work body.
func (m Model) withClientProgress(fn func(*api.Client, func(string)) tea.Msg) func(report func(string)) tea.Msg {
	cfg := m.apiConfig
	return func(report func(string)) tea.Msg {
		if report == nil {
			report = func(string) {}
		}
		client, err := cfg.Client()
		if err != nil {
			return noticeMsg{err: fmt.Errorf("%w - use /token and /url (or --token / --url)", err)}
		}
		return fn(client, report)
	}
}

func (m Model) notice(text string) (tea.Model, tea.Cmd) {
	m.transcript.Append(session.RoleSystem, text)
	m.refreshTranscript()
	return m, nil
}

func (m Model) done() (tea.Model, tea.Cmd) {
	m.refreshTranscript()
	return m, nil
}

func (m Model) requireID(cmd command, usage string) (int64, bool) {
	if len(cmd.args) == 0 {
		m.transcript.Append(session.RoleSystem, "Usage: "+usage)
		return 0, false
	}
	id, err := strconv.ParseInt(cmd.args[0], 10, 64)
	if err != nil {
		m.transcript.Append(session.RoleSystem, "Usage: "+usage)
		return 0, false
	}
	return id, true
}

func (m Model) requireRepo(fn func(owner, name string) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	owner, name, ok := m.repoSlug()
	if !ok {
		return m.notice("The current directory has no git remote origin (owner/name). Set origin or cd into the job's clone.")
	}
	return fn(owner, name)
}

func (m Model) repoSlug() (string, string, bool) {
	if owner, name, ok := m.repository.RemoteSlug(); ok {
		return owner, name, true
	}
	if m.repository.GitHubOwner != "" && m.repository.GitHubName != "" {
		return m.repository.GitHubOwner, m.repository.GitHubName, true
	}
	return "", "", false
}

func (m Model) hasAgent() bool { return m.selected.Name != "" }

func (m Model) withClient(fn func(*api.Client) tea.Msg) tea.Cmd {
	cfg := m.apiConfig
	return func() tea.Msg {
		client, err := cfg.Client()
		if err != nil {
			return noticeMsg{err: fmt.Errorf("%w - use /token and /url (or --token / --url)", err)}
		}
		return fn(client)
	}
}

func runAgentInSnapshot(root, commit string, provider agent.Provider, prompt string, report func(string)) (string, error) {
	return runAgentInSnapshotContext(context.Background(), root, commit, provider, prompt, report)
}

func runAgentInSnapshotContext(ctx context.Context, root, commit string, provider agent.Provider, prompt string, report func(string)) (string, error) {
	if report == nil {
		report = func(string) {}
	}
	if provider.Name == "" {
		return "", fmt.Errorf("no agent CLI selected")
	}
	if commit == "" {
		return "", fmt.Errorf("the current repository has no commit to pin a review to")
	}
	report("Preparing isolated snapshot @ " + shortSHA(commit) + "…")
	pinned, err := snapshot.Create(root, commit)
	if err != nil {
		return "", fmt.Errorf("prepare pinned snapshot: %w", err)
	}
	defer pinned.Close()
	report("Snapshot ready. Running " + provider.Description + " (this can take a while)…")

	started := time.Now()
	output, err := agent.Run(ctx, provider, agent.Request{
		Prompt:    prompt,
		Directory: pinned.Root,
		Progress:  report,
	})
	elapsed := time.Since(started).Round(time.Second)
	if err != nil {
		report(provider.Description + " failed after " + elapsed.String())
		return output, err
	}
	report(provider.Description + " finished in " + elapsed.String())
	if changed, changeErr := pinned.Changed(); changeErr != nil {
		return output, fmt.Errorf("snapshot could not be verified after the run: %w", changeErr)
	} else if changed {
		return output, fmt.Errorf("refusing output because the agent modified its read-only snapshot")
	}
	report("Snapshot unchanged (agent stayed read-only)")
	return output, nil
}
