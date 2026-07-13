package app

import (
	"strings"
	"testing"

	"tarakan-client/internal/reviewdoc"
)

func TestLastJSONObjectIgnoresProseAndFences(t *testing.T) {
	output := "Here is my review.\n\n```json\n{\"tarakan_scan_format\": 1, \"findings\": []}\n```\nDone."
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		t.Fatal("expected to find a JSON object")
	}
	if raw != `{"tarakan_scan_format": 1, "findings": []}` {
		t.Fatalf("unexpected extraction: %q", raw)
	}
}

func TestLastJSONObjectHandlesBracesInStrings(t *testing.T) {
	output := `prose {"notes": "a } brace and a { brace inside", "verdict": "confirmed"} trailing`
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		t.Fatal("expected to find a JSON object")
	}
	if raw != `{"notes": "a } brace and a { brace inside", "verdict": "confirmed"}` {
		t.Fatalf("string braces broke balancing: %q", raw)
	}
}

func TestLastJSONObjectHandlesEscapedQuotes(t *testing.T) {
	output := `{"poc": "he said \"} not the end\" and continued", "verdict": "disputed"}`
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		t.Fatal("expected to find a JSON object")
	}
	if raw != output {
		t.Fatalf("escaped quote broke balancing: %q", raw)
	}
}

func TestLastJSONObjectPicksLastObject(t *testing.T) {
	output := `{"first": 1} then some reasoning then {"verdict": "confirmed"}`
	raw, ok := reviewdoc.LastJSONObject(output)
	if !ok {
		t.Fatal("expected to find a JSON object")
	}
	if raw != `{"verdict": "confirmed"}` {
		t.Fatalf("expected the last object, got: %q", raw)
	}
}

func TestParseScanDocumentReadsFindings(t *testing.T) {
	output := "Reviewed.\n{\"tarakan_scan_format\":1,\"findings\":[{\"file\":\"app.js\",\"line_start\":83,\"line_end\":83,\"severity\":\"high\",\"title\":\"Hardcoded secret\",\"description\":\"A token is committed.\"}]}"
	doc, err := reviewdoc.Parse(output)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if doc.Format != 1 || len(doc.Findings) != 1 {
		t.Fatalf("unexpected document: %#v", doc)
	}
	f := doc.Findings[0]
	if f.File != "app.js" || f.LineStart != 83 || f.Severity != "high" || f.Title != "Hardcoded secret" {
		t.Fatalf("unexpected finding: %#v", f)
	}
}

func TestParseScanDocumentAcceptsEmptyFindings(t *testing.T) {
	doc, err := reviewdoc.Parse(`{"tarakan_scan_format":1,"findings":[]}`)
	if err != nil {
		t.Fatalf("empty findings should be valid: %v", err)
	}
	if len(doc.Findings) != 0 {
		t.Fatalf("expected zero findings, got %d", len(doc.Findings))
	}
}

func TestParseScanDocumentRejectsNonJSON(t *testing.T) {
	if _, err := reviewdoc.Parse("I could not complete the review."); err == nil {
		t.Fatal("expected an error for output with no JSON")
	}
}

func TestParseFindingChecks(t *testing.T) {
	output := "My analysis follows.\n{\"checks\":[{\"finding_id\":\"finding-1\",\"verdict\":\"CONFIRMED\",\"notes\":\"Reproduced at app.js:83\",\"poc\":\"curl ... returns the token\"}]}"
	checks, err := parseFindingChecks(output, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 1 || checks[0].verdict.Verdict != "confirmed" {
		t.Fatalf("checks = %#v", checks)
	}
	if checks[0].verdict.CommitSHA == "" || checks[0].verdict.Evidence == "" {
		t.Fatalf("verdict should include commit and evidence: %#v", checks[0].verdict)
	}
}

func TestParseFindingChecksRejectsUnknownVerdict(t *testing.T) {
	if _, err := parseFindingChecks(`{"checks":[{"finding_id":"finding-1","verdict":"maybe","notes":"unsure"}]}`, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("expected an error for an unknown verdict value")
	}
}

func TestParseFixArtifactRequiresPatchAndTestPlan(t *testing.T) {
	output := `{"summary":"Guard the state transition.","patch":"diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new","tests":"go test ./..."}`
	summary, evidence, err := parseFixArtifact(output)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "Guard the state transition." || !strings.Contains(evidence, "diff --git") || !strings.Contains(evidence, "go test ./...") {
		t.Fatalf("unexpected fix artifact: %q %q", summary, evidence)
	}

	for _, invalid := range []string{
		`{"summary":"x","patch":"","tests":"go test"}`,
		`{"summary":"x","patch":"diff --git a/a b/a","tests":""}`,
		`{"summary":"x","patch":"replace the line","tests":"go test"}`,
	} {
		if _, _, err := parseFixArtifact(invalid); err == nil {
			t.Fatalf("invalid fix artifact succeeded: %s", invalid)
		}
	}
}

func TestTruncateCountsRunes(t *testing.T) {
	if got := truncate("héllo", 3); got != "hél" {
		t.Fatalf("expected rune-safe truncation, got %q", got)
	}
	if got := truncate("hi", 5); got != "hi" {
		t.Fatalf("short strings should be unchanged, got %q", got)
	}
}
