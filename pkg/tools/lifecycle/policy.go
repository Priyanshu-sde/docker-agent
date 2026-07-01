package lifecycle

import (
	"log/slog"

	"github.com/docker/docker-agent/pkg/config/latest"
)

// PolicyFromConfig converts a latest.LifecycleConfig into a Policy. nil cfg
// returns the resilient default policy.
func PolicyFromConfig(name string, cfg *latest.LifecycleConfig) Policy {
	policy := profilePolicy(profileName(cfg))
	policy.Logger = slog.With("component", "supervisor", "toolset", name)
	policy.StartupTimeout = cfg.EffectiveStartupTimeout()

	if cfg == nil {
		return policy
	}
	if cfg.Restart != "" {
		policy.Restart = ParseRestart(cfg.Restart)
	}
	if cfg.MaxRestarts != 0 {
		policy.MaxAttempts = cfg.MaxRestarts
	}
	if b := cfg.Backoff; b != nil {
		if b.Initial.Duration > 0 {
			policy.Backoff.Initial = b.Initial.Duration
		}
		if b.Max.Duration > 0 {
			policy.Backoff.Max = b.Max.Duration
		}
		if b.Multiplier > 0 {
			policy.Backoff.Multiplier = b.Multiplier
		}
		if b.Jitter > 0 {
			policy.Backoff.Jitter = b.Jitter
		}
	}
	return policy
}

func profileName(cfg *latest.LifecycleConfig) string {
	if cfg == nil || cfg.Profile == "" {
		return latest.LifecycleProfileResilient
	}
	return cfg.Profile
}

func profilePolicy(profile string) Policy {
	switch profile {
	case latest.LifecycleProfileStrict, latest.LifecycleProfileBestEffort:
		return Policy{Restart: RestartNever, MaxAttempts: -1}
	default:
		return Policy{Restart: RestartOnFailure, MaxAttempts: 5}
	}
}

// ParseRestart converts a YAML restart string into the supervisor enum.
func ParseRestart(s string) Restart {
	switch s {
	case "never":
		return RestartNever
	case "always":
		return RestartAlways
	default:
		return RestartOnFailure
	}
}
