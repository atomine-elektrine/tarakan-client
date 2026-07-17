package app

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/atomine-elektrine/tarakan-client/internal/api"
	repoctx "github.com/atomine-elektrine/tarakan-client/internal/context"
)

// worktreeForTask returns a git repository root that contains the job's pinned
// commit. Uses the local clone when origin matches; otherwise clones from the
// job's host (GitHub, Tarakan-hosted, or CanonicalURL) into a temp directory.
//
// apiBase is TARAKAN_URL (e.g. http://localhost:4000) used for Tarakan-hosted
// clones when CanonicalURL points at the public host but git is served locally.
// report is optional; non-empty lines are status updates for the TUI.
func worktreeForTask(local repoctx.Info, task api.Task, apiBase string, report func(string)) (root string, cleanup func(), err error) {
	if report == nil {
		report = func(string) {}
	}
	cleanup = func() {}
	if len(task.CommitSHA) != 40 {
		return "", cleanup, fmt.Errorf("job %d has no full commit SHA to pin", task.ID)
	}
	slug := task.Repository.Slug()
	if local.IsGit && localMatchesTask(local, task) {
		report("Using local clone of " + slug + " at " + local.Root)
		return local.Root, cleanup, nil
	}

	owner := task.Repository.Owner
	name := task.Repository.Name
	if owner == "" || name == "" {
		return "", cleanup, fmt.Errorf("job %d has no repository identity", task.ID)
	}

	remote, err := cloneRemoteURL(task.Repository, apiBase)
	if err != nil {
		return "", cleanup, err
	}

	report("Cloning " + slug + " from " + remote + "…")
	base, err := os.MkdirTemp("", "tarakan-pickup-")
	if err != nil {
		return "", cleanup, fmt.Errorf("create temp worktree: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(base) }

	repoDir := filepath.Join(base, "repository")
	if err := runGit("", "clone", "--filter=blob:none", "--no-checkout", remote, repoDir); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("clone %s for job %d: %w\n(hint: cd into a clone of %s/%s and re-run)", remote, task.ID, err, owner, name)
	}
	report("Fetching pinned commit " + shortSHA(task.CommitSHA) + "…")
	if err := runGit(repoDir, "fetch", "--depth", "1", "origin", task.CommitSHA); err != nil {
		if err2 := runGit(repoDir, "fetch", "origin", task.CommitSHA); err2 != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("fetch commit %s: %w", shortSHA(task.CommitSHA), err2)
		}
	}
	report("Checking out " + shortSHA(task.CommitSHA) + "…")
	if err := runGit(repoDir, "checkout", "--detach", "--force", task.CommitSHA); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("checkout %s: %w", shortSHA(task.CommitSHA), err)
	}
	report("Worktree ready for " + slug + " @ " + shortSHA(task.CommitSHA))
	return repoDir, cleanup, nil
}

func localMatchesTask(local repoctx.Info, task api.Task) bool {
	owner, name, ok := local.RemoteSlug()
	if !ok {
		owner, name = local.GitHubOwner, local.GitHubName
		if owner == "" || name == "" {
			return false
		}
	}
	if !strings.EqualFold(owner, task.Repository.Owner) || !strings.EqualFold(name, task.Repository.Name) {
		return false
	}
	taskHost := normalizeTaskHost(task.Repository.Host)
	localHost := strings.ToLower(local.Host)
	if taskHost == "" || localHost == "" {
		// Owner/name match is enough when one side lacks host.
		return true
	}
	if taskHost == localHost {
		return true
	}
	// Local clone of hosted repo via localhost vs public tarakan.lol.
	if isTarakanHost(taskHost) && (isTarakanHost(localHost) || isLoopback(localHost)) {
		return true
	}
	return false
}

func cloneRemoteURL(repo api.Repository, apiBase string) (string, error) {
	host := normalizeTaskHost(repo.Host)
	owner, name := repo.Owner, repo.Name

	// Prefer live Tarakan base for hosted repos (dev often uses localhost while
	// canonical_url says https://tarakan.lol/…).
	if isTarakanHost(host) || host == "" && strings.Contains(strings.ToLower(repo.CanonicalURL), "tarakan") {
		if base := strings.TrimRight(strings.TrimSpace(apiBase), "/"); base != "" {
			return base + "/" + owner + "/" + name + ".git", nil
		}
	}

	if u := strings.TrimSpace(repo.CanonicalURL); u != "" {
		u = strings.TrimRight(u, "/")
		if !strings.HasSuffix(strings.ToLower(u), ".git") {
			u += ".git"
		}
		if _, err := url.Parse(u); err == nil {
			return u, nil
		}
	}

	switch {
	case host == "" || host == "github.com":
		return "https://github.com/" + owner + "/" + name + ".git", nil
	case isTarakanHost(host):
		return "https://tarakan.lol/" + owner + "/" + name + ".git", nil
	default:
		return "", fmt.Errorf("job host %q is not supported for auto-clone; clone %s/%s locally and re-run", repo.Host, owner, name)
	}
}

func normalizeTaskHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch h {
	case "github", "www.github.com":
		return "github.com"
	case "tarakan", "www.tarakan.lol":
		return "tarakan.lol"
	default:
		return h
	}
}

func isTarakanHost(host string) bool {
	h := normalizeTaskHost(host)
	return h == "tarakan.lol"
}

func isLoopback(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GCM_INTERACTIVE=never",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}
