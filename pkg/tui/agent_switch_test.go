package tui

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/docker/docker-agent/pkg/session"
	"github.com/docker/docker-agent/pkg/tui/service"
)

func TestHandleSwitchAgentNoOpForCurrentAgent(t *testing.T) {
	m, _ := newTestModel()
	m.sessionState = service.NewSessionState(session.New())
	m.sessionState.SetCurrentAgentName("agent1")

	_, cmd := m.handleSwitchAgent("agent1")

	require.Nil(t, cmd)
}
