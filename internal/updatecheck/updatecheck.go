// Package updatecheck tells users when a newer tarakan release is available.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultRepo is where official release binaries are published.
	DefaultRepo = "atomine-elektrine/tarakan-client"
	// InstallHint is the one-liner people already use to install or upgrade.
	InstallHint = "curl -fsSL https://tarakan.lol/install.sh | bash"
	cacheTTL    = 24 * time.Hour
	httpTimeout = 3 * time.Second
)

// githubAPIBase is overridden in tests.
var githubAPIBase = "https://api.github.com"

// Result is one comparison of the running binary against the latest release.
type Result struct {
	Current         string
	Latest          string
	UpdateAvailable bool
}

// Notice is a short human line for stderr (empty when no update).
func (r Result) Notice() string {
	if !r.UpdateAvailable || r.Latest == "" {
		return ""
	}
	return fmt.Sprintf(
		"tarakan %s is available (you have %s). Update:\n  %s",
		displayVersion(r.Latest),
		displayVersion(r.Current),
		InstallHint,
	)
}

// MaybeNotify checks for a newer release and prints a notice to w when one
// exists. Network and disk errors are ignored so update checks never break
// normal CLI work. Set TARAKAN_SKIP_UPDATE_CHECK=1 to disable.
func MaybeNotify(w io.Writer, current string) {
	if w == nil || skipEnabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	result, err := Check(ctx, current)
	if err != nil || !result.UpdateAvailable {
		return
	}
	if notice := result.Notice(); notice != "" {
		fmt.Fprintln(w, notice)
	}
}

// Check returns whether current is older than the latest GitHub release.
// Results are cached under the user config dir for cacheTTL.
func Check(ctx context.Context, current string) (Result, error) {
	current = normalizeVersion(current)
	result := Result{Current: current}
	if current == "" || current == "dev" {
		return result, nil
	}
	if skipEnabled() {
		return result, nil
	}

	latest, fromCache, err := latestVersion(ctx)
	if err != nil {
		return result, err
	}
	result.Latest = latest
	result.UpdateAvailable = compareVersions(current, latest) < 0
	_ = fromCache
	return result, nil
}

func skipEnabled() bool {
	v := strings.TrimSpace(os.Getenv("TARAKAN_SKIP_UPDATE_CHECK"))
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

func latestVersion(ctx context.Context) (string, bool, error) {
	if cached, ok := readCache(); ok {
		return cached, true, nil
	}
	latest, err := fetchLatestGitHub(ctx, DefaultRepo)
	if err != nil {
		return "", false, err
	}
	_ = writeCache(latest)
	return latest, false, nil
}

type cacheFile struct {
	Latest    string    `json:"latest"`
	CheckedAt time.Time `json:"checked_at"`
}

func cachePath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tarakan", "update-check.json"), nil
}

func readCache() (string, bool) {
	path, err := cachePath()
	if err != nil {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c cacheFile
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", false
	}
	if c.Latest == "" || time.Since(c.CheckedAt) > cacheTTL {
		return "", false
	}
	return normalizeVersion(c.Latest), true
}

func writeCache(latest string) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cacheFile{
		Latest:    normalizeVersion(latest),
		CheckedAt: time.Now().UTC(),
	}, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o600)
}

func fetchLatestGitHub(ctx context.Context, repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", errors.New("empty repo")
	}
	url := strings.TrimRight(githubAPIBase, "/") + "/repos/" + repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tarakan-client-updatecheck")

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", err
	}
	tag := normalizeVersion(body.TagName)
	if tag == "" {
		return "", errors.New("github releases: empty tag_name")
	}
	return tag, nil
}

func displayVersion(v string) string {
	v = normalizeVersion(v)
	if v == "" {
		return "unknown"
	}
	return "v" + v
}

// normalizeVersion strips a leading "v" and whitespace.
func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	v = strings.TrimPrefix(v, "V")
	return strings.TrimSpace(v)
}

// compareVersions returns -1 if a < b, 0 if equal, 1 if a > b.
// Handles dotted numeric versions (1.2.3). Missing trailing segments are 0
// (so "0.2" equals "0.2.0"). Non-numeric tails compare as strings.
func compareVersions(a, b string) int {
	a = normalizeVersion(a)
	b = normalizeVersion(b)
	if a == b {
		return 0
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		an, aOk := versionPart(as, i)
		bn, bOk := versionPart(bs, i)
		if aOk && bOk {
			if an < bn {
				return -1
			}
			if an > bn {
				return 1
			}
			continue
		}
		// Fall back to string compare for pre-release-ish segments.
		ap, bp := "", ""
		if i < len(as) {
			ap = as[i]
		}
		if i < len(bs) {
			bp = bs[i]
		}
		if ap < bp {
			return -1
		}
		if ap > bp {
			return 1
		}
	}
	return 0
}

func versionPart(parts []string, i int) (int, bool) {
	if i >= len(parts) || parts[i] == "" {
		return 0, true
	}
	n, err := strconv.Atoi(parts[i])
	if err != nil {
		return 0, false
	}
	return n, true
}
