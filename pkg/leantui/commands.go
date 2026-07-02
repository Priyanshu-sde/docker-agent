package leantui

import (
	"sort"
	"strings"
)

// commandKind distinguishes built-in lean-TUI commands (handled locally) from
// agent-provided commands and skills (resolved and sent to the agent).
type commandKind int

const (
	cmdBuiltin commandKind = iota
	cmdAgent
)

type command struct {
	name string
	desc string
	kind commandKind
}

// builtinCommands are the slash commands the lean TUI handles itself.
func builtinCommands() []command {
	return []command{
		{name: "new", desc: "Start a new session", kind: cmdBuiltin},
		{name: "compact", desc: "Summarize and compact the conversation", kind: cmdBuiltin},
		{name: "effort", desc: "Set the model's reasoning effort (usage: /effort <level>)", kind: cmdBuiltin},
		{name: "clear", desc: "Clear the screen", kind: cmdBuiltin},
		{name: "help", desc: "Show keyboard shortcuts and commands", kind: cmdBuiltin},
		{name: "exit", desc: "Exit", kind: cmdBuiltin},
		{name: "quit", desc: "Exit", kind: cmdBuiltin},
	}
}

// filterCommands returns the commands whose name has the given prefix, built-in
// commands first, then agent commands, each group alphabetically sorted.
func filterCommands(all []command, prefix string) []command {
	prefix = strings.ToLower(prefix)
	var out []command
	for _, c := range all {
		if strings.HasPrefix(strings.ToLower(c.name), prefix) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		return out[i].name < out[j].name
	})
	return out
}
