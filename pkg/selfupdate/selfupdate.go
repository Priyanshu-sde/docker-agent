// Package selfupdate implements an opt-in, fail-safe self-update mechanism.
//
// When enabled via the [EnvAutoUpdate] environment variable, the running
// binary checks GitHub for a newer release, downloads the asset matching the
// current OS/arch, verifies it, atomically replaces itself on disk, and
// re-executes the new binary with the original arguments.
//
// The whole mechanism is best-effort: any failure (network, disk, permissions,
// version parsing, a corrupt download, ...) is logged and swallowed so the
// caller always falls back to running the current binary. The only observable
// effect of a failed update is a short delay and a log line.
package selfupdate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/mattn/go-isatty"

	"github.com/docker/docker-agent/pkg/atomicfile"
)

const (
	// EnvAutoUpdate enables the self-update mechanism when set to a truthy
	// value ("1", "true", "yes", "on", case-insensitive).
	EnvAutoUpdate = "DOCKER_AGENT_AUTO_UPDATE"

	// envReExecMarker is set on the re-executed child process to prevent an
	// infinite update→re-exec loop. Its presence means "an update has already
	// been attempted in this process tree, do not try again".
	envReExecMarker = "DOCKER_AGENT_SELF_UPDATE_REEXEC"

	// envBackupMarker points the re-executed child at the previous binary backup
	// so it can clean up after a successful restart.
	envBackupMarker = "DOCKER_AGENT_SELF_UPDATE_BACKUP"

	defaultRepoOwner = "docker"
	defaultRepoName  = "docker-agent"

	defaultAPIBaseURL      = "https://api.github.com"
	defaultDownloadBaseURL = "https://github.com"

	// devVersion marks a local/unreleased build that must never be updated.
	devVersion = "dev"

	// httpTimeout bounds every network operation. The full update budget is
	// roughly downloadTimeout for the asset plus this for metadata.
	httpTimeout     = 30 * time.Second
	downloadTimeout = 5 * time.Minute

	// maxBinarySize caps the downloaded asset to defeat a malicious or broken
	// release from filling the disk. docker-agent binaries are tens of MiB.
	maxBinarySize = 512 << 20 // 512 MiB
)

// Updater performs a single best-effort self-update. The zero value is not
// usable; call [New] to get one wired with sensible defaults.
type Updater struct {
	CurrentVersion string
	Owner          string
	Repo           string

	APIBaseURL      string
	DownloadBaseURL string

	GOOS   string
	GOARCH string

	HTTPClient *http.Client

	// resolveExecutable returns the absolute, symlink-resolved path of the
	// binary to replace. Overridable in tests.
	resolveExecutable func() (string, error)

	// install replaces the current executable with the staged binary and returns
	// the previous binary backup plus a restore function that puts it back on
	// disk if the restart step fails. Overridable in tests.
	install func(dst, src string) (func() error, string, error)

	// reExec replaces (Unix) or relaunches (Windows) the current process with
	// path, args and env. On success it does not return on Unix; on Windows it
	// exits the process with the child's status. Overridable in tests.
	reExec func(path string, args, env []string) error

	// confirm asks the user whether to install the available update. It returns
	// true to proceed. In non-interactive sessions it must auto-confirm.
	// Overridable in tests.
	confirm func(stdin io.Reader, stderr io.Writer, current, latest string) bool
}

// New returns an Updater configured for the docker-agent GitHub repository,
// targeting the current binary and platform.
func New(currentVersion string) *Updater {
	return &Updater{
		CurrentVersion:    currentVersion,
		Owner:             defaultRepoOwner,
		Repo:              defaultRepoName,
		APIBaseURL:        defaultAPIBaseURL,
		DownloadBaseURL:   defaultDownloadBaseURL,
		GOOS:              runtime.GOOS,
		GOARCH:            runtime.GOARCH,
		HTTPClient:        &http.Client{Timeout: downloadTimeout},
		resolveExecutable: resolveExecutable,
		install:           installExecutable,
		reExec:            reExecProcess,
		confirm:           confirmUpdate,
	}
}

// Enabled reports whether the self-update mechanism should run for this
// process. It is false when disabled by env, or when this process is already
// the re-executed child of a prior update (loop guard).
func Enabled() bool {
	if os.Getenv(envReExecMarker) != "" {
		return false
	}
	return isTruthy(os.Getenv(EnvAutoUpdate))
}

// Run attempts a self-update. It never returns an error: on success it
// re-executes the new binary (and does not return on Unix); on any failure or
// when already up to date it returns so the caller can continue with the
// current binary. Progress and failures are reported to stderr and slog.
//
// When a newer release is available and the session is interactive, the user
// is prompted to confirm the upgrade; non-interactive sessions auto-confirm.
func Run(ctx context.Context, stdin io.Reader, stderr io.Writer) {
	if !Enabled() {
		return
	}
	New(currentVersion()).run(ctx, stdin, stderr)
}

// run is the testable core. It logs and swallows every error.
func (u *Updater) run(ctx context.Context, stdin io.Reader, stderr io.Writer) {
	if err := u.tryUpdate(ctx, stdin, stderr); err != nil {
		slog.WarnContext(ctx, "Self-update skipped; continuing with current binary", "error", err)
		fmt.Fprintf(stderr, "docker-agent: self-update failed (%v); continuing with current version\n", err)
	}
}

func (u *Updater) tryUpdate(ctx context.Context, stdin io.Reader, stderr io.Writer) error {
	current, err := semver.NewVersion(u.CurrentVersion)
	if err != nil {
		// "dev" and other non-release versions land here. Never clobber a
		// local build the developer compiled themselves.
		return fmt.Errorf("current version %q is not a release version: %w", u.CurrentVersion, err)
	}

	assetName := u.assetName()
	release, err := u.latestRelease(ctx, assetName)
	if err != nil {
		return fmt.Errorf("resolving latest release: %w", err)
	}

	latest, err := semver.NewVersion(release.Tag)
	if err != nil {
		return fmt.Errorf("parsing latest tag %q: %w", release.Tag, err)
	}

	if !latest.GreaterThan(current) {
		slog.DebugContext(ctx, "Already on the latest version", "current", u.CurrentVersion, "latest", release.Tag)
		return nil
	}

	if !u.confirm(stdin, stderr, u.CurrentVersion, release.Tag) {
		slog.DebugContext(ctx, "User declined self-update", "current", u.CurrentVersion, "latest", release.Tag)
		return nil
	}

	exePath, err := u.resolveExecutable()
	if err != nil {
		return fmt.Errorf("locating current executable: %w", err)
	}

	fmt.Fprintf(stderr, "docker-agent: updating from %s to %s...\n", u.CurrentVersion, release.Tag)

	newPath, err := u.downloadAndStage(ctx, release, exePath)
	if err != nil {
		return err
	}

	if err := u.verifyBinary(ctx, newPath); err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("verifying downloaded binary: %w", err)
	}

	restore, backup, err := u.install(exePath, newPath)
	if err != nil {
		_ = os.Remove(newPath)
		return fmt.Errorf("replacing executable: %w", err)
	}

	fmt.Fprintf(stderr, "docker-agent: updated to %s, restarting...\n", release.Tag)

	// Re-execute the freshly installed binary with the original arguments,
	// marking the child so it will not attempt to update again. Strip any
	// inherited self-update markers first so a stale backup path from a prior
	// cycle cannot shadow the one we set (getenv returns the first match).
	env := append(selfUpdateEnv(os.Environ()), envReExecMarker+"=1", envBackupMarker+"="+backup)
	if err := u.reExec(exePath, os.Args, env); err != nil {
		if restoreErr := restore(); restoreErr != nil {
			return fmt.Errorf("re-executing updated binary and restoring previous binary: %w", errors.Join(err, restoreErr))
		}
		return fmt.Errorf("re-executing updated binary: %w", err)
	}
	return nil
}

// assetName returns the release asset filename for the target platform, e.g.
// "docker-agent-darwin-arm64" or "docker-agent-windows-amd64.exe".
func (u *Updater) assetName() string {
	name := fmt.Sprintf("%s-%s-%s", u.Repo, u.GOOS, u.GOARCH)
	if u.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

type releaseInfo struct {
	Tag         string
	Asset       string
	DownloadURL string
}

// latestRelease fetches the latest GitHub release metadata and locates the
// asset matching the current platform.
func (u *Updater) latestRelease(ctx context.Context, assetName string) (releaseInfo, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", u.APIBaseURL, u.Owner, u.Repo)

	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return releaseInfo{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	setGitHubAuth(req)

	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return releaseInfo{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return releaseInfo{}, fmt.Errorf("GitHub API returned HTTP %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&release); err != nil {
		return releaseInfo{}, fmt.Errorf("decoding release metadata: %w", err)
	}
	if release.TagName == "" {
		return releaseInfo{}, errors.New("latest release has no tag_name")
	}

	for _, asset := range release.Assets {
		if asset.Name == assetName {
			if asset.BrowserDownloadURL == "" {
				return releaseInfo{}, fmt.Errorf("release asset %s has no download URL", assetName)
			}
			if err := u.validateDownloadURL(asset.BrowserDownloadURL); err != nil {
				return releaseInfo{}, err
			}
			return releaseInfo{
				Tag:         release.TagName,
				Asset:       asset.Name,
				DownloadURL: asset.BrowserDownloadURL,
			}, nil
		}
	}

	return releaseInfo{}, fmt.Errorf("latest release %s does not contain asset %s", release.TagName, assetName)
}

// validateDownloadURL rejects asset URLs that do not point at the trusted
// download host. The asset URL comes from the GitHub API response, so a
// tampered or compromised response could otherwise redirect the binary
// download to an attacker-controlled host. The trusted host is derived from
// the hardcoded DownloadBaseURL (github.com in production), and the scheme of
// that base URL is enforced too.
func (u *Updater) validateDownloadURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing asset download URL: %w", err)
	}
	base, err := url.Parse(u.DownloadBaseURL)
	if err != nil {
		return fmt.Errorf("parsing download base URL: %w", err)
	}
	if parsed.Scheme != base.Scheme {
		return fmt.Errorf("asset download URL %q scheme is not %q", rawURL, base.Scheme)
	}
	if !strings.EqualFold(parsed.Hostname(), base.Hostname()) {
		return fmt.Errorf("asset download URL host %q is not the trusted host %q", parsed.Hostname(), base.Hostname())
	}
	return nil
}

// downloadAndStage downloads the release asset into a temp file next to exePath
// (same filesystem, so the later rename is atomic) and returns its path.
func (u *Updater) downloadAndStage(ctx context.Context, release releaseInfo, exePath string) (string, error) {
	dlURL := release.DownloadURL

	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(dlCtx, http.MethodGet, dlURL, http.NoBody)
	if err != nil {
		return "", err
	}
	setGitHubAuth(req)

	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", dlURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", dlURL, resp.StatusCode)
	}

	dir := filepath.Dir(exePath)
	tmp, err := os.CreateTemp(dir, ".docker-agent-update-*")
	if err != nil {
		// Fall back to the system temp dir; replaceExecutable copies across
		// filesystems when an atomic rename is not possible.
		tmp, err = os.CreateTemp("", ".docker-agent-update-*")
		if err != nil {
			return "", fmt.Errorf("creating temp file: %w", err)
		}
	}
	tmpPath := tmp.Name()

	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(resp.Body, maxBinarySize+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("writing downloaded binary: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("closing downloaded binary: %w", closeErr)
	}
	if n == 0 {
		_ = os.Remove(tmpPath)
		return "", errors.New("downloaded binary is empty")
	}
	if n > maxBinarySize {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("downloaded binary exceeds %d bytes", int64(maxBinarySize))
	}

	if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec // replacement binary must be executable
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("setting executable permissions: %w", err)
	}

	// Integrity check is mandatory for self-update: do not execute or install a
	// downloaded binary unless GitHub provides a digest or the release publishes
	// a matching SHA-256 entry in checksums.txt.
	if err := u.verifyChecksum(ctx, release, hex.EncodeToString(hasher.Sum(nil))); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}

	return tmpPath, nil
}

// verifyChecksum verifies gotHex against the SHA-256 listed for the asset in
// the release's checksums.txt, fetched from the hardcoded DownloadBaseURL
// rather than any API-supplied value. It fails closed when checksums.txt is
// missing or does not list the asset, so a tampered API response cannot
// substitute its own digest for a malicious binary.
func (u *Updater) verifyChecksum(ctx context.Context, release releaseInfo, gotHex string) error {
	endpoint := fmt.Sprintf("%s/%s/%s/releases/download/%s/checksums.txt", u.DownloadBaseURL, u.Owner, u.Repo, release.Tag)

	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	setGitHubAuth(req)

	resp, err := u.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetching checksums.txt: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching checksums.txt: HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading checksums.txt: %w", err)
	}

	want, ok := checksumFor(string(body), release.Asset)
	if !ok {
		return fmt.Errorf("checksums.txt does not list %s", release.Asset)
	}

	if !strings.EqualFold(want, gotHex) {
		return fmt.Errorf("checksum mismatch for %s: expected %s, got %s", release.Asset, want, gotHex)
	}
	return nil
}

// checksumFor parses a "sha256  filename" formatted checksums file and returns
// the hex digest for the given asset.
func checksumFor(contents, asset string) (string, bool) {
	for line := range strings.Lines(contents) {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// The filename column may carry a leading "*" (binary mode marker).
		name := strings.TrimPrefix(fields[1], "*")
		if name == asset {
			return fields[0], true
		}
	}
	return "", false
}

// verifyBinary sanity-checks the staged binary by executing it with the
// "version" subcommand. A binary that cannot even print its version (wrong
// platform, truncated download, ...) must not replace the working one.
func (u *Updater) verifyBinary(ctx context.Context, path string) error {
	// Skip when staged for another platform (tests): we cannot run it here.
	if u.GOOS != runtime.GOOS || u.GOARCH != runtime.GOARCH {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "version")
	// Mark the probe so the freshly downloaded binary does not recursively
	// attempt its own self-update while we are validating it. Keep the
	// environment minimal so the probe cannot read model/provider secrets.
	cmd.Env = []string{envReExecMarker + "=1"}
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"), "PATH="+os.Getenv("PATH"))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("staged binary failed to run (%w): %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// resolveExecutable returns the absolute, symlink-resolved path of the running
// binary. Resolving symlinks ensures we replace the real file (e.g. the
// Homebrew/cli-plugins target) rather than a link to it.
func resolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Abs(exe)
}

// backupFilePrefix is the basename prefix of the temp backup created by
// backupExecutable. Cleanup only removes paths matching it so a hostile or
// accidental DOCKER_AGENT_SELF_UPDATE_BACKUP value cannot delete arbitrary
// files on startup.
const backupFilePrefix = ".docker-agent-backup-"

// Cleanup removes the previous-binary backup after a successful re-exec. It is
// deliberately best-effort: cleanup failure must never block normal execution.
//
// The backup path comes from an environment variable, so it is validated to
// look like a backup file produced by this package before being removed. This
// prevents an attacker-controlled environment from turning startup into an
// arbitrary file deletion.
func Cleanup(ctx context.Context) {
	backup := os.Getenv(envBackupMarker)
	if backup == "" || !isOwnedBackupPath(backup) {
		return
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.DebugContext(ctx, "Could not remove self-update backup", "path", backup, "error", err)
	}
}

// isOwnedBackupPath reports whether p looks like a backup file created by
// backupExecutable, i.e. its basename carries the expected prefix.
func isOwnedBackupPath(p string) bool {
	return strings.HasPrefix(filepath.Base(p), backupFilePrefix)
}

// installExecutable swaps the binary at dst with the staged file at src and
// returns a restore function that can put the previous binary back if restart
// fails. The platform-specific details (a running binary cannot be overwritten
// on Windows) live in swapBinary's platform implementations.
func installExecutable(dst, src string) (func() error, string, error) {
	backup, err := backupExecutable(dst)
	if err != nil {
		return nil, "", fmt.Errorf("backing up current executable: %w", err)
	}

	if err := swapBinary(dst, src); err != nil {
		_ = os.Remove(backup)
		return nil, "", err
	}

	restored := false
	return func() error {
		if restored {
			return nil
		}
		restored = true
		defer os.Remove(backup)
		return swapBinary(dst, backup)
	}, backup, nil
}

func backupExecutable(path string) (string, error) {
	in, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer in.Close()

	tmp, err := os.CreateTemp(filepath.Dir(path), backupFilePrefix+"*")
	if err != nil {
		return "", err
	}
	backup := tmp.Name()

	_, copyErr := io.Copy(tmp, in)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(backup)
		switch {
		case copyErr != nil:
			return "", copyErr
		case syncErr != nil:
			return "", syncErr
		default:
			return "", closeErr
		}
	}

	if err := os.Chmod(backup, 0o755); err != nil { //nolint:gosec // backup must be executable if restored
		_ = os.Remove(backup)
		return "", err
	}
	return backup, nil
}

// atomicWriteFromFile copies src into dst atomically (used by the Windows path
// and by the cross-filesystem fallback). Kept here so both platform files can
// share it.
func atomicWriteFromFile(dst, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return atomicfile.Write(dst, f, 0o755)
}

// confirmUpdate asks the user whether to install the available update. On a
// non-interactive session (stdin is not a terminal, e.g. CI or piped input) it
// auto-confirms so automation keeps working. On an interactive session it
// prompts and treats anything other than an explicit "yes" as a decline.
func confirmUpdate(stdin io.Reader, stderr io.Writer, current, latest string) bool {
	if !isInteractive(stdin) {
		return true
	}

	fmt.Fprintf(stderr, "An update is available (%s). Do you want to install it or continue with version %s? [Y/n] ", latest, current)

	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		// Could not read an answer (EOF on an empty line): be conservative and
		// keep running the current version rather than upgrading unprompted.
		return false
	}

	return answerIsYes(line)
}

// answerIsYes reports whether a prompt answer means "proceed". The prompt
// defaults to yes, so an empty answer confirms; anything other than y/yes
// declines.
func answerIsYes(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

// isInteractive reports whether stdin is a terminal we can prompt on. It probes
// for an Fd() accessor (satisfied by *os.File and by common wrappers) rather
// than asserting a concrete *os.File, so a wrapped real terminal is still
// detected. A reader without a file descriptor (e.g. a bytes buffer in tests)
// or a non-terminal descriptor (pipe, redirect, CI) is non-interactive.
func isInteractive(stdin io.Reader) bool {
	f, ok := stdin.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// selfUpdateEnv returns environ with every DOCKER_AGENT_SELF_UPDATE_* entry
// removed so the caller can append fresh markers without leaving duplicates.
// Duplicates matter because getenv returns the first match, so a stale backup
// path inherited from a prior update cycle would otherwise shadow the new one
// and leak the temp backup file.
func selfUpdateEnv(environ []string) []string {
	filtered := make([]string, 0, len(environ))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if key == envReExecMarker || key == envBackupMarker {
			continue
		}
		filtered = append(filtered, kv)
	}
	return filtered
}

// isTruthy reports whether s represents an enabled boolean flag.
func isTruthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// setGitHubAuth adds a bearer token from the environment when available,
// raising the GitHub rate limit and enabling access to private assets.
func setGitHubAuth(req *http.Request) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
