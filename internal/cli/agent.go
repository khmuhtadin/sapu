package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/agent"
)

// newAgentCmd is what the LaunchAgent runs every hour. It is hidden: people
// use sapu schedule and sapu alerts.
func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "agent",
		Short:  "Run the scheduled checks (launchd calls this)",
		Hidden: true,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Check free space and run the scheduled clean when due",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErr(fmt.Errorf("agent run takes no arguments, got %q", args[0]))
			}
			e, err := loadEnv()
			if err != nil {
				return err
			}
			return agent.Run(cmd.Context(), e, time.Now())
		},
	})
	return cmd
}
