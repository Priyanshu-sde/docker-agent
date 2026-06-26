// Package sound provides cross-platform sound notification support.
// It plays system sounds asynchronously to notify users of task completion or failure.
package sound

import (
	"context"
	"log/slog"
	"os/exec"
	"runtime"
)

// Event represents the type of sound to play.
type Event int

const (
	// Success is played when a task completes successfully.
	Success Event = iota
	// Failure is played when a task fails.
	Failure
)

// Play plays a notification sound for the given event in the background.
// It is non-blocking and safe to call from any goroutine.
// If the sound cannot be played, the error is logged and silently ignored.
func Play(ctx context.Context, event Event) {
	go func() {
		ctx := context.WithoutCancel(ctx)
		if err := playSound(ctx, event); err != nil {
			slog.DebugContext(ctx, "Failed to play sound", "event", event, "error", err)
		}
	}()
}

func playSound(ctx context.Context, event Event) error {
	switch runtime.GOOS {
	case "darwin":
		return playDarwin(ctx, event)
	case "linux":
		return playLinux(ctx, event)
	case "windows":
		return playWindows(ctx, event)
	default:
		return nil
	}
}

// runDetached executes a command for fire-and-forget audio playback. The
// process is short-lived and not tied to caller cancellation.
func runDetached(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

func playDarwin(ctx context.Context, event Event) error {
	// Use macOS built-in system sounds via afplay
	var soundFile string
	switch event {
	case Success:
		soundFile = "/System/Library/Sounds/Glass.aiff"
	case Failure:
		soundFile = "/System/Library/Sounds/Basso.aiff"
	}
	return runDetached(ctx, "afplay", soundFile)
}

func playLinux(ctx context.Context, event Event) error {
	// Try paplay (PulseAudio) first, then fall back to terminal bell
	var soundFile string
	switch event {
	case Success:
		soundFile = "/usr/share/sounds/freedesktop/stereo/complete.oga"
	case Failure:
		soundFile = "/usr/share/sounds/freedesktop/stereo/dialog-error.oga"
	}

	if path, err := exec.LookPath("paplay"); err == nil {
		return runDetached(ctx, path, soundFile)
	}

	// Fallback: terminal bell via printf
	return runDetached(ctx, "printf", `\a`)
}

func playWindows(ctx context.Context, event Event) error {
	// Use PowerShell to play system sounds
	var script string
	switch event {
	case Success:
		script = `[System.Media.SystemSounds]::Asterisk.Play()`
	case Failure:
		script = `[System.Media.SystemSounds]::Hand.Play()`
	}
	return runDetached(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}
