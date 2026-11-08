package cli

import (
	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
)

const trashLong = `Moves everything in ~/.Trash (hidden files too) and in the Trash of every
mounted volume to quarantine. You already threw these away, so they are
selected; quarantine keeps them for 7 days and "sapu undo" brings them back.

Items on your whitelist stay in the Trash. Reading the Trash needs Full Disk
Access for your terminal app on recent macOS; "sapu doctor" shows how.`

func newTrashCmd() *cobra.Command {
	var f cleanupFlags
	cmd := &cobra.Command{
		Use:   "trash",
		Short: "Empty the Trash through quarantine, hidden files included",
		Long:  trashLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "trash", runTrash(cmd, &f))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	return cmd
}

func runTrash(cmd *cobra.Command, f *cleanupFlags) error {
	if err := f.check(); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	items, err := finders.Trash(ctx, e)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindTrash, items)
	if err != nil {
		return err
	}
	return runPlan(cmd, "trash", e, p, *f)
}
