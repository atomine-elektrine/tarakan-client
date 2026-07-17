package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
	"github.com/atomine-elektrine/tarakan-client/internal/reviewdoc"
)

var errNoRepo = errors.New("the current directory has no git remote origin (owner/name)")

func verifyPrompt(scan api.Scan) string {
	var b strings.Builder
	b.WriteString(`You are independently verifying another reviewer's finding(s) against the
repository in the current directory. Do not modify any files.

Reproduce each finding independently. Return one verdict per canonical finding:
"confirmed" if real and reproducible, "disputed" if wrong or not exploitable,
or "fixed" if the issue is no longer present at the pinned commit.

Output ONLY a single JSON object and nothing else:

{"checks": [{
 "finding_id": "canonical UUID supplied below",
 "verdict": "confirmed|disputed|fixed",
 "notes": "short factual rationale (20-2000 chars)",
 "poc": "a concrete proof of concept, exact trace, or counter-evidence"
}]}

Findings under verification:
`)
	for _, f := range scan.Findings {
		fmt.Fprintf(&b, "- id=%s [%s] %s%s: %s\n  %s\n", f.CanonicalFindingID, f.Severity, f.File, findingLines(f), f.Title, f.Description)
	}
	return b.String()
}

func taskPrompt(task api.Task) string {
	// Finding-producing Requests must emit Review Format so complete creates Findings.
	switch task.Kind {
	case "code_review", "threat_model", "privacy_review", "business_logic":
		return reviewdoc.TaskFormatPromptForKind(task.Kind, task.Title, task.Description)
	case "write_fix":
		return fixPrompt(task)
	}
	var b strings.Builder
	b.WriteString("You are performing a read-only security review task. Do not modify any files.\n\n")
	fmt.Fprintf(&b, "Task: %s\n", task.Title)
	if strings.TrimSpace(task.Description) != "" {
		fmt.Fprintf(&b, "\n%s\n", task.Description)
	}
	b.WriteString("\nProvide your findings and reasoning as evidence. Cite file:line where relevant.")
	return b.String()
}

func fixPrompt(task api.Task) string {
	return fmt.Sprintf(`You are preparing a safe patch for a Tarakan fix job against the repository
in the current directory. Work read-only: do not modify files, install dependencies,
commit, push, or contact external services.

Inspect the exact pinned source and produce a minimal unified diff that addresses the
requested defect without unrelated cleanup. Include tests that would fail before the
patch and pass after it. If a safe concrete patch cannot be produced, return an error
explanation instead of inventing code.

Output ONLY one JSON object and nothing else:

{"summary":"what the patch fixes and why", "patch":"diff --git ...", "tests":"exact test plan and commands"}

Job title: %s

Job description:
%s`, task.Title, task.Description)
}

func parseFixArtifact(output string) (string, string, error) {
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		return "", "", errors.New("agent did not return a fix JSON object")
	}
	var artifact struct {
		Summary string `json:"summary"`
		Patch   string `json:"patch"`
		Tests   string `json:"tests"`
	}
	if err := json.Unmarshal([]byte(raw), &artifact); err != nil {
		return "", "", fmt.Errorf("agent output was not valid fix JSON: %w", err)
	}
	artifact.Summary = strings.TrimSpace(artifact.Summary)
	artifact.Patch = strings.TrimSpace(artifact.Patch)
	artifact.Tests = strings.TrimSpace(artifact.Tests)
	if artifact.Summary == "" {
		return "", "", errors.New("fix summary is blank")
	}
	if !strings.HasPrefix(artifact.Patch, "diff --git ") {
		return "", "", errors.New("fix patch must be a unified git diff")
	}
	if artifact.Tests == "" {
		return "", "", errors.New("fix test plan is blank")
	}
	summary := truncate(artifact.Summary, 2_000)
	evidence := "Proposed patch:\n" + truncate(artifact.Patch, 8_000) +
		"\n\nTest plan:\n" + truncate(artifact.Tests, 1_500)
	return summary, truncate(evidence, 10_000), nil
}

func parseFindingChecks(output, commitSHA string) ([]findingCheck, error) {
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		return nil, errors.New("agent did not return a JSON object")
	}
	var parsed struct {
		Checks []struct {
			FindingID string `json:"finding_id"`
			Verdict   string `json:"verdict"`
			Notes     string `json:"notes"`
			PoC       string `json:"poc"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("agent output was not valid per-finding check JSON: %w", err)
	}
	if len(parsed.Checks) == 0 {
		return nil, errors.New("agent returned no per-finding checks")
	}
	checks := make([]findingCheck, 0, len(parsed.Checks))
	for _, item := range parsed.Checks {
		verdict := strings.ToLower(strings.TrimSpace(item.Verdict))
		if verdict != "confirmed" && verdict != "disputed" && verdict != "fixed" {
			return nil, fmt.Errorf("invalid finding verdict %q", item.Verdict)
		}
		if strings.TrimSpace(item.FindingID) == "" {
			return nil, errors.New("finding check is missing finding_id")
		}
		checks = append(checks, findingCheck{
			findingID: strings.TrimSpace(item.FindingID),
			verdict: api.FindingVerdict{
				CommitSHA: commitSHA,
				Verdict:   verdict,
				Notes:     truncate(strings.TrimSpace(item.Notes), 2_000),
				Evidence:  truncate(strings.TrimSpace(item.PoC), 10_000),
			},
		})
	}
	return checks, nil
}

func formatDocument(doc api.ScanDocument) string {
	if len(doc.Findings) == 0 {
		return "The agent reported no findings (a valid, useful result)."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The agent reported %d finding(s):", len(doc.Findings))
	for _, f := range doc.Findings {
		lines := ""
		if f.LineStart > 0 {
			lines = fmt.Sprintf(":%d", f.LineStart)
			if f.LineEnd > f.LineStart {
				lines = fmt.Sprintf(":%d-%d", f.LineStart, f.LineEnd)
			}
		}
		fmt.Fprintf(&b, "\n  [%s] %s%s - %s", f.Severity, f.File, lines, f.Title)
	}
	return b.String()
}

func findingLines(f api.Finding) string {
	if f.LineStart <= 0 {
		return ""
	}
	if f.LineEnd > f.LineStart {
		return fmt.Sprintf(":%d-%d", f.LineStart, f.LineEnd)
	}
	return fmt.Sprintf(":%d", f.LineStart)
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
