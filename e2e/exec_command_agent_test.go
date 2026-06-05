package e2e_test

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestExec_CommandTargetsAgent verifies that invoking a /command which targets
// a sub-agent sends the instructions directly to that agent, bypassing the root
// agent (no transfer_task round-trip). The recorded cassette only contains a
// single request carrying the specialist agent's system prompt, which proves
// the message reached the specialist directly.
func TestExec_CommandTargetsAgent(t *testing.T) {
	out := runCLI(t, "run", "--exec", "testdata/command_agent.yaml", "/ask What's 2+2?")

	require.Equal(t, "SPECIALIST: 4", out)
}
