package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/agent"
	"github.com/docker/docker-agent/pkg/chat"
	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/team"
	"github.com/docker/docker-agent/pkg/tools"
)

// TestCompactIfNeeded_IgnoresSubSessionTokens is a regression test for
// issue #2871: in a multi-agent run, the tokens accumulated inside a
// transfer_task sub-session were counted by the proactive compaction
// trigger (GetAllMessages recurses into sub-sessions) even though they
// never enter the parent's prompt (GetMessages skips sub-session items).
// The phantom tokens made the parent compact its own tiny conversation;
// with everything fitting the keep budget that meant "compact
// everything, keep nothing" — the agent's next prompt was just the
// summary and it halted with a confused "no conversation history" reply.
func TestCompactIfNeeded_IgnoresSubSessionTokens(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/model", stream: &mockStream{}}
	root := agent.New("root", "agent", agent.WithModel(prov))
	tm := team.New(team.WithAgents(root))

	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(true),
		WithModelStore(mockModelStoreWithLimit{limit: 100_000}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("build the app"))
	messageCountBefore := len(sess.OwnMessages())

	// Simulate a completed transfer_task tool call: a sub-session holding
	// far more content than the parent's context limit, plus a small
	// tool-result message on the parent itself.
	sub := session.New(session.WithUserMessage("subtask"))
	sub.AddMessage(session.NewAgentMessage("worker", &chat.Message{
		Role:    chat.MessageRoleAssistant,
		Content: strings.Repeat("z", 600_000), // ~150k estimated tokens
	}))
	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:      chat.MessageRoleAssistant,
		ToolCalls: []tools.ToolCall{{ID: "t1", Function: tools.FunctionCall{Name: "transfer_task"}}},
	}))
	sess.AddSubSession(sub)
	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:       chat.MessageRoleTool,
		ToolCallID: "t1",
		Content:    "subtask done",
	}))

	events := make(chan Event, 16)
	rt.compactIfNeeded(t.Context(), sess, root, 100_000, messageCountBefore, NewChannelSink(events))
	close(events)

	for ev := range events {
		_, isCompaction := ev.(*SessionCompactionEvent)
		assert.False(t, isCompaction,
			"sub-session tokens must not trigger compaction of the parent session")
	}
}

// TestCompactIfNeeded_TriggersOnOwnMessages pins the complementary case:
// large tool results recorded directly on the session still trigger the
// proactive compaction.
func TestCompactIfNeeded_TriggersOnOwnMessages(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/model", stream: &mockStream{}}
	root := agent.New("root", "agent", agent.WithModel(prov))
	tm := team.New(team.WithAgents(root))

	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(true),
		WithModelStore(mockModelStoreWithLimit{limit: 100_000}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("build the app"))
	messageCountBefore := len(sess.OwnMessages())

	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:      chat.MessageRoleAssistant,
		ToolCalls: []tools.ToolCall{{ID: "t1", Function: tools.FunctionCall{Name: "shell"}}},
	}))
	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:       chat.MessageRoleTool,
		ToolCallID: "t1",
		Content:    strings.Repeat("z", 600_000), // ~150k estimated tokens
	}))

	events := make(chan Event, 16)
	rt.compactIfNeeded(t.Context(), sess, root, 100_000, messageCountBefore, NewChannelSink(events))
	close(events)

	sawCompaction := false
	for ev := range events {
		if _, ok := ev.(*SessionCompactionEvent); ok {
			sawCompaction = true
		}
	}
	assert.True(t, sawCompaction, "large own tool results must still trigger compaction")
}

// TestCompactIfNeeded_CustomThreshold verifies that the agent's configured
// compaction_threshold replaces the 0.9 default in the proactive trigger:
// the same session content that stays under the default threshold triggers
// compaction once a lower threshold is configured.
func TestCompactIfNeeded_CustomThreshold(t *testing.T) {
	t.Parallel()

	// ~150k chars ≈ ~37.5k estimated tokens: 37.5% of the 100k window —
	// under the 0.9 default, over a 0.25 threshold.
	buildSession := func() (*session.Session, int) {
		sess := session.New(session.WithUserMessage("build the app"))
		before := len(sess.OwnMessages())
		sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
			Role:      chat.MessageRoleAssistant,
			ToolCalls: []tools.ToolCall{{ID: "t1", Function: tools.FunctionCall{Name: "shell"}}},
		}))
		sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
			Role:       chat.MessageRoleTool,
			ToolCallID: "t1",
			Content:    strings.Repeat("z", 150_000),
		}))
		return sess, before
	}

	run := func(t *testing.T, agentOpts ...agent.Opt) bool {
		t.Helper()
		prov := &mockProvider{id: "test/model", stream: &mockStream{}}
		root := agent.New("root", "agent", append([]agent.Opt{agent.WithModel(prov)}, agentOpts...)...)
		tm := team.New(team.WithAgents(root))

		rt, err := NewLocalRuntime(t.Context(), tm,
			WithSessionCompaction(true),
			WithModelStore(mockModelStoreWithLimit{limit: 100_000}))
		require.NoError(t, err)

		sess, before := buildSession()
		events := make(chan Event, 16)
		rt.compactIfNeeded(t.Context(), sess, root, 100_000, before, NewChannelSink(events))
		close(events)

		for ev := range events {
			if _, ok := ev.(*SessionCompactionEvent); ok {
				return true
			}
		}
		return false
	}

	t.Run("default threshold does not trigger at 37% usage", func(t *testing.T) {
		t.Parallel()
		assert.False(t, run(t), "37%% usage must not trigger compaction at the 0.9 default")
	})

	t.Run("lower threshold triggers on the same session", func(t *testing.T) {
		t.Parallel()
		assert.True(t, run(t, agent.WithCompactionThreshold(0.25)),
			"37%% usage must trigger compaction with compaction_threshold: 0.25")
	})

	t.Run("threshold of 1 suppresses the trigger even at high usage", func(t *testing.T) {
		t.Parallel()
		prov := &mockProvider{id: "test/model", stream: &mockStream{}}
		root := agent.New("root", "agent", agent.WithModel(prov), agent.WithCompactionThreshold(1))
		tm := team.New(team.WithAgents(root))

		rt, err := NewLocalRuntime(t.Context(), tm,
			WithSessionCompaction(true),
			WithModelStore(mockModelStoreWithLimit{limit: 100_000}))
		require.NoError(t, err)

		sess := session.New(session.WithUserMessage("build the app"))
		before := len(sess.OwnMessages())
		sess.InputTokens = 95_000 // over the 0.9 default, under 1.0

		events := make(chan Event, 16)
		rt.compactIfNeeded(t.Context(), sess, root, 100_000, before, NewChannelSink(events))
		close(events)

		for ev := range events {
			_, isCompaction := ev.(*SessionCompactionEvent)
			assert.False(t, isCompaction, "95%% usage must not trigger compaction with compaction_threshold: 1")
		}
	})
}

// TestCompactIfNeeded_AgentSessionCompactionDisabled verifies that an agent
// with `session_compaction: false` never auto-compacts, even when the
// runtime-level session-compaction option is on and the context usage is
// far past the threshold.
func TestCompactIfNeeded_AgentSessionCompactionDisabled(t *testing.T) {
	t.Parallel()

	prov := &mockProvider{id: "test/model", stream: &mockStream{}}
	root := agent.New("root", "agent", agent.WithModel(prov), agent.WithSessionCompaction(false))
	tm := team.New(team.WithAgents(root))

	rt, err := NewLocalRuntime(t.Context(), tm,
		WithSessionCompaction(true),
		WithModelStore(mockModelStoreWithLimit{limit: 100_000}))
	require.NoError(t, err)

	sess := session.New(session.WithUserMessage("build the app"))
	messageCountBefore := len(sess.OwnMessages())

	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:      chat.MessageRoleAssistant,
		ToolCalls: []tools.ToolCall{{ID: "t1", Function: tools.FunctionCall{Name: "shell"}}},
	}))
	sess.AddMessage(session.NewAgentMessage("root", &chat.Message{
		Role:       chat.MessageRoleTool,
		ToolCallID: "t1",
		Content:    strings.Repeat("z", 600_000), // ~150k estimated tokens
	}))

	events := make(chan Event, 16)
	rt.compactIfNeeded(t.Context(), sess, root, 100_000, messageCountBefore, NewChannelSink(events))
	close(events)

	for ev := range events {
		_, isCompaction := ev.(*SessionCompactionEvent)
		assert.False(t, isCompaction,
			"session_compaction: false must suppress the proactive compaction trigger")
	}
}
