package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/config/latest"
	"github.com/docker/docker-agent/pkg/hooks"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
)

func TestElicitationError_Error(t *testing.T) {
	t.Parallel()

	err := &ElicitationError{Action: "decline", Message: "user said no"}
	assert.Equal(t, "elicitation decline: user said no", err.Error())
}

func TestElicitationBridge_SendBeforeSwapReturnsError(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	err := b.send(Error("nothing"))
	assert.ErrorIs(t, err, errNoElicitationChannel)
}

func TestElicitationBridge_SwapReturnsPrevious(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	first := make(chan Event, 1)
	second := make(chan Event, 1)

	prev := b.swap(first)
	assert.Nil(t, prev, "first swap should return nil prev")

	prev = b.swap(second)
	assert.Equal(t, first, prev, "swap should return the previously stored channel")

	prev = b.swap(nil)
	assert.Equal(t, second, prev, "swap(nil) should return the previously stored channel")
}

func TestElicitationBridge_SendDeliversToCurrentChannel(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	ch := make(chan Event, 1)
	b.swap(ch)

	require.NoError(t, b.send(Error("hello")))

	select {
	case ev := <-ch:
		ee, ok := ev.(*ErrorEvent)
		require.True(t, ok)
		assert.Equal(t, "hello", ee.Error)
	case <-time.After(time.Second):
		t.Fatal("expected event, none received")
	}
}

func TestElicitationBridge_SendRecoversClosedChannel(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	ch := make(chan Event)
	b.swap(ch)
	close(ch)

	err := b.send(Error("closed"))
	assert.ErrorIs(t, err, errNoElicitationChannel)
}

// TestElicitationBridge_RestoreAndCloseWaitsForInflightSenders is the
// regression test for issue #3069: stream teardown must not close an event
// channel while an MCP elicitation goroutine is blocked sending to it.
//
// The test parks a send on the current channel, starts restoreAndClose, and
// verifies teardown cannot close the channel until the parked send drains.
// Running under -race exercises the close-vs-send coordination that used to
// panic with "send on closed channel".
func TestElicitationBridge_RestoreAndCloseWaitsForInflightSenders(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	current := make(chan Event)
	parent := make(chan Event, 1)
	b.swap(current)

	sendStarted := make(chan struct{})
	sendDone := make(chan error, 1)
	go func() {
		close(sendStarted)
		sendDone <- b.send(Error("inflight"))
	}()
	<-sendStarted

	// Give the sender a moment to grab the RLock and park on the channel.
	time.Sleep(20 * time.Millisecond)

	closed := make(chan struct{})
	go func() {
		b.restoreAndClose(current, parent)
		close(closed)
	}()

	select {
	case <-closed:
		t.Fatal("channel closed while a send was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	select {
	case ev := <-current:
		ee, ok := ev.(*ErrorEvent)
		require.True(t, ok)
		assert.Equal(t, "inflight", ee.Error)
	case <-time.After(time.Second):
		t.Fatal("expected in-flight event")
	}

	select {
	case err := <-sendDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("in-flight send never completed after reader drained")
	}

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("restoreAndClose never completed after reader drained")
	}

	select {
	case _, ok := <-current:
		assert.False(t, ok, "current channel should be closed after in-flight send completed")
	default:
		t.Fatal("current channel should be closed")
	}
}

// TestElicitationBridge_ConcurrentSendsAndCloseAreSerializedSafely runs many
// concurrent sends while closing the stream under -race to confirm the bridge
// owns all close-vs-send synchronization.
func TestElicitationBridge_ConcurrentSendsAndCloseAreSerializedSafely(t *testing.T) {
	t.Parallel()

	var b elicitationBridge
	ch := make(chan Event, 64)
	parent := make(chan Event, 1)
	b.swap(ch)

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			for range 5 {
				_ = b.send(Error("x"))
			}
		})
	}

	received := make(chan struct{})
	go func() {
		defer close(received)
		for range ch {
		}
	}()

	wg.Wait()
	b.restoreAndClose(ch, parent)

	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("reader did not observe channel close")
	}
}

func TestLocalRuntime_FinalizeEventChannelEmitsStreamStoppedOnce(t *testing.T) {
	t.Parallel()

	rt := newElicitationTestRuntime(t)
	sess := session.New()
	events := make(chan Event, 1)
	parent := make(chan Event, 1)
	rt.elicitation.swap(events)

	rt.finalizeEventChannel(t.Context(), sess, turnEndReasonNormal, parent, events)

	var stopped int
	for ev := range events {
		if _, ok := ev.(*StreamStoppedEvent); ok {
			stopped++
		}
	}
	assert.Equal(t, 1, stopped, "StreamStopped should be emitted exactly once")
}

func TestLocalRuntime_FinalizeEventChannelDoesNotDeadlockWhenBufferFullAndConsumerGone(t *testing.T) {
	t.Parallel()

	rt := newElicitationTestRuntime(t)
	sess := session.New()
	events := make(chan Event, 1)
	parent := make(chan Event, 1)
	events <- Error("buffer already full")
	rt.elicitation.swap(events)

	done := make(chan struct{})
	go func() {
		rt.finalizeEventChannel(t.Context(), sess, turnEndReasonNormal, parent, events)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("finalizeEventChannel deadlocked with a full buffer and no consumer")
	}

	var stopped int
	for ev := range events {
		if _, ok := ev.(*StreamStoppedEvent); ok {
			stopped++
		}
	}
	assert.Zero(t, stopped, "StreamStopped should be dropped instead of blocking when the buffer is full")
}

// TestRunStreamClosesChannelAndRestoresElicitationOnEarlyReturn is the
// regression test for issue #3073: runStreamLoop swapped this stream's
// events channel into the elicitation bridge before registering the
// finalize defer, so the early-return paths (tool setup failure, a
// user_prompt_submit hook signalling termination) exited without closing
// the events channel or restoring the bridge. A `for range RunStream(...)`
// consumer then hung forever and the bridge kept pointing at the dead
// stream's channel.
//
// We drive the reachable early return — a user_prompt_submit hook that
// stops the run — and assert the consumer's range terminates and the
// previously-swapped elicitation channel is restored.
func TestRunStreamClosesChannelAndRestoresElicitationOnEarlyReturn(t *testing.T) {
	t.Parallel()

	const hookName = "test-stop-user-prompt-submit"
	dontContinue := false

	root := agent.New("root", "test agent",
		agent.WithModel(&mockProvider{id: "test/mock-model"}),
		agent.WithHooks(&latest.HooksConfig{
			UserPromptSubmit: []latest.HookDefinition{
				{Type: "builtin", Command: hookName},
			},
		}),
	)
	rt, err := NewLocalRuntime(team.New(team.WithAgents(root)),
		WithSessionCompaction(false),
		WithModelStore(mockModelStore{}),
	)
	require.NoError(t, err)

	require.NoError(t, rt.hooksRegistry.RegisterBuiltin(
		hookName,
		func(_ context.Context, _ *hooks.Input, _ []string) (*hooks.Output, error) {
			return &hooks.Output{Continue: &dontContinue, StopReason: "stop the run"}, nil
		},
	))

	// Seed a sentinel "parent" elicitation channel. After the stream tears
	// down, the bridge must be restored to this channel — not left pointing
	// at the stream's own (now closed) events channel.
	parent := make(chan Event, 1)
	rt.elicitation.swap(parent)

	sess := session.New(session.WithUserMessage("hi"))
	sess.Title = "Unit Test"

	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range rt.RunStream(t.Context(), sess) {
		}
	}()

	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("RunStream consumer hung: events channel was never closed on the hook-driven early return")
	}

	restored := rt.elicitation.swap(nil)
	assert.Equal(t, parent, restored,
		"the previous elicitation channel must be restored on the early-return path")
}

func newElicitationTestRuntime(t *testing.T) *LocalRuntime {
	t.Helper()

	prov := &mockProvider{id: "test/mock-model"}
	root := agent.New("root", "test", agent.WithModel(prov))
	rt, err := NewLocalRuntime(team.New(team.WithAgents(root)), WithModelStore(mockModelStore{}))
	require.NoError(t, err)
	return rt
}
