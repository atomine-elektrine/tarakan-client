package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"tarakan-client/internal/agent"
	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
	"tarakan-client/internal/reviewdoc"
	"tarakan-client/internal/snapshot"
)

// runReport is the mass-facing path: run a local agent, produce Review Format
// findings, and publish a Report (optionally completing a Job).
//
//	tarakan report --agent grok
//	tarakan report --agent grok --job 42
//	tarakan report --document-file findings.json   # publish only
func runReport(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, cfg api.Config) int {
	flags := flag.NewFlagSet("report", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var (
		agentName     string
		model         string
		jobID         int64
		documentFile  string
		kind          string
		promptVersion string
		yes           bool
		interactive   bool
		outputFile    string
		urlFlag       string
		hostFlag      string
		tokenFlag     string
	)
	flags.StringVar(&agentName, "agent", "", "local agent: claude, codex, or grok (required unless --document-file)")
	flags.StringVar(&model, "model", "", "model label stored on the report (defaults to --agent)")
	flags.Int64Var(&jobID, "job", 0, "optional Job/Request ID to claim and complete")
	flags.StringVar(&documentFile, "document-file", "", "publish an existing Review Format JSON file (skip agent run)")
	flags.StringVar(&kind, "kind", "code_review", "report kind: code_review, threat_model, privacy_review, business_logic")
	flags.StringVar(&promptVersion, "prompt-version", "tarakan-report/v2", "prompt version label")
	flags.StringVar(&outputFile, "output", "", "also write findings JSON to this path")
	var pickup bool
	flags.BoolVar(&yes, "yes", false, "publish without an interactive confirmation prompt")
	flags.BoolVar(&interactive, "interactive", false, "open the TUI (with --job, or next open job if omitted)")
	flags.BoolVar(&pickup, "pickup", false, "open the TUI, claim next open job from the global queue, run agent")
	addAPIFlags(flags, &urlFlag, &hostFlag, &tokenFlag)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tarakan report --token TOKEN --agent grok --pickup")
		fmt.Fprintln(stderr, "       tarakan report [--url URL] [--token TOKEN] [--agent …] [--job ID] [--yes]")
		fmt.Fprintln(stderr, "       tarakan report --document-file findings.json [--job ID] [--yes]")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Mass path: run a local AI agent, produce findings, publish a Report.")
		fmt.Fprintln(stderr, "Auth: --url/--host and --token (or TARAKAN_URL / TARAKAN_API_TOKEN).")
		fmt.Fprintln(stderr, "With --job, claims and completes that Job so it links to the Report.")
		fmt.Fprintln(stderr, "With --pickup (or --interactive without --job), opens the TUI and")
		fmt.Fprintln(stderr, "auto-claims the next open report job from the global queue.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	var err error
	cfg, err = mergeFlagConfig(cfg, urlFlag, hostFlag, tokenFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if interactive || pickup {
		if yes {
			fmt.Fprintln(stderr, "use either --interactive/--pickup or --yes, not both")
			return 2
		}
		if documentFile != "" {
			fmt.Fprintln(stderr, "--interactive/--pickup cannot be combined with --document-file")
			return 2
		}
		// --interactive with no --job means auto-pickup; --pickup always does.
		autoPickup := pickup || jobID <= 0
		if interactive && jobID > 0 {
			autoPickup = false
		}
		return runInteractiveJob(agentName, model, jobID, autoPickup, cfg, stderr)
	}

	client, err := cfg.Client()
	if err != nil {
		return printAPIConfigurationError(stderr, err)
	}

	repository, err := repoctx.Current()
	if err != nil {
		fmt.Fprintf(stderr, "discover repository: %v\n", err)
		return 1
	}
	owner, name, ok := repository.RemoteSlug()
	if !ok {
		owner, name = repository.GitHubOwner, repository.GitHubName
		ok = owner != "" && name != ""
	}
	if !ok {
		if o, n, err := repositoryFromFlagOrContext(""); err == nil {
			owner, name = o, n
		} else {
			fmt.Fprintln(stderr, "current directory has no git remote origin; run inside a registered repo clone, or use --pickup / --job with the TUI to auto-clone")
			return 1
		}
	}

	var doc api.ScanDocument
	var commitSHA string
	var usedAgent string
	repositoryHost := repository.Host

	if documentFile != "" {
		raw, err := readEvidence(documentFile, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "read document: %v\n", err)
			return 1
		}
		doc, err = reviewdoc.Parse(raw)
		if err != nil {
			fmt.Fprintf(stderr, "parse Review Format: %v\n", err)
			return 2
		}
		commitSHA = repository.CommitSHA
		if jobID != 0 {
			task, err := client.GetTask(ctx, jobID)
			if err != nil {
				fmt.Fprintf(stderr, "get job: %v\n", err)
				return 1
			}
			commitSHA = task.CommitSHA
			repositoryHost = task.Repository.Host
		}
		if commitSHA == "" || len(commitSHA) < 40 {
			fmt.Fprintln(stderr, "need a full 40-character commit SHA (git HEAD or job pin)")
			return 1
		}
		usedAgent = strings.TrimSpace(model)
		if usedAgent == "" {
			usedAgent = "manual"
		}
	} else {
		if agentName == "" {
			fmt.Fprintln(stderr, "--agent is required unless --document-file is set")
			flags.Usage()
			return 2
		}
		registry := agent.Detect()
		provider, found := registry.Find(agentName)
		if !found {
			fmt.Fprintf(stderr, "agent %q is not installed; try tarakan --agents\n", agentName)
			return 1
		}
		usedAgent = strings.TrimSpace(model)
		if usedAgent == "" {
			usedAgent = provider.Name
		}

		var workDir string
		if jobID != 0 {
			task, err := client.GetTask(ctx, jobID)
			if err != nil {
				fmt.Fprintf(stderr, "get job: %v\n", err)
				return 1
			}
			if err := validateTaskRepository(task, repository); err != nil {
				fmt.Fprintf(stderr, "refusing job: %v\n", err)
				return 1
			}
			if !reviewdoc.FindingKinds[task.Kind] && task.Kind != "" {
				fmt.Fprintf(stderr, "job %d kind %q is not a Report job (use tarakan check for verify_findings)\n", jobID, task.Kind)
				return 1
			}
			commitSHA = task.CommitSHA
			claimWasInactive := task.Lease == nil || !task.Lease.Active
			if _, err := client.ClaimTask(ctx, jobID); err != nil {
				fmt.Fprintf(stderr, "claim job: %v\n", err)
				return 1
			}
			fmt.Fprintf(stderr, "Claimed job %d. Preparing isolated snapshot of %s…\n", jobID, shortSHA(commitSHA))
			pinned, err := snapshot.Create(repository.Root, commitSHA)
			if err != nil {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintf(stderr, "snapshot failed (absolute symlinks or missing commit?): %v\n", err)
				fmt.Fprintln(stderr, "Tip: fix external symlinks, or run: tarakan report --document-file FILE --job ID")
				return 1
			}
			defer pinned.Close()
			workDir = pinned.Root
			prompt := reviewdoc.TaskFormatPromptForKind(task.Kind, task.Title, task.Description)
			output, err := agent.Run(ctx, provider, agent.Request{Prompt: prompt, Directory: workDir})
			if err != nil {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintf(stderr, "agent failed: %v\n", err)
				return 1
			}
			if changed, cerr := pinned.Changed(); cerr != nil || changed {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintln(stderr, "refusing output: agent modified the read-only snapshot")
				return 1
			}
			doc, err = reviewdoc.Parse(sanitizeTerminalOutput(output))
			if err != nil {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintf(stderr, "agent did not return Review Format JSON: %v\n", err)
				return 1
			}
			doc, err = reconcileReport(ctx, client, provider, repositoryHost, owner, name, commitSHA, workDir, doc, stderr)
			if err != nil {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintf(stderr, "reconcile repository memory: %v\n", err)
				return 1
			}
			if changed, cerr := pinned.Changed(); cerr != nil || changed {
				if claimWasInactive {
					releaseClaimAfterFailure(client, jobID, stderr)
				}
				fmt.Fprintln(stderr, "refusing reconciled output: agent modified the read-only snapshot")
				return 1
			}
		} else {
			commitSHA = repository.CommitSHA
			if len(commitSHA) < 40 {
				fmt.Fprintln(stderr, "need a full commit SHA at HEAD")
				return 1
			}
			pinned, err := snapshot.Create(repository.Root, commitSHA)
			if err != nil {
				fmt.Fprintf(stderr, "prepare isolated snapshot: %v\n", err)
				return 1
			}
			defer pinned.Close()
			workDir = pinned.Root
			fmt.Fprintf(stderr, "Running %s on %s @ %s (isolated snapshot)…\n", provider.Description, owner+"/"+name, shortSHA(commitSHA))
			output, err := agent.Run(ctx, provider, agent.Request{
				Prompt:    reviewdoc.FormatPrompt,
				Directory: workDir,
			})
			if err != nil {
				fmt.Fprintf(stderr, "agent failed: %v\n", err)
				return 1
			}
			doc, err = reviewdoc.Parse(sanitizeTerminalOutput(output))
			if err != nil {
				fmt.Fprintf(stderr, "agent did not return Review Format JSON: %v\n", err)
				return 1
			}
			doc, err = reconcileReport(ctx, client, provider, repositoryHost, owner, name, commitSHA, workDir, doc, stderr)
			if err != nil {
				fmt.Fprintf(stderr, "reconcile repository memory: %v\n", err)
				return 1
			}
			if changed, changeErr := pinned.Changed(); changeErr != nil {
				fmt.Fprintf(stderr, "verify snapshot: %v\n", changeErr)
				return 1
			} else if changed {
				fmt.Fprintln(stderr, "refusing output: agent modified the read-only snapshot")
				return 1
			}
		}
	}

	if outputFile != "" {
		encoded, _ := json.MarshalIndent(doc, "", "  ")
		if err := os.WriteFile(outputFile, append(encoded, '\n'), 0o600); err != nil {
			fmt.Fprintf(stderr, "write --output: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "Wrote findings to %s\n", outputFile)
	}

	fmt.Fprintf(stderr, "Report preview: %d finding(s)\n", len(doc.Findings))
	for i, f := range doc.Findings {
		if i >= 5 {
			fmt.Fprintf(stderr, "  … and %d more\n", len(doc.Findings)-5)
			break
		}
		fmt.Fprintf(stderr, "  [%s] %s: %s\n", f.Severity, f.File, f.Title)
	}

	if !yes {
		fmt.Fprint(stderr, "Publish this Report to Tarakan? [y/N] ")
		var answer string
		fmt.Fscanln(stdin, &answer)
		if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
			fmt.Fprintln(stderr, "Aborted. Nothing published.")
			return 0
		}
	}

	summary := reviewdoc.SummaryFromDocument(doc, 2_000)
	if jobID != 0 {
		task, err := client.SubmitTask(ctx, jobID, api.Submission{
			Provenance:    "agent",
			Summary:       summary,
			Model:         usedAgent,
			PromptVersion: promptVersion,
			Document:      &doc,
		})
		if err != nil {
			fmt.Fprintf(stderr, "publish via job: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "Published Report")
		if task.LinkedReview != nil {
			fmt.Fprintf(stderr, " #%d (%d findings, %s)", task.LinkedReview.ID, task.LinkedReview.FindingsCount, task.LinkedReview.ReviewStatus)
		}
		fmt.Fprintf(stderr, " via Job %d.\n", jobID)
		return writeJSON(stdout, stderr, task)
	}

	// Ad-hoc report (no job)
	if len(commitSHA) > 40 {
		commitSHA = commitSHA[:40]
	}
	// Ensure full sha if short
	if len(commitSHA) < 40 {
		fmt.Fprintln(stderr, "commit SHA must be 40 characters for ad-hoc publish")
		return 1
	}

	runID, err := api.NewRunID()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	scan, err := client.SubmitScanForHost(ctx, repositoryHost, owner, name, api.ScanSubmission{
		CommitSHA:     strings.ToLower(commitSHA),
		Provenance:    "agent",
		ReviewKind:    kind,
		Model:         usedAgent,
		PromptVersion: promptVersion,
		RunID:         runID,
		Document:      doc,
		// Notes via document path - ScanSubmission may not have Notes; check type
	})
	if err != nil {
		// Try with notes if API supports embedding in document only
		fmt.Fprintf(stderr, "publish report: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "Published Report #%d with %d finding(s) (status %s).\n", scan.ID, scan.FindingsCount, scan.ReviewStatus)
	return writeJSON(stdout, stderr, scan)
}

func reconcileReport(
	ctx context.Context,
	client *api.Client,
	provider agent.Provider,
	host, owner, name, commitSHA, directory string,
	discovery api.ScanDocument,
	stderr io.Writer,
) (api.ScanDocument, error) {
	memory, err := client.GetRepositoryMemoryForHost(ctx, host, owner, name, commitSHA)
	if err != nil {
		return api.ScanDocument{}, err
	}
	if len(memory.Findings) == 0 || len(discovery.Findings) == 0 {
		return discovery, nil
	}

	fmt.Fprintf(stderr, "Reconciling %d independent finding(s) against %d canonical finding(s)…\n",
		len(discovery.Findings), len(memory.Findings))
	output, err := agent.Run(ctx, provider, agent.Request{
		Prompt:    reviewdoc.ReconciliationPrompt(memory, discovery),
		Directory: directory,
	})
	if err != nil {
		return api.ScanDocument{}, err
	}
	return reviewdoc.Parse(sanitizeTerminalOutput(output))
}

// runCheck records an independent Check (confirm/dispute) on a Report.
//
//	tarakan check 17 --verdict confirmed --notes "…"
//	tarakan check 17 --job 5 --verdict disputed --notes "…"
func runCheck(ctx context.Context, arguments []string, stdin io.Reader, stdout, stderr io.Writer, cfg api.Config) int {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var verdict, notes, provenance, evidenceFile string
	var jobID int64
	var urlFlag, hostFlag, tokenFlag string
	flags.StringVar(&verdict, "verdict", "", "confirmed or disputed (required)")
	flags.StringVar(&notes, "notes", "", "rationale, ≥20 characters (required)")
	flags.StringVar(&provenance, "provenance", "human", "human, agent, or hybrid")
	flags.StringVar(&evidenceFile, "evidence-file", "", "optional PoC / evidence file")
	flags.Int64Var(&jobID, "job", 0, "optional Check Job ID to complete (verify_findings)")
	addAPIFlags(flags, &urlFlag, &hostFlag, &tokenFlag)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: tarakan check REPORT_ID --verdict confirmed|disputed --notes TEXT [--token TOKEN]")
		fmt.Fprintln(stderr, "       tarakan check REPORT_ID --job JOB_ID --verdict confirmed --notes TEXT")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Mass path: independently confirm or dispute a published Report.")
		flags.PrintDefaults()
	}
	// The documented mass-facing form puts REPORT_ID first. Go's flag package
	// stops at the first positional argument, so temporarily remove that ID and
	// parse the remaining flags before restoring it.
	var leadingReportID string
	if len(arguments) > 0 && !strings.HasPrefix(arguments[0], "-") {
		leadingReportID = arguments[0]
		arguments = arguments[1:]
	}
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	positionals := flags.Args()
	if leadingReportID != "" {
		positionals = append([]string{leadingReportID}, positionals...)
	}
	if len(positionals) != 1 {
		flags.Usage()
		return 2
	}
	reportID, err := strconv.ParseInt(positionals[0], 10, 64)
	if err != nil || reportID <= 0 {
		fmt.Fprintln(stderr, "REPORT_ID must be a positive integer")
		return 2
	}
	verdict = strings.ToLower(strings.TrimSpace(verdict))
	if verdict != "confirmed" && verdict != "disputed" {
		fmt.Fprintln(stderr, "--verdict must be confirmed or disputed")
		return 2
	}
	notes = strings.TrimSpace(notes)
	if utf8.RuneCountInString(notes) < 20 {
		fmt.Fprintln(stderr, "--notes must be at least 20 characters")
		return 2
	}
	provenance = strings.ToLower(strings.TrimSpace(provenance))
	if provenance != "human" && provenance != "agent" && provenance != "hybrid" {
		fmt.Fprintln(stderr, "--provenance must be human, agent, or hybrid")
		return 2
	}

	var evidence string
	if evidenceFile != "" {
		evidence, err = readEvidence(evidenceFile, stdin)
		if err != nil {
			fmt.Fprintf(stderr, "read evidence: %v\n", err)
			return 1
		}
	}

	cfg, err = mergeFlagConfig(cfg, urlFlag, hostFlag, tokenFlag)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	client, err := cfg.Client()
	if err != nil {
		return printAPIConfigurationError(stderr, err)
	}

	if jobID != 0 {
		if _, err := client.ClaimTask(ctx, jobID); err != nil {
			// may already be claimed by us
			fmt.Fprintf(stderr, "note: claim: %v\n", err)
		}
		task, err := client.SubmitTask(ctx, jobID, api.Submission{
			Provenance: provenance,
			Verdict:    verdict,
			Notes:      notes,
			Summary:    notes,
			Evidence:   evidence,
		})
		if err != nil {
			fmt.Fprintf(stderr, "check via job: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "Recorded Check on Report via Job %d (verdict=%s).\n", jobID, verdict)
		return writeJSON(stdout, stderr, task)
	}

	repository, err := repoctx.Current()
	if err != nil {
		fmt.Fprintf(stderr, "discover repository: %v\n", err)
		return 1
	}
	owner, name, ok := repository.RemoteSlug()
	if !ok {
		owner, name = repository.GitHubOwner, repository.GitHubName
		ok = owner != "" && name != ""
	}
	if !ok {
		fmt.Fprintln(stderr, "current directory has no git remote origin (owner/name)")
		return 1
	}

	scan, err := client.SubmitVerdictForHost(ctx, repository.Host, owner, name, reportID, api.Verdict{
		Verdict:    verdict,
		Provenance: provenance,
		Notes:      notes,
		Evidence:   evidence,
	})
	if err != nil {
		fmt.Fprintf(stderr, "check report: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "Recorded Check on Report #%d (verdict=%s).\n", reportID, verdict)
	return writeJSON(stdout, stderr, scan)
}

func shortSHA(sha string) string {
	if len(sha) >= 12 {
		return sha[:12]
	}
	return sha
}
