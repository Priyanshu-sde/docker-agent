package lifecycle

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/docker/docker-agent/pkg/config/latest"
)

func TestPolicyFromConfig_NilUsesResilientDefaults(t *testing.T) {
	t.Parallel()
	p := PolicyFromConfig("test", nil)
	assert.Equal(t, RestartOnFailure, p.Restart)
	assert.Equal(t, 5, p.MaxAttempts)
	assert.NotNil(t, p.Logger)
}

func TestPolicyFromConfig_StrictProfile(t *testing.T) {
	t.Parallel()
	p := PolicyFromConfig("test", &latest.LifecycleConfig{
		Profile: latest.LifecycleProfileStrict,
	})
	assert.Equal(t, RestartNever, p.Restart)
	assert.Equal(t, -1, p.MaxAttempts)
}

func TestPolicyFromConfig_BestEffortProfile(t *testing.T) {
	t.Parallel()
	p := PolicyFromConfig("test", &latest.LifecycleConfig{
		Profile: latest.LifecycleProfileBestEffort,
	})
	assert.Equal(t, RestartNever, p.Restart)
}

func TestPolicyFromConfig_ExplicitOverrides(t *testing.T) {
	t.Parallel()
	cfg := &latest.LifecycleConfig{
		Profile:     latest.LifecycleProfileResilient,
		Restart:     "always",
		MaxRestarts: 12,
		Backoff: &latest.BackoffConfig{
			Initial:    latest.Duration{Duration: 500 * time.Millisecond},
			Max:        latest.Duration{Duration: 10 * time.Second},
			Multiplier: 1.5,
			Jitter:     0.3,
		},
	}
	p := PolicyFromConfig("test", cfg)
	assert.Equal(t, RestartAlways, p.Restart)
	assert.Equal(t, 12, p.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, p.Backoff.Initial)
	assert.Equal(t, 10*time.Second, p.Backoff.Max)
	assert.InDelta(t, 1.5, p.Backoff.Multiplier, 0.001)
	assert.InDelta(t, 0.3, p.Backoff.Jitter, 0.001)
}

func TestPolicyFromConfig_PartialOverridesKeepProfileDefaults(t *testing.T) {
	t.Parallel()
	cfg := &latest.LifecycleConfig{
		Profile:     latest.LifecycleProfileResilient,
		MaxRestarts: 7,
	}
	p := PolicyFromConfig("test", cfg)
	assert.Equal(t, RestartOnFailure, p.Restart, "profile default preserved")
	assert.Equal(t, 7, p.MaxAttempts, "explicit override applied")
}

func TestPolicyFromConfig_StartupTimeout(t *testing.T) {
	t.Parallel()

	// Nil config and non-strict profiles default to no timeout.
	assert.Equal(t, time.Duration(0), PolicyFromConfig("test", nil).StartupTimeout)
	assert.Equal(t, time.Duration(0), PolicyFromConfig("test", &latest.LifecycleConfig{
		Profile: latest.LifecycleProfileResilient,
	}).StartupTimeout)

	// The strict profile carries a 30s default.
	assert.Equal(t, 30*time.Second, PolicyFromConfig("test", &latest.LifecycleConfig{
		Profile: latest.LifecycleProfileStrict,
	}).StartupTimeout)

	// An explicit value wins over the profile default.
	assert.Equal(t, 5*time.Second, PolicyFromConfig("test", &latest.LifecycleConfig{
		StartupTimeout: latest.Duration{Duration: 5 * time.Second},
	}).StartupTimeout)
}

func TestParseRestart(t *testing.T) {
	t.Parallel()
	cases := map[string]Restart{
		"":           RestartOnFailure,
		"on_failure": RestartOnFailure,
		"never":      RestartNever,
		"always":     RestartAlways,
	}
	for in, want := range cases {
		assert.Equal(t, want, ParseRestart(in), "input=%q", in)
	}
}
