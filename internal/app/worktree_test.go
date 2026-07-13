package app

import (
	"strings"
	"testing"

	"tarakan-client/internal/api"
	repoctx "tarakan-client/internal/context"
)

func TestCloneRemoteURLTarakanPrefersAPIBase(t *testing.T) {
	repo := api.Repository{
		Host:         "tarakan.lol",
		Owner:        "max",
		Name:         "elektrine",
		CanonicalURL: "https://tarakan.lol/max/elektrine",
	}
	got, err := cloneRemoteURL(repo, "http://localhost:4000")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://localhost:4000/max/elektrine.git" {
		t.Fatalf("clone URL = %q", got)
	}
}

func TestCloneRemoteURLTarakanFallsBackToPublic(t *testing.T) {
	repo := api.Repository{
		Host:  "tarakan.lol",
		Owner: "max",
		Name:  "elektrine",
	}
	got, err := cloneRemoteURL(repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://tarakan.lol/max/elektrine.git" {
		t.Fatalf("clone URL = %q", got)
	}
}

func TestCloneRemoteURLUsesCanonicalURL(t *testing.T) {
	repo := api.Repository{
		Host:         "tarakan.lol",
		Owner:        "max",
		Name:         "elektrine",
		CanonicalURL: "https://tarakan.lol/max/elektrine",
	}
	// Empty apiBase falls through to CanonicalURL (with .git).
	got, err := cloneRemoteURL(repo, "   ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://tarakan.lol/max/elektrine.git" {
		t.Fatalf("clone URL = %q", got)
	}
}

func TestCloneRemoteURLGitHub(t *testing.T) {
	repo := api.Repository{Host: "github", Owner: "openai", Name: "codex"}
	got, err := cloneRemoteURL(repo, "http://localhost:4000")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/openai/codex.git" {
		t.Fatalf("clone URL = %q", got)
	}
}

func TestCloneRemoteURLUnsupported(t *testing.T) {
	repo := api.Repository{Host: "gitlab.com", Owner: "o", Name: "n"}
	_, err := cloneRemoteURL(repo, "")
	if err == nil || !strings.Contains(err.Error(), "not supported for auto-clone") {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalMatchesTaskTarakanAndLoopback(t *testing.T) {
	task := api.Task{
		Repository: api.Repository{Host: "tarakan.lol", Owner: "max", Name: "elektrine"},
	}
	local := repoctx.Info{Host: "localhost", Owner: "max", Repo: "elektrine", IsGit: true}
	if !localMatchesTask(local, task) {
		t.Fatal("localhost clone should match tarakan.lol job")
	}
	mismatch := repoctx.Info{Host: "github.com", Owner: "max", Repo: "elektrine", IsGit: true}
	if localMatchesTask(mismatch, task) {
		t.Fatal("github.com should not match tarakan.lol job")
	}
}

func TestNormalizeTaskHost(t *testing.T) {
	if got := normalizeTaskHost("tarakan"); got != "tarakan.lol" {
		t.Fatalf("normalize tarakan = %q", got)
	}
	if got := normalizeTaskHost("github"); got != "github.com" {
		t.Fatalf("normalize github = %q", got)
	}
	if !isTarakanHost("tarakan.lol") || !isTarakanHost("tarakan") {
		t.Fatal("isTarakanHost failed")
	}
}
