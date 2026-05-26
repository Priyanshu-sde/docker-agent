package runtime

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
)

// trackingStore wraps an in-memory store so a test can detect Close() calls.
type trackingStore struct {
	session.Store

	closes atomic.Int32
}

func (s *trackingStore) Close() error {
	s.closes.Add(1)
	return s.Store.Close()
}

// TestRuntimeClose_DoesNotCloseExternalSessionStore is a regression test for
// the bug fixed in #2872: closing one runtime would close the SQLite session
// store shared with other runtimes (e.g. spawned by the TUI's session
// browser), making subsequent reads fail with "sql: database is closed".
func TestRuntimeClose_DoesNotCloseExternalSessionStore(t *testing.T) {
	store := &trackingStore{Store: session.NewInMemorySessionStore()}

	prov := &mockProvider{id: "test/mock-model", stream: &mockStream{}}
	root := agent.New("root", "You are a test agent", agent.WithModel(prov))
	tm := team.New(team.WithAgents(root))

	rt, err := New(tm,
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
		WithSessionStore(store),
	)
	require.NoError(t, err)

	require.NoError(t, rt.Close())
	assert.Zero(t, store.closes.Load(),
		"runtime.Close() must not close an externally supplied session store; "+
			"the store may be shared with other runtimes")

	// The store is still usable after the runtime is closed.
	_, err = store.GetSessionSummaries(t.Context())
	require.NoError(t, err)
}
