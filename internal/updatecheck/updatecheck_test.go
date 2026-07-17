package updatecheck

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"0.2.0", "0.2.0", 0},
		{"v0.2.0", "0.2.0", 0},
		{"0.2.0", "0.2.1", -1},
		{"0.2.1", "0.2.0", 1},
		{"0.1.9", "0.2.0", -1},
		{"1.0.0", "0.9.9", 1},
		{"0.2", "0.2.0", 0},
		{"0.2.0", "0.2.0.1", -1},
	}
	for _, tc := range tests {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestResultNotice(t *testing.T) {
	r := Result{Current: "0.2.0", Latest: "0.2.2", UpdateAvailable: true}
	notice := r.Notice()
	for _, want := range []string{"v0.2.2", "v0.2.0", InstallHint} {
		if !strings.Contains(notice, want) {
			t.Fatalf("notice missing %q: %s", want, notice)
		}
	}
	if (Result{}).Notice() != "" {
		t.Fatal("empty result should have no notice")
	}
}

func TestCheckUsesCache(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// UserConfigDir on some platforms ignores XDG; write via cachePath after override.
	// Force cache into temp by monkeying through write/read with HOME/XDG.
	// On Linux, UserConfigDir uses XDG_CONFIG_HOME when set.
	path := filepath.Join(dir, "tarakan", "update-check.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cacheFile{Latest: "0.9.0", CheckedAt: time.Now().UTC()})
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	// If UserConfigDir doesn't use our dir, skip (platform quirk).
	if p, err := cachePath(); err != nil || p != path {
		t.Skipf("cache path %q != %q (platform config dir)", p, path)
	}

	result, err := Check(context.Background(), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.Latest != "0.9.0" {
		t.Fatalf("expected cached update, got %+v", result)
	}
}

func TestCheckFetchesGitHubAndDetectsUpdate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if p, err := cachePath(); err != nil || !strings.HasPrefix(p, dir) {
		t.Skip("UserConfigDir does not honor XDG_CONFIG_HOME on this platform")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/atomine-elektrine/tarakan-client/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v0.3.0"})
	}))
	defer server.Close()

	prev := githubAPIBase
	githubAPIBase = server.URL
	t.Cleanup(func() { githubAPIBase = prev })

	result, err := Check(context.Background(), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.Latest != "0.3.0" {
		t.Fatalf("unexpected result: %+v", result)
	}

	// Second call should use cache (still works if server goes away).
	server.Close()
	result2, err := Check(context.Background(), "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if result2.Latest != "0.3.0" {
		t.Fatalf("cache miss: %+v", result2)
	}
}

func TestMaybeNotifySkipsWhenDisabled(t *testing.T) {
	t.Setenv("TARAKAN_SKIP_UPDATE_CHECK", "1")
	var b strings.Builder
	MaybeNotify(&b, "0.0.1")
	if b.Len() != 0 {
		t.Fatalf("expected silence when skip set, got %q", b.String())
	}
}

func TestSkipEnabled(t *testing.T) {
	t.Setenv("TARAKAN_SKIP_UPDATE_CHECK", "true")
	if !skipEnabled() {
		t.Fatal("expected skip")
	}
}
