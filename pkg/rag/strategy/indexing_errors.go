package strategy

import (
	"context"
	"errors"
	"fmt"

	"github.com/docker/docker-agent/pkg/modelerrors"
)

// errIndexingAborted marks a permanent model/provider failure (e.g. invalid
// model name, authentication failure, rate limit) encountered during indexing.
// When such an error occurs, the whole indexing run must stop immediately:
// every remaining file/chunk would trigger the same failing request, flooding
// the provider (see https://github.com/docker/docker-agent/issues/3082).
var errIndexingAborted = errors.New("indexing aborted due to non-retryable model error")

// classifyModelCallError inspects an error returned by an embedding or LLM
// call made during indexing. Permanent failures are wrapped with
// errIndexingAborted so callers can abort the run; transient failures (5xx,
// timeouts) and context cancellation are returned unchanged so callers can
// skip the current file and continue.
func classifyModelCallError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// Rate-limited (429) errors are also non-retryable here: continuing to
	// index would keep hammering a provider that asked us to back off.
	retryable, _, _ := modelerrors.ClassifyModelError(err)
	if !retryable {
		return fmt.Errorf("%w: %w", errIndexingAborted, err)
	}
	return err
}

// isIndexingAborted reports whether err carries the errIndexingAborted marker.
func isIndexingAborted(err error) bool {
	return errors.Is(err, errIndexingAborted)
}
