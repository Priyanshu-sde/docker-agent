package root

import (
	"errors"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/docker/docker-agent/pkg/telemetry"
)

// newGettingStartedCmd launches the interactive getting-started tour: a
// normal `run` of the default agent with the scripted tour overlay started
// immediately. It dispatches through the registered run command (the same
// mechanism the root command uses when invoked with no arguments) so the
// session behaves exactly like a regular interactive run.
func newGettingStartedCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "getting-started",
		Aliases: []string{"tour"},
		Short:   "Learn docker agent with a hands-on interactive tour",
		Long: `Learn docker agent by doing: a short interactive tour inside the chat UI.

It walks through sending messages, approving tool calls, the command palette
(Ctrl+k), slash commands, and how agents are configured. About 2 minutes,
skippable at any point with Esc. Replay it anytime with this command or the
/getting-started slash command inside the TUI.`,
		Example: `  docker-agent getting-started`,
		GroupID: "core",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			telemetry.TrackCommand(cmd.Context(), "getting-started", args)

			if !isatty.IsTerminal(os.Stdout.Fd()) {
				return errors.New("the getting-started tour is interactive and needs a terminal")
			}

			runCmd, _, err := cmd.Root().Find([]string{"run"})
			if err != nil {
				return err
			}
			if err := runCmd.PersistentFlags().Set("tour", "true"); err != nil {
				return err
			}
			if err := runCmd.PersistentPreRunE(runCmd, nil); err != nil {
				return err
			}
			return runCmd.RunE(runCmd, nil)
		},
	}
}
