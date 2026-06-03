package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsTruthy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"yes", true},
		{"on", true},
		{" true ", true},
		{"0", false},
		{"false", false},
		{"", false},
		{"nope", false},
	} {
		assert.Equal(t, tc.want, isTruthy(tc.in), "input %q", tc.in)
	}
}

func TestEnabled(t *testing.T) {
	t.Setenv(EnvAutoUpdate, "")
	t.Setenv(envReExecMarker, "")
	assert.False(t, Enabled())

	t.Setenv(EnvAutoUpdate, "true")
	assert.True(t, Enabled())

	// The re-exec marker disables updates even when explicitly enabled.
	t.Setenv(envReExecMarker, "1")
	assert.False(t, Enabled())
}

func TestAssetName(t *testing.T) {
	t.Parallel()

	u := &Updater{Repo: "docker-agent", GOOS: "linux", GOARCH: "amd64"}
	assert.Equal(t, "docker-agent-linux-amd64", u.assetName())

	u = &Updater{Repo: "docker-agent", GOOS: "windows", GOARCH: "arm64"}
	assert.Equal(t, "docker-agent-windows-arm64.exe", u.assetName())
}

func TestChecksumFor(t *testing.T) {
	t.Parallel()

	contents := "abc123  docker-agent-linux-amd64\n" +
		"def456 *docker-agent-darwin-arm64\n" +
		"bad999  nested/docker-agent-windows-amd64.exe\n"

	got, ok := checksumFor(contents, "docker-agent-linux-amd64")
	assert.True(t, ok)
	assert.Equal(t, "abc123", got)

	got, ok = checksumFor(contents, "docker-agent-darwin-arm64")
	assert.True(t, ok)
	assert.Equal(t, "def456", got)

	_, ok = checksumFor(contents, "docker-agent-windows-amd64.exe")
	assert.False(t, ok, "path-bearing entries should not match")
}

// newFakeRelease returns an httptest server emulating the GitHub release API
// and download endpoints for the given tag and asset payload.
const testAssetName = "docker-agent-plan9-mips"

func newFakeRelease(t *testing.T, tag string, payload []byte, withChecksums bool) *httptest.Server {
	t.Helper()

	assetName := testAssetName
	sum := sha256.Sum256(payload)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/docker/docker-agent/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q}]}`, tag, assetName, baseURL+"/docker/docker-agent/releases/download/"+tag+"/"+assetName)
	})
	mux.HandleFunc("/docker/docker-agent/releases/download/"+tag+"/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	})
	mux.HandleFunc("/docker/docker-agent/releases/download/"+tag+"/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		if !withChecksums {
			http.NotFound(w, nil)
			return
		}
		_, _ = io.WriteString(w, checksums)
	})

	srv := httptest.NewServer(mux)
	baseURL = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// newTestUpdater wires an Updater against srv, targeting a non-host platform so
// verifyBinary is skipped, and capturing the re-exec call instead of execing.
func newTestUpdater(t *testing.T, srv *httptest.Server, currentVer, exePath string) (*Updater, *reExecCapture) {
	t.Helper()

	capt := &reExecCapture{}
	return &Updater{
		CurrentVersion:  currentVer,
		Owner:           "docker",
		Repo:            "docker-agent",
		APIBaseURL:      srv.URL,
		DownloadBaseURL: srv.URL,
		// Deliberately not the host platform: verifyBinary returns early so we
		// don't try to exec a fake binary.
		GOOS:       "plan9",
		GOARCH:     "mips",
		HTTPClient: srv.Client(),
		resolveExecutable: func() (string, error) {
			return exePath, nil
		},
		reExec:  capt.fn,
		install: installExecutable,
		confirm: func(io.Reader, io.Writer, string, string) bool { return true },
	}, capt
}

type reExecCapture struct {
	mu     sync.Mutex
	called bool
	path   string
	args   []string
	env    []string
}

func (c *reExecCapture) fn(path string, args, env []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.called = true
	c.path = path
	c.args = args
	c.env = env
	return nil
}

func TestTryUpdateSuccess(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new binary\n")
	srv := newFakeRelease(t, "v2.0.0", payload, true)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old binary"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)

	var stderr strings.Builder
	require.NoError(t, u.tryUpdate(t.Context(), nil, &stderr))

	// The on-disk binary was replaced with the downloaded payload.
	got, err := os.ReadFile(exePath)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// And the new binary was re-executed with the loop-guard env marker.
	assert.True(t, capt.called)
	assert.Equal(t, exePath, capt.path)
	assert.Contains(t, capt.env, envReExecMarker+"=1")
}

func TestTryUpdateDeclinedDoesNotUpdate(t *testing.T) {
	payload := []byte("#!/bin/sh\necho new binary\n")
	srv := newFakeRelease(t, "v2.0.0", payload, true)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old binary"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)
	u.confirm = func(io.Reader, io.Writer, string, string) bool { return false }

	var stderr strings.Builder
	require.NoError(t, u.tryUpdate(t.Context(), nil, &stderr))

	assert.False(t, capt.called, "declining must not re-exec")
	got, _ := os.ReadFile(exePath)
	assert.Equal(t, "old binary", string(got), "binary must be untouched when declined")
}

func TestConfirmUpdateNonInteractiveAutoConfirms(t *testing.T) {
	t.Parallel()

	// A non-*os.File reader (e.g. a pipe in CI) is non-interactive: auto-confirm.
	var stderr strings.Builder
	assert.True(t, confirmUpdate(strings.NewReader(""), &stderr, "v1.0.0", "v2.0.0"))
	assert.Empty(t, stderr.String(), "must not prompt in a non-interactive session")
}

func TestAnswerIsYes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"", true}, // default is yes
		{"\n", true},
		{"y", true},
		{"Y", true},
		{"yes", true},
		{" YES ", true},
		{"n", false},
		{"no", false},
		{"nope", false},
		{"x", false},
	} {
		assert.Equal(t, tc.want, answerIsYes(tc.in), "input %q", tc.in)
	}
}

func TestTryUpdateAlreadyLatest(t *testing.T) {
	srv := newFakeRelease(t, "v1.0.0", []byte("x"), true)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)

	var stderr strings.Builder
	require.NoError(t, u.tryUpdate(t.Context(), nil, &stderr))

	assert.False(t, capt.called, "should not re-exec when already up to date")
	got, _ := os.ReadFile(exePath)
	assert.Equal(t, "old", string(got), "binary must be untouched")
}

func TestTryUpdateDevVersionNeverUpdates(t *testing.T) {
	srv := newFakeRelease(t, "v1.0.0", []byte("x"), true)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	u, capt := newTestUpdater(t, srv, devVersion, exePath)

	var stderr strings.Builder
	err := u.tryUpdate(t.Context(), nil, &stderr)
	require.Error(t, err, "dev builds must not be replaced")
	assert.False(t, capt.called)
}

func TestTryUpdateChecksumMismatch(t *testing.T) {
	payload := []byte("real payload")

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	// Server advertises a checksum that does not match the payload.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			fmt.Fprintf(w, `{"tag_name":"v2.0.0","assets":[{"name":"docker-agent-plan9-mips","browser_download_url":%q}]}`, "http://"+r.Host+"/download/docker-agent-plan9-mips")
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprint(w, "deadbeef  docker-agent-plan9-mips\n")
		default:
			_, _ = w.Write(payload)
		}
	}))
	t.Cleanup(bad.Close)

	u, capt := newTestUpdater(t, bad, "v1.0.0", exePath)
	u.APIBaseURL = bad.URL
	u.DownloadBaseURL = bad.URL

	var stderr strings.Builder
	err := u.tryUpdate(t.Context(), nil, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum mismatch")
	assert.False(t, capt.called)

	// The original binary must be intact on failure.
	got, _ := os.ReadFile(exePath)
	assert.Equal(t, "old", string(got))
}

func TestTryUpdateMissingChecksumFailsClosed(t *testing.T) {
	payload := []byte("real payload")
	srv := newFakeRelease(t, "v2.0.0", payload, false)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)

	var stderr strings.Builder
	err := u.tryUpdate(t.Context(), nil, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksums.txt")
	assert.False(t, capt.called)

	got, err := os.ReadFile(exePath)
	require.NoError(t, err)
	assert.Equal(t, "old", string(got))
}

func TestTryUpdateReExecFailureRestoresPreviousBinary(t *testing.T) {
	payload := []byte("new binary")
	srv := newFakeRelease(t, "v2.0.0", payload, true)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old binary"), 0o755))

	u, _ := newTestUpdater(t, srv, "v1.0.0", exePath)
	u.reExec = func(string, []string, []string) error {
		return errors.New("boom")
	}

	var stderr strings.Builder
	err := u.tryUpdate(t.Context(), nil, &stderr)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "re-executing updated binary")

	got, err := os.ReadFile(exePath)
	require.NoError(t, err)
	assert.Equal(t, "old binary", string(got))
}

func TestTryUpdateDownloadNotFound(t *testing.T) {
	// Latest resolves but the asset 404s: must fail and leave binary intact.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/releases/latest") {
			fmt.Fprintf(w, `{"tag_name":"v2.0.0","assets":[{"name":"docker-agent-plan9-mips","browser_download_url":%q}]}`, "http://"+r.Host+"/missing/docker-agent-plan9-mips")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)

	var stderr strings.Builder
	err := u.tryUpdate(t.Context(), nil, &stderr)
	require.Error(t, err)
	assert.False(t, capt.called)
	got, _ := os.ReadFile(exePath)
	assert.Equal(t, "old", string(got))
}

func TestRunSwallowsErrors(t *testing.T) {
	// A totally unreachable server must not panic or propagate: Run is
	// best-effort and only logs.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	exePath := filepath.Join(dir, "docker-agent")
	require.NoError(t, os.WriteFile(exePath, []byte("old"), 0o755))

	u, capt := newTestUpdater(t, srv, "v1.0.0", exePath)

	var stderr strings.Builder
	u.run(t.Context(), nil, &stderr) // must not panic
	assert.False(t, capt.called)
	assert.Contains(t, stderr.String(), "self-update failed")
}

func TestLatestReleaseAuthHeader(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token")

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":"docker-agent-plan9-mips","browser_download_url":%q}]}`, "http://"+r.Host+"/download")
	}))
	t.Cleanup(srv.Close)

	u := &Updater{
		Owner:           "docker",
		Repo:            "docker-agent",
		APIBaseURL:      srv.URL,
		DownloadBaseURL: srv.URL,
		HTTPClient:      srv.Client(),
	}

	release, err := u.latestRelease(t.Context(), "docker-agent-plan9-mips")
	require.NoError(t, err)
	assert.Equal(t, "v9.9.9", release.Tag)
	assert.Equal(t, "Bearer secret-token", gotAuth)
}

func TestLatestReleaseRejectsUntrustedDownloadHost(t *testing.T) {
	// The asset download URL points at an attacker-controlled host while the
	// trusted DownloadBaseURL is the test server: resolution must fail rather
	// than follow the foreign URL.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v9.9.9","assets":[{"name":"docker-agent-plan9-mips","browser_download_url":%q}]}`, "http://evil.example.com/docker-agent-plan9-mips")
	}))
	t.Cleanup(srv.Close)

	u := &Updater{
		Owner:           "docker",
		Repo:            "docker-agent",
		APIBaseURL:      srv.URL,
		DownloadBaseURL: srv.URL,
		HTTPClient:      srv.Client(),
	}

	_, err := u.latestRelease(t.Context(), "docker-agent-plan9-mips")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not the trusted host")
}

func TestValidateDownloadURL(t *testing.T) {
	t.Parallel()

	u := &Updater{DownloadBaseURL: "https://github.com"}

	require.NoError(t, u.validateDownloadURL("https://github.com/docker/docker-agent/releases/download/v1/asset"))
	require.NoError(t, u.validateDownloadURL("https://GitHub.com/docker/docker-agent/releases/download/v1/asset"))

	require.Error(t, u.validateDownloadURL("https://objects.githubusercontent.com/asset"), "foreign host must be rejected")
	require.Error(t, u.validateDownloadURL("http://github.com/asset"), "non-HTTPS must be rejected")
	require.Error(t, u.validateDownloadURL("https://evil.example.com/asset"))
	require.Error(t, u.validateDownloadURL("://bad"))
}

func TestSelfUpdateEnvStripsMarkers(t *testing.T) {
	t.Parallel()

	in := []string{
		"PATH=/usr/bin",
		envBackupMarker + "=/tmp/stale",
		"HOME=/home/me",
		envReExecMarker + "=1",
	}

	got := selfUpdateEnv(in)
	assert.Equal(t, []string{"PATH=/usr/bin", "HOME=/home/me"}, got)

	// Appending fresh markers must yield exactly one entry for each key.
	full := append(selfUpdateEnv(in), envReExecMarker+"=1", envBackupMarker+"=/tmp/new")
	assert.Equal(t, 1, countKey(full, envReExecMarker))
	assert.Equal(t, 1, countKey(full, envBackupMarker))
	assert.Contains(t, full, envBackupMarker+"=/tmp/new")
}

func countKey(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k == key {
			n++
		}
	}
	return n
}

func TestCleanupRemovesBackup(t *testing.T) {
	backup := filepath.Join(t.TempDir(), backupFilePrefix+"123")
	require.NoError(t, os.WriteFile(backup, []byte("old"), 0o755))
	t.Setenv(envBackupMarker, backup)

	Cleanup(t.Context())

	_, err := os.Stat(backup)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestCleanupIgnoresForeignBackupPath(t *testing.T) {
	// A path that does not look like one of our backups must never be removed,
	// even if pointed at by the environment variable.
	victim := filepath.Join(t.TempDir(), "important.txt")
	require.NoError(t, os.WriteFile(victim, []byte("keep"), 0o644))
	t.Setenv(envBackupMarker, victim)

	Cleanup(t.Context())

	got, err := os.ReadFile(victim)
	require.NoError(t, err, "foreign path must not be deleted")
	assert.Equal(t, "keep", string(got))
}

func TestIsOwnedBackupPath(t *testing.T) {
	t.Parallel()

	assert.True(t, isOwnedBackupPath("/tmp/"+backupFilePrefix+"abc"))
	assert.True(t, isOwnedBackupPath(backupFilePrefix+"abc"))
	assert.False(t, isOwnedBackupPath("/tmp/important.txt"))
	assert.False(t, isOwnedBackupPath("/etc/passwd"))
	assert.False(t, isOwnedBackupPath(""))
}

func TestSwapBinary(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "docker-agent")
	src := filepath.Join(dir, "staged")
	require.NoError(t, os.WriteFile(dst, []byte("old"), 0o755))
	require.NoError(t, os.WriteFile(src, []byte("new"), 0o755))

	require.NoError(t, swapBinary(dst, src))

	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
}
