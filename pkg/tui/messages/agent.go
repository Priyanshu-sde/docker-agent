package messages

// Agent messages control agent switching, commands, and model selection.
type (
	// SwitchAgentMsg switches to a different agent.
	SwitchAgentMsg struct{ AgentName string }

	// ShowAgentDetailsMsg opens the read-only agent-details dialog for the
	// named agent (clicking the current agent's card or Ctrl+clicking any agent).
	ShowAgentDetailsMsg struct{ AgentName string }

	// AgentCommandMsg sends a command to the agent.
	AgentCommandMsg struct{ Command string }

	// OpenModelPickerMsg opens the model picker dialog.
	OpenModelPickerMsg struct{}

	// ChangeModelMsg changes the model for the current agent.
	ChangeModelMsg struct{ ModelRef string }

	// SetThinkingLevelMsg sets the reasoning-effort level of the current
	// agent's model (/effort command). An empty Level shows usage.
	SetThinkingLevelMsg struct{ Level string }
)
