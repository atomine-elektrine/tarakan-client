package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL  = "https://tarakan.lol"
	maxResponseSize = 2 << 20
)

var ErrTokenRequired = errors.New("API token is required (run tarakan login, pass --token, or set TARAKAN_API_TOKEN)")

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

type APIError struct {
	StatusCode int
	Message    string
	Errors     map[string][]string
}

func (e *APIError) Error() string {
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if len(e.Errors) == 0 {
		return fmt.Sprintf("Tarakan API returned %d: %s", e.StatusCode, message)
	}
	return fmt.Sprintf("Tarakan API returned %d: %s (%s)", e.StatusCode, message, formatValidationErrors(e.Errors))
}

// New builds a client. baseURL may come from --url/--host or TARAKAN_URL;
// token from --token or TARAKAN_API_TOKEN. The local Phoenix development
// server is the only HTTP exception; remote tokens require TLS so a production
// token is never sent in clear text by mistake.
func New(baseURL, token string, httpClient *http.Client) (*Client, error) {
	return newClient(baseURL, token, httpClient, true)
}

// NewPublic builds a client for the unauthenticated browser-login endpoints.
// It applies the same HTTPS and redirect protections as an authenticated client.
func NewPublic(baseURL string, httpClient *http.Client) (*Client, error) {
	return newClient(baseURL, "", httpClient, false)
}

func newClient(baseURL, token string, httpClient *http.Client, requireToken bool) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("host URL must be an absolute HTTP(S) URL (pass --url or TARAKAN_URL)")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && !(scheme == "http" && isLoopbackHost(parsed.Hostname())) {
		return nil, errors.New("host URL must use HTTPS except for a loopback development server")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("host URL must not contain credentials, a query, or a fragment")
	}
	token = strings.TrimSpace(token)
	if requireToken && token == "" {
		return nil, ErrTokenRequired
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{baseURL: baseURL, token: token, httpClient: &clientCopy}, nil
}

// BaseURL returns the configured Tarakan origin (no trailing slash). Used when
// cloning Tarakan-hosted repositories: git is served at {BaseURL}/{owner}/{name}.git.
func (c *Client) BaseURL() string {
	if c == nil {
		return ""
	}
	return c.baseURL
}

func (c *Client) ListTasks(ctx context.Context, owner, name string) ([]Task, error) {
	var response struct {
		Tasks []Task `json:"tasks"`
	}
	path := "/api/github/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/requests"
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Tasks, nil
}

// ListOpenJobs returns claimable Jobs from the global queue (all listed repos).
func (c *Client) ListOpenJobs(ctx context.Context) ([]Task, error) {
	var response struct {
		Jobs     []Task `json:"jobs"`
		Tasks    []Task `json:"tasks"`
		Requests []Task `json:"requests"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/requests", nil, &response); err != nil {
		return nil, err
	}
	switch {
	case len(response.Jobs) > 0:
		return response.Jobs, nil
	case len(response.Requests) > 0:
		return response.Requests, nil
	default:
		return response.Tasks, nil
	}
}

func (c *Client) GetTask(ctx context.Context, id int64) (Task, error) {
	return c.taskRequest(ctx, http.MethodGet, taskPath(id), nil)
}

func (c *Client) ClaimTask(ctx context.Context, id int64) (Task, error) {
	return c.taskRequest(ctx, http.MethodPost, taskPath(id)+"/claim", struct{}{})
}

func (c *Client) ReleaseTask(ctx context.Context, id int64) (Task, error) {
	return c.taskRequest(ctx, http.MethodDelete, taskPath(id)+"/claim", nil)
}

// RenewTaskClaim extends an active lease held by this credential's account.
func (c *Client) RenewTaskClaim(ctx context.Context, id int64) (Task, error) {
	return c.taskRequest(ctx, http.MethodPost, taskPath(id)+"/claim/renew", struct{}{})
}

func (c *Client) SubmitTask(ctx context.Context, id int64, submission Submission) (Task, error) {
	return c.taskRequest(ctx, http.MethodPost, taskPath(id)+"/complete", submission)
}

// CompleteTask is retained for source compatibility. The server transition is
// a restricted submission and never marks the contribution accepted.
func (c *Client) CompleteTask(ctx context.Context, id int64, completion Completion) (Task, error) {
	return c.SubmitTask(ctx, id, completion)
}

// ListReviewableRepositories returns the review queue. status may be empty or
// one of "unscanned"/"findings"/"reviewed"/"clear".
func (c *Client) ListReviewableRepositories(ctx context.Context, status string) ([]QueueRepository, error) {
	path := "/api/repositories"
	if status != "" {
		path += "?status=" + url.QueryEscape(status)
	}
	var response struct {
		Repositories []QueueRepository `json:"repositories"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	return response.Repositories, nil
}

// ListScans returns the reviews of a repository visible to the caller. A
// reviewer-tier credential with reviews:read sees restricted findings.
func (c *Client) ListScans(ctx context.Context, owner, name string) ([]Scan, error) {
	return c.ListScansForHost(ctx, "github", owner, name)
}

func (c *Client) ListScansForHost(ctx context.Context, host, owner, name string) ([]Scan, error) {
	var response struct {
		Scans []Scan `json:"scans"`
	}
	if err := c.do(ctx, http.MethodGet, scanBasePathForHost(host, owner, name), nil, &response); err != nil {
		return nil, err
	}
	return response.Scans, nil
}

// GetRepositoryMemory returns prompt-safe canonical findings for reconciliation.
func (c *Client) GetRepositoryMemory(ctx context.Context, owner, name, commitSHA string) (RepositoryMemory, error) {
	return c.GetRepositoryMemoryForHost(ctx, "github", owner, name, commitSHA)
}

func (c *Client) GetRepositoryMemoryForHost(ctx context.Context, host, owner, name, commitSHA string) (RepositoryMemory, error) {
	path := repositoryBasePath(host, owner, name) + "/memory"
	if commitSHA != "" {
		path += "?commit_sha=" + url.QueryEscape(commitSHA)
	}
	var memory RepositoryMemory
	if err := c.do(ctx, http.MethodGet, path, nil, &memory); err != nil {
		return RepositoryMemory{}, err
	}
	return memory, nil
}

// SubmitFindingVerdict records an independent check on one canonical finding.
func (c *Client) SubmitFindingVerdict(ctx context.Context, owner, name, publicID string, verdict FindingVerdict) error {
	return c.SubmitFindingVerdictForHost(ctx, "github", owner, name, publicID, verdict)
}

func (c *Client) SubmitFindingVerdictForHost(ctx context.Context, host, owner, name, publicID string, verdict FindingVerdict) error {
	path := repositoryBasePath(host, owner, name) +
		"/findings/" + url.PathEscape(publicID) + "/check"
	var response json.RawMessage
	return c.do(ctx, http.MethodPost, path, verdict, &response)
}

// SubmitScan submits a review in Tarakan Scan Format v1 and returns the
// quarantined scan.
func (c *Client) SubmitScan(ctx context.Context, owner, name string, submission ScanSubmission) (Scan, error) {
	return c.SubmitScanForHost(ctx, "github", owner, name, submission)
}

func (c *Client) SubmitScanForHost(ctx context.Context, host, owner, name string, submission ScanSubmission) (Scan, error) {
	var scan Scan
	if err := c.do(ctx, http.MethodPost, scanBasePathForHost(host, owner, name), submission, &scan); err != nil {
		return Scan{}, err
	}
	return scan, nil
}

// SubmitVerdict records a verdict (and optional proof-of-concept) on a review.
// The caller must be an independent qualified reviewer.
func (c *Client) SubmitVerdict(ctx context.Context, owner, name string, scanID int64, verdict Verdict) (Scan, error) {
	return c.SubmitVerdictForHost(ctx, "github", owner, name, scanID, verdict)
}

func (c *Client) SubmitVerdictForHost(ctx context.Context, host, owner, name string, scanID int64, verdict Verdict) (Scan, error) {
	path := scanBasePathForHost(host, owner, name) + "/" + strconv.FormatInt(scanID, 10) + "/verdict"
	var scan Scan
	if err := c.do(ctx, http.MethodPost, path, verdict, &scan); err != nil {
		return Scan{}, err
	}
	return scan, nil
}

func (c *Client) taskRequest(ctx context.Context, method, path string, input any) (Task, error) {
	// The canonical contract returns the task directly. The wrapper field keeps
	// the client tolerant of older development builds without weakening types.
	var raw json.RawMessage
	if err := c.do(ctx, method, path, input, &raw); err != nil {
		return Task{}, err
	}
	var wrapped struct {
		Task json.RawMessage `json:"task"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Task) != 0 {
		raw = wrapped.Task
	}
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return Task{}, fmt.Errorf("decode Tarakan task: %w", err)
	}
	return task, nil
}

func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode Tarakan request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("create Tarakan request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	request.Header.Set("User-Agent", "tarakan-client")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("contact Tarakan API: %w", err)
	}
	defer response.Body.Close()

	limited := io.LimitReader(response.Body, maxResponseSize+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("read Tarakan response: %w", err)
	}
	if len(data) > maxResponseSize {
		return errors.New("Tarakan API response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return decodeAPIError(response.StatusCode, data)
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode Tarakan response: %w", err)
	}
	return nil
}

func decodeAPIError(status int, data []byte) error {
	response := struct {
		Error  string              `json:"error"`
		Errors map[string][]string `json:"errors"`
	}{}
	_ = json.Unmarshal(data, &response)
	return &APIError{StatusCode: status, Message: response.Error, Errors: response.Errors}
}

func formatValidationErrors(errorsByField map[string][]string) string {
	parts := make([]string, 0, len(errorsByField))
	for field, messages := range errorsByField {
		parts = append(parts, field+": "+strings.Join(messages, ", "))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func taskPath(id int64) string {
	return "/api/requests/" + strconv.FormatInt(id, 10)
}

func scanBasePath(owner, name string) string {
	return scanBasePathForHost("github", owner, name)
}

func scanBasePathForHost(host, owner, name string) string {
	return repositoryBasePath(host, owner, name) + "/reports"
}

func repositoryBasePath(host, owner, name string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		host = "github.com"
	}
	return "/api/" + url.PathEscape(host) + "/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
