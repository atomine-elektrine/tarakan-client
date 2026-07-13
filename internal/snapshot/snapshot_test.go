package snapshot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreatePinsCommitWithoutGitMetadataAndDetectsChanges(t *testing.T) {
	repository := createTestRepository(t)
	commit := gitOutput(t, repository, "rev-parse", "HEAD")

	snapshot, err := Create(repository, commit)
	if err != nil {
		t.Fatal(err)
	}
	root := snapshot.Root
	t.Cleanup(func() { _ = snapshot.Close() })

	if _, err := os.Stat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("snapshot retains Git metadata: %v", err)
	}
	changed, err := snapshot.Changed()
	if err != nil || changed {
		t.Fatalf("unchanged snapshot: changed=%v err=%v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err = snapshot.Changed()
	if err != nil || !changed {
		t.Fatalf("changed snapshot: changed=%v err=%v", changed, err)
	}

	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("snapshot was not destroyed: %v", err)
	}
}

func TestCreateRejectsUnavailableCommit(t *testing.T) {
	repository := createTestRepository(t)
	_, err := Create(repository, "dead000000000000000000000000000000000000")
	if !errors.Is(err, ErrCommitUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateNeutralizesExternalAndEscapingSymlinks(t *testing.T) {
	repository := createTestRepository(t)

	// Absolute external pointer (common "works on my Mac" vendor path).
	if err := os.Symlink("/Users/someone/Desktop/secret", filepath.Join(repository, "external-abs")); err != nil {
		t.Fatal(err)
	}
	// Relative path that escapes the tree.
	if err := os.Symlink("../host-secret", filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	// Safe in-tree relative link must be kept.
	if err := os.Symlink("README.md", filepath.Join(repository, "readme-link")); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "external-abs", "escape", "readme-link")
	runGitTest(t, repository, "-c", "user.name=Tarakan Test", "-c", "user.email=test@tarakan.invalid", "commit", "-m", "symlinks")

	commit := gitOutput(t, repository, "rev-parse", "HEAD")
	snap, err := Create(repository, commit)
	if err != nil {
		t.Fatalf("Create should neutralize external symlinks, got: %v", err)
	}
	t.Cleanup(func() { _ = snap.Close() })

	// Escaping / absolute links become ordinary files with the original target.
	for _, name := range []string{"external-abs", "escape"} {
		info, err := os.Lstat(filepath.Join(snap.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Fatalf("%s still a symlink", name)
		}
		body, err := os.ReadFile(filepath.Join(snap.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "neutralized external symlink") {
			t.Fatalf("%s body = %q", name, body)
		}
	}

	// In-tree relative symlink preserved.
	target, err := os.Readlink(filepath.Join(snap.Root, "readme-link"))
	if err != nil {
		t.Fatalf("safe symlink should remain: %v", err)
	}
	if target != "README.md" {
		t.Fatalf("safe symlink target = %q", target)
	}

	changed, err := snap.Changed()
	if err != nil || changed {
		t.Fatalf("unchanged after neutralize: changed=%v err=%v", changed, err)
	}
}

func createTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	runGitTest(t, repository, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repository, "add", "README.md")
	runGitTest(t, repository, "-c", "user.name=Tarakan Test", "-c", "user.email=test@tarakan.invalid", "commit", "-m", "initial")
	return repository
}

func runGitTest(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func gitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}
