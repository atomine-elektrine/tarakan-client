package repoctx

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverOutsideGit(t *testing.T) {
	directory := t.TempDir()
	info := Discover(directory)

	if info.Root != directory {
		t.Fatalf("root = %q, want %q", info.Root, directory)
	}
	if info.Name != filepath.Base(directory) {
		t.Fatalf("name = %q, want %q", info.Name, filepath.Base(directory))
	}
	if info.IsGit {
		t.Fatal("temporary directory unexpectedly detected as a Git repository")
	}
}

func TestDiscoverGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	directory := t.TempDir()
	command := exec.Command("git", "init", "-b", "main", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", directory, "remote", "add", "origin", "git@github.com:tarakan-lol/client.git")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, output)
	}

	info := Discover(directory)
	if !info.IsGit {
		t.Fatal("Git repository was not detected")
	}
	if info.Branch != "main" {
		t.Fatalf("branch = %q, want main", info.Branch)
	}
	if info.GitHubOwner != "tarakan-lol" || info.GitHubName != "client" {
		t.Fatalf("GitHub repository = %q/%q", info.GitHubOwner, info.GitHubName)
	}
}

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		owner  string
		repo   string
		ok     bool
	}{
		{name: "HTTPS", remote: "https://github.com/openai/codex.git", owner: "openai", repo: "codex", ok: true},
		{name: "SSH URL", remote: "ssh://git@github.com/openai/codex.git", owner: "openai", repo: "codex", ok: true},
		{name: "SCP SSH", remote: "git@github.com:openai/codex.git", owner: "openai", repo: "codex", ok: true},
		{name: "git protocol", remote: "git://github.com/openai/codex", owner: "openai", repo: "codex", ok: true},
		{name: "lookalike host", remote: "https://github.com.example.org/openai/codex.git", ok: false},
		{name: "GitLab", remote: "git@gitlab.com:openai/codex.git", ok: false},
		{name: "Tarakan hosted", remote: "https://tarakan.lol/max/elektrine.git", ok: false},
		{name: "nested path", remote: "https://github.com/one/two/three.git", ok: false},
		{name: "file scheme", remote: "file://github.com/openai/codex.git", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner, repo, ok := ParseGitHubRemote(test.remote)
			if owner != test.owner || repo != test.repo || ok != test.ok {
				t.Fatalf("ParseGitHubRemote(%q) = %q, %q, %v", test.remote, owner, repo, ok)
			}
		})
	}
}

func TestParseRemoteMultiHost(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		host   string
		owner  string
		repo   string
		ok     bool
	}{
		{name: "GitHub HTTPS", remote: "https://github.com/openai/codex.git", host: "github.com", owner: "openai", repo: "codex", ok: true},
		{name: "Tarakan HTTPS", remote: "https://tarakan.lol/max/elektrine.git", host: "tarakan.lol", owner: "max", repo: "elektrine", ok: true},
		{name: "Tarakan no .git", remote: "https://tarakan.lol/max/elektrine", host: "tarakan.lol", owner: "max", repo: "elektrine", ok: true},
		{name: "localhost dev", remote: "http://localhost:4000/max/elektrine.git", host: "localhost", owner: "max", repo: "elektrine", ok: true},
		{name: "SCP Tarakan", remote: "git@tarakan.lol:max/elektrine.git", host: "tarakan.lol", owner: "max", repo: "elektrine", ok: true},
		{name: "nested path", remote: "https://tarakan.lol/one/two/three.git", ok: false},
		{name: "empty", remote: "", ok: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host, owner, repo, ok := ParseRemote(test.remote)
			if host != test.host || owner != test.owner || repo != test.repo || ok != test.ok {
				t.Fatalf("ParseRemote(%q) = %q, %q, %q, %v; want %q, %q, %q, %v",
					test.remote, host, owner, repo, ok, test.host, test.owner, test.repo, test.ok)
			}
		})
	}
}

func TestDiscoverTarakanRemote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	directory := t.TempDir()
	if out, err := exec.Command("git", "init", "-b", "main", directory).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", directory, "remote", "add", "origin", "https://tarakan.lol/max/elektrine.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
	info := Discover(directory)
	if !info.IsGit {
		t.Fatal("expected git repo")
	}
	if info.Host != "tarakan.lol" || info.Owner != "max" || info.Repo != "elektrine" {
		t.Fatalf("remote = %q/%q/%q", info.Host, info.Owner, info.Repo)
	}
	if owner, name, ok := info.RemoteSlug(); !ok || owner != "max" || name != "elektrine" {
		t.Fatalf("RemoteSlug = %q/%q %v", owner, name, ok)
	}
}

func TestRedactRemoteCredentials(t *testing.T) {
	remote := redactRemote("https://secret-token@github.example/repository.git?token=also-secret")
	if remote != "https://github.example/repository.git" {
		t.Fatalf("redacted remote = %q", remote)
	}
}
