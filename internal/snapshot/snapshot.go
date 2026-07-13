package snapshot

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	ErrCommitUnavailable = errors.New("pinned commit is not available in the local repository")
	fullCommitPattern    = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
)

// Snapshot is a disposable, metadata-free copy of one exact Git commit.
type Snapshot struct {
	Root     string
	base     string
	baseline [sha256.Size]byte
}

// Create materializes commit from repositoryRoot without sharing Git object
// hardlinks. Hooks, global Git configuration, and interactive credential
// prompts are disabled for every Git subprocess involved in the snapshot.
func Create(repositoryRoot, commit string) (*Snapshot, error) {
	if !fullCommitPattern.MatchString(commit) {
		return nil, errors.New("task commit must be a full 40-character hexadecimal SHA")
	}
	if err := gitAvailable(repositoryRoot, commit); err != nil {
		return nil, err
	}

	base, err := os.MkdirTemp("", "tarakan-snapshot-")
	if err != nil {
		return nil, fmt.Errorf("create snapshot directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(base)
		}
	}()

	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return nil, fmt.Errorf("create isolated Git home: %w", err)
	}
	root := filepath.Join(base, "repository")
	if output, err := runGit(base, home, "-c", "core.hooksPath=/dev/null", "clone", "--local", "--no-hardlinks", "--no-checkout", "--", repositoryRoot, root); err != nil {
		return nil, fmt.Errorf("clone repository snapshot: %w: %s", err, strings.TrimSpace(output))
	}
	if output, err := runGit(base, home,
		"-C", root,
		"-c", "core.hooksPath=/dev/null",
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.required=false",
		"checkout", "--detach", "--force", commit,
	); err != nil {
		return nil, fmt.Errorf("check out pinned commit: %w: %s", err, strings.TrimSpace(output))
	}
	// Absolute or escaping symlinks would let the agent read host paths.
	// Neutralize them (replace with a small text file) instead of failing:
	// many real repos ship broken absolute "external/" pointers from another
	// machine (e.g. /Users/... on Linux), and the review should still run.
	if err := neutralizeExternalSymlinks(root); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(filepath.Join(root, ".git")); err != nil {
		return nil, fmt.Errorf("remove snapshot Git metadata: %w", err)
	}

	baseline, err := digestTree(root)
	if err != nil {
		return nil, fmt.Errorf("hash repository snapshot: %w", err)
	}
	cleanup = false
	return &Snapshot{Root: root, base: base, baseline: baseline}, nil
}

func neutralizeExternalSymlinks(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink == 0 {
			return nil
		}

		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if !isExternalSymlink(root, path, target) {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove external symlink %s: %w", filepath.ToSlash(rel), err)
		}
		// Regular file: agent cannot follow it to host content; original
		// target is preserved as text so reviewers still see the pointer.
		body := "tarakan-snapshot: neutralized external symlink\noriginal-target: " + target + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("neutralize external symlink %s: %w", filepath.ToSlash(rel), err)
		}
		return nil
	})
}

// isExternalSymlink is true for absolute targets or relative targets that
// resolve outside the snapshot root.
func isExternalSymlink(root, path, target string) bool {
	if filepath.IsAbs(target) {
		return true
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return true
	}
	return false
}

// Changed reports whether any path, mode, symlink target, or regular-file
// content differs from the snapshot presented to the agent.
func (s *Snapshot) Changed() (bool, error) {
	current, err := digestTree(s.Root)
	if err != nil {
		return false, err
	}
	return current != s.baseline, nil
}

// Close destroys the snapshot. It is safe to call more than once.
func (s *Snapshot) Close() error {
	if s == nil || s.base == "" {
		return nil
	}
	base := s.base
	s.base = ""
	return os.RemoveAll(base)
}

func gitAvailable(repositoryRoot, commit string) error {
	command := exec.Command("git", "-C", repositoryRoot, "cat-file", "-e", commit+"^{commit}")
	command.Env = gitEnvironment(os.TempDir())
	if err := command.Run(); err != nil {
		return fmt.Errorf("%w: %s (run git fetch origin first)", ErrCommitUnavailable, commit)
	}
	return nil
}

func runGit(directory, home string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = gitEnvironment(home)
	output, err := command.CombinedOutput()
	return string(output), err
}

func gitEnvironment(home string) []string {
	filtered := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "HOME", "XDG_CONFIG_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT", "GIT_ASKPASS", "SSH_ASKPASS":
			continue
		}
		if strings.HasPrefix(strings.ToUpper(name), "TARAKAN_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return append(filtered,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
}

func digestTree(root string) ([sha256.Size]byte, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hash, filepath.ToSlash(relative))
		_, _ = io.WriteString(hash, "\x00"+info.Mode().String()+"\x00")

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
		case info.Mode().IsRegular():
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		_, _ = io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}
