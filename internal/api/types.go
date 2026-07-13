package api

// Repository is the canonical repository identity returned by Tarakan. Client
// code must compare this identity with the local origin before running a task.
type Repository struct {
	ID                int64  `json:"id,omitempty"`
	Host              string `json:"host,omitempty"`
	Owner             string `json:"owner"`
	Name              string `json:"name"`
	FullName          string `json:"full_name,omitempty"`
	CanonicalURL      string `json:"canonical_url,omitempty"`
	ParticipationMode string `json:"participation_mode,omitempty"`
	RecordURL         string `json:"record_url,omitempty"`
}

func (r Repository) Slug() string {
	if r.FullName != "" {
		return r.FullName
	}
	if r.Owner == "" || r.Name == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

// Actor is the minimal public contributor identity returned by Tarakan.
type Actor struct {
	ID     int64  `json:"id,omitempty"`
	Handle string `json:"handle,omitempty"`
}

type Lease struct {
	ClaimedAt string `json:"claimed_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Active    bool   `json:"active"`
}

type Contribution struct {
	ID          int64  `json:"id,omitempty"`
	Version     int64  `json:"version,omitempty"`
	Provenance  string `json:"provenance"`
	Summary     string `json:"summary"`
	Evidence    string `json:"evidence,omitempty"`
	Contributor *Actor `json:"contributor,omitempty"`
	SubmittedAt string `json:"submitted_at,omitempty"`
}

type ReviewDecision struct {
	ID        int64  `json:"id,omitempty"`
	Action    string `json:"action"`
	Reason    string `json:"reason,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	Reviewer  *Actor `json:"reviewer,omitempty"`
	DecidedAt string `json:"decided_at,omitempty"`
}

// Task is one immutable, commit-pinned unit of collaborative security work.
type Task struct {
	ID                int64            `json:"id"`
	Repository        Repository       `json:"repository"`
	CommitSHA         string           `json:"commit_sha"`
	CommitCommittedAt string           `json:"commit_committed_at,omitempty"`
	Kind              string           `json:"kind"`
	Capability        string           `json:"capability"`
	Title             string           `json:"title"`
	Description       string           `json:"description"`
	Status            string           `json:"status"`
	Visibility        string           `json:"visibility,omitempty"`
	Creator           *Actor           `json:"creator,omitempty"`
	Claimant          *Actor           `json:"claimant,omitempty"`
	Reviewer          *Actor           `json:"reviewer,omitempty"`
	Lease             *Lease           `json:"lease,omitempty"`
	Contribution      *Contribution    `json:"contribution,omitempty"`
	Contributions     []Contribution   `json:"contributions,omitempty"`
	Decisions         []ReviewDecision `json:"decisions,omitempty"`
	PublishedAt       string           `json:"published_at,omitempty"`
	SubmittedAt       string           `json:"submitted_at,omitempty"`
	ReviewedAt        string           `json:"reviewed_at,omitempty"`
	InsertedAt        string           `json:"inserted_at,omitempty"`
	UpdatedAt         string           `json:"updated_at,omitempty"`
	CompletedAt       string           `json:"completed_at,omitempty"`
	DisclosedAt       string           `json:"disclosed_at,omitempty"`
	Discloser         *Actor           `json:"discloser,omitempty"`
	SensitiveReviewed bool             `json:"sensitive_data_reviewed,omitempty"`
	TaskURL           string           `json:"task_url,omitempty"`
	RequestURL        string           `json:"request_url,omitempty"`
	LinkedReviewID    *int64           `json:"linked_review_id,omitempty"`
	LinkedReview      *LinkedReview    `json:"linked_review,omitempty"`
	TargetReviewID    *int64           `json:"target_review_id,omitempty"`
	TargetReview      *LinkedReview    `json:"target_review,omitempty"`
}

// LinkedReview is the structured Review created when completing a Request with
// Tarakan Review/Scan Format document.
type LinkedReview struct {
	ID              int64     `json:"id"`
	ReviewStatus    string    `json:"review_status,omitempty"`
	Visibility      string    `json:"visibility,omitempty"`
	FindingsCount   int64     `json:"findings_count,omitempty"`
	Provenance      string    `json:"provenance,omitempty"`
	ReviewKind      string    `json:"review_kind,omitempty"`
	Model           string    `json:"model,omitempty"`
	PromptVersion   string    `json:"prompt_version,omitempty"`
	CommitSHA       string    `json:"commit_sha,omitempty"`
	SourceRequestID *int64    `json:"source_request_id,omitempty"`
	Findings        []Finding `json:"findings,omitempty"`
}

// Submission completes a Request. Prefer Document (Review Format) for
// finding-producing kinds so Tarakan records Findings; Evidence is legacy prose.
// For verify_findings with target_review_id, set Verdict + Notes (or Summary).
type Submission struct {
	Provenance    string        `json:"provenance"`
	Summary       string        `json:"summary,omitempty"`
	Evidence      string        `json:"evidence,omitempty"`
	Model         string        `json:"model,omitempty"`
	PromptVersion string        `json:"prompt_version,omitempty"`
	Document      *ScanDocument `json:"document,omitempty"`
	Verdict       string        `json:"verdict,omitempty"`
	Notes         string        `json:"notes,omitempty"`
}

type Completion = Submission

// QueueRepository is a repository in the review queue returned by
// GET /api/repositories. It is the work a scanning client picks up.
type QueueRepository struct {
	Host            string `json:"host"`
	Owner           string `json:"owner"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	DefaultBranch   string `json:"default_branch,omitempty"`
	PrimaryLanguage string `json:"primary_language,omitempty"`
	ScanCount       int64  `json:"scan_count"`
	LastScannedAt   string `json:"last_scanned_at,omitempty"`
	RegisteredAt    string `json:"registered_at,omitempty"`
	RecordURL       string `json:"record_url,omitempty"`
}

func (r QueueRepository) Slug() string {
	if r.Owner == "" || r.Name == "" {
		return ""
	}
	return r.Owner + "/" + r.Name
}

// Finding is one issue inside a review, visible only when the caller is
// authorized to see restricted evidence.
type Finding struct {
	PublicID           string `json:"public_id,omitempty"`
	CanonicalFindingID string `json:"canonical_finding_id,omitempty"`
	Disposition        string `json:"disposition,omitempty"`
	File               string `json:"file"`
	LineStart          int64  `json:"line_start,omitempty"`
	LineEnd            int64  `json:"line_end,omitempty"`
	Severity           string `json:"severity"`
	Title              string `json:"title"`
	Description        string `json:"description"`
}

// ScanConfirmation is a recorded verdict on a review.
type ScanConfirmation struct {
	Verdict    string `json:"verdict"`
	Provenance string `json:"provenance"`
	Verifier   string `json:"verifier,omitempty"`
}

// Scan is one submitted review of a repository at an exact commit.
type Scan struct {
	ID             int64              `json:"id"`
	CommitSHA      string             `json:"commit_sha"`
	Provenance     string             `json:"provenance"`
	ReviewKind     string             `json:"review_kind"`
	Model          string             `json:"model,omitempty"`
	PromptVersion  string             `json:"prompt_version,omitempty"`
	RunID          string             `json:"run_id,omitempty"`
	ReviewStatus   string             `json:"review_status"`
	Visibility     string             `json:"visibility"`
	Verified       bool               `json:"verified"`
	FindingsCount  int64              `json:"findings_count"`
	DetailsVisible bool               `json:"details_visible"`
	Submitter      string             `json:"submitter,omitempty"`
	Findings       []Finding          `json:"findings,omitempty"`
	Confirmations  []ScanConfirmation `json:"confirmations,omitempty"`
}

// ScanDocument is the Tarakan Scan Format v1 body of a review submission.
type ScanDocument struct {
	Format   int64         `json:"tarakan_scan_format"`
	Findings []ScanFinding `json:"findings"`
}

// ScanFinding is one finding inside a submitted ScanDocument.
type ScanFinding struct {
	File              string `json:"file"`
	LineStart         int64  `json:"line_start,omitempty"`
	LineEnd           int64  `json:"line_end,omitempty"`
	Severity          string `json:"severity"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Disposition       string `json:"disposition,omitempty"`
	ExistingFindingID string `json:"existing_finding_id,omitempty"`
}

// ScanSubmission is the request body for POST .../scans.
type ScanSubmission struct {
	CommitSHA     string       `json:"commit_sha"`
	Provenance    string       `json:"provenance"`
	ReviewKind    string       `json:"review_kind"`
	Model         string       `json:"model,omitempty"`
	PromptVersion string       `json:"prompt_version,omitempty"`
	RunID         string       `json:"run_id,omitempty"`
	Document      ScanDocument `json:"document"`
}

// RepositoryMemory is the compact canonical issue index used only after an
// agent has completed a blind discovery pass.
type RepositoryMemory struct {
	Repository      string                   `json:"repository"`
	TargetCommitSHA string                   `json:"target_commit_sha,omitempty"`
	Findings        []CanonicalFindingMemory `json:"findings"`
}

type CanonicalFindingMemory struct {
	PublicID                string `json:"public_id"`
	Status                  string `json:"status"`
	File                    string `json:"file_path"`
	LineStart               int64  `json:"line_start,omitempty"`
	LineEnd                 int64  `json:"line_end,omitempty"`
	Severity                string `json:"severity"`
	Title                   string `json:"title"`
	Description             string `json:"description"`
	FirstSeenCommitSHA      string `json:"first_seen_commit_sha"`
	LastSeenCommitSHA       string `json:"last_seen_commit_sha"`
	SameCommit              bool   `json:"same_commit"`
	DetectionsCount         int64  `json:"detections_count"`
	DistinctSubmittersCount int64  `json:"distinct_submitters_count"`
	DistinctModelsCount     int64  `json:"distinct_models_count"`
	ConfirmationsCount      int64  `json:"confirmations_count"`
	DisputesCount           int64  `json:"disputes_count"`
}

type FindingVerdict struct {
	CommitSHA  string `json:"commit_sha"`
	Verdict    string `json:"verdict"`
	Provenance string `json:"provenance"`
	Notes      string `json:"notes"`
	Evidence   string `json:"evidence,omitempty"`
}

// Verdict is the request body for POST .../scans/:id/verdict.
type Verdict struct {
	Verdict    string `json:"verdict"`
	Provenance string `json:"provenance"`
	Notes      string `json:"notes"`
	Evidence   string `json:"evidence,omitempty"`
}
