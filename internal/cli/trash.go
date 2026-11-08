package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/walk"
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
	if useReview(cmd, f) {
		return runReview(cmd, e, trashTool(e, nil, ""))
	}
	p, err := planTrash(cmd.Context(), e)
	if err != nil {
		return err
	}
	return runPlan(cmd, "trash", e, p, *f)
}

// planTrash plans everything in the Trash; it is moderate, so it starts
// selected. sapu trash and the trash tool share it.
func planTrash(ctx context.Context, e env.Env) (*plan.Plan, error) {
	items, err := finders.Trash(ctx, e)
	if err != nil {
		return nil, err
	}
	return engine.Plan(ctx, e, plan.KindTrash, items)
}

// trashTool is the plan of the trash tool. It has no folders.
func trashTool(e env.Env, _ []string, _ string) review.PlanFunc {
	return func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
		return planTrash(ctx, e)
	}
}
