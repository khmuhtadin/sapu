package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOut {
				return output.JSON(cmd.OutOrStdout(), "version", map[string]string{"version": Version})
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "sapu", Version)
			return err
		},
	}
}
