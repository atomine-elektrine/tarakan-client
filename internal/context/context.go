package repoctx

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info is the repository context attached to an agent session.
type Info struct {
	Root      string `json:"root"`
	Name      string `json:"name"`
	Branch    string `json:"branch,omitempty"`
	Commit    string `json:"commit,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
	IsGit     bool   `json:"is_git"`
	Dirty     bool   `json:"dirty"`
	Origin    string `json:"origin,omitempty"`

	// Remote identity (any supported host).
	Host  string `json:"host,omitempty"`  // e.g. github.com, tarakan.lol
	Owner string `json:"owner,omitempty"` // remote owner / handle
	Repo  string `json:"repo,omitempty"`  // remote repository name

	// GitHubOwner/GitHubName are kept for older call sites; set when Host is GitHub,
	// and also mirrored from Owner/Repo for convenience when matching jobs.
	GitHubOwner string `json:"github_owner,omitempty"`
	GitHubName  string `json:"github_name,omitempty"`
}

// Discover finds the Git repository containing path. Outside a Git repository,
// it still returns useful directory context.
func Discover(path string) Info {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = path
	}

	info := Info{
		Root: absolute,
		Name: filepath.Base(absolute),
	}

	if root, ok := gitOutput(absolute, "rev-parse", "--show-toplevel"); ok {
		info.Root = root
		info.Name = filepath.Base(root)
		info.IsGit = true
		info.Branch, _ = gitOutput(root, "branch", "--show-current")
		info.Commit, _ = gitOutput(root, "rev-parse", "--short", "HEAD")
		info.CommitSHA, _ = gitOutput(root, "rev-parse", "HEAD")
		if status, found := gitOutput(root, "status", "--porcelain", "--untracked-files=normal"); found {
			info.Dirty = status != ""
		}
		if origin, found := gitOutput(root, "remote", "get-url", "origin"); found {
			if host, owner, repo, ok := ParseRemote(origin); ok {
				info.Host = host
				info.Owner = owner
				info.Repo = repo
				info.Origin = redactRemote(origin)
				if isGitHubHost(host) {
					info.GitHubOwner = owner
					info.GitHubName = repo
					info.Origin = "https://github.com/" + owner + "/" + repo + ".git"
				}
			} else {
				info.Origin = redactRemote(origin)
			}
		}
	}

	return info
}

func redactRemote(remote string) string {
	parsed, err := url.Parse(remote)
	if err != nil || parsed.Scheme == "" {
		if separator := strings.IndexByte(remote, ':'); separator >= 0 {
			host := remote[:separator]
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			return host + ":" + remote[separator+1:]
		}
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// GitHubRepository returns origin's owner/name pair when origin points to
// GitHub. The second result is false for non-GitHub or malformed remotes.
func (i Info) GitHubRepository() (string, bool) {
	if i.GitHubOwner == "" || i.GitHubName == "" {
		return "", false
	}
	return i.GitHubOwner + "/" + i.GitHubName, true
}

// RemoteSlug is host-less owner/name when known.
func (i Info) RemoteSlug() (owner, name string, ok bool) {
	if i.Owner != "" && i.Repo != "" {
		return i.Owner, i.Repo, true
	}
	if i.GitHubOwner != "" && i.GitHubName != "" {
		return i.GitHubOwner, i.GitHubName, true
	}
	return "", "", false
}

// ParseGitHubRemote accepts GitHub HTTPS, SSH URL, and SCP-like remote forms.
func ParseGitHubRemote(remote string) (owner, name string, ok bool) {
	host, owner, name, ok := ParseRemote(remote)
	if !ok || !isGitHubHost(host) {
		return "", "", false
	}
	return owner, name, true
}

// ParseRemote extracts host, owner, and repo from common git remote URLs
// (HTTPS, SSH, SCP-like) for GitHub, Tarakan-hosted, and similar forges.
func ParseRemote(remote string) (host, owner, name string, ok bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", "", "", false
	}

	// SCP-like: git@github.com:owner/repo.git  or  git@tarakan.lol:owner/repo.git
	if !strings.Contains(remote, "://") {
		if separator := strings.IndexByte(remote, ':'); separator >= 0 {
			hostPart := remote[:separator]
			if at := strings.LastIndexByte(hostPart, '@'); at >= 0 {
				hostPart = hostPart[at+1:]
			}
			owner, name, pathOK := parseOwnerRepoPath(remote[separator+1:])
			if !pathOK || !supportedHost(hostPart) {
				return "", "", "", false
			}
			return normalizeHost(hostPart), owner, name, true
		}
		return "", "", "", false
	}

	parsed, err := url.Parse(remote)
	if err != nil || !remoteScheme(parsed.Scheme) {
		return "", "", "", false
	}
	hostPart := parsed.Hostname()
	if !supportedHost(hostPart) {
		return "", "", "", false
	}
	owner, name, pathOK := parseOwnerRepoPath(parsed.Path)
	if !pathOK {
		return "", "", "", false
	}
	return normalizeHost(hostPart), owner, name, true
}

func remoteScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ssh", "git":
		return true
	default:
		return false
	}
}

func supportedHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	// Explicit forges + loopback (local Tarakan-hosted clones).
	if isGitHubHost(host) || isTarakanHost(host) || isLoopbackHost(host) {
		return true
	}
	// Generic host.tld/owner/repo for future forges.
	return strings.Contains(host, ".")
}

func isGitHubHost(host string) bool {
	h := strings.ToLower(host)
	return h == "github.com" || h == "www.github.com"
}

func isTarakanHost(host string) bool {
	h := strings.ToLower(host)
	return h == "tarakan.lol" || h == "www.tarakan.lol" || h == "tarakan"
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	switch h {
	case "www.github.com", "github":
		return "github.com"
	case "www.tarakan.lol", "tarakan":
		return "tarakan.lol"
	default:
		return h
	}
}

func parseOwnerRepoPath(path string) (owner, name string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner = strings.TrimSpace(parts[0])
	name = strings.TrimSuffix(strings.TrimSpace(parts[1]), ".git")
	if owner == "" || name == "" || owner == "." || name == "." {
		return "", "", false
	}
	return owner, name, true
}

// Current discovers context from the process working directory.
func Current() (Info, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return Info{}, err
	}
	return Discover(workingDirectory), nil
}

func gitOutput(directory string, args ...string) (string, bool) {
	commandArgs := append([]string{"-C", directory}, args...)
	output, err := exec.Command("git", commandArgs...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}
