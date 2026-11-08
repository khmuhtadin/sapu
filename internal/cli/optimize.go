package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/plan"
)

func newOptimizeCmd() *cobra.Command {
	var f cleanupFlags
	cmd := &cobra.Command{
		Use:   "optimize",
		Short: "Clear caches macOS rebuilds, to fix previews, fonts or DNS",
		Long: `Clear caches macOS rebuilds, to fix specific problems.

Each action fixes one problem (stale previews, broken fonts, DNS trouble).
None of them makes a Mac faster in general. Nothing to roll back: these only
clear caches macOS rebuilds, and change no settings.

Actions that need admin rights are shown with the exact command; Sapu never
runs sudo.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "optimize", runOptimize(cmd, args, &f))
		},
	}
	f.addPlanFlag(cmd)
	f.addConfirmFlags(cmd)
	return cmd
}

func runOptimize(cmd *cobra.Command, args []string, f *cleanupFlags) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("optimize takes no arguments, got %q", args[0]))
	}
	if err := f.check(); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	p, err := engine.Plan(cmd.Context(), e, plan.KindOptimize, optimize.Items())
	if err != nil {
		return err
	}
	if f.planFile != "-" {
		out := humanOut(cmd)
		for _, line := range wrapText(optimize.Intro, tableWidth) {
			fmt.Fprintln(out, line)
		}
		fmt.Fprintln(out, optimize.Rollback)
	}
	// Running macOS's own reset commands is what this command is for, so it
	// does not need --run-commands; the confirmation lists every command first.
	f.runCommands = true
	f.runWhy = "Running macOS's own reset commands is what sapu optimize is for."
	f.admin = optimize.Admin()
	return runPlan(cmd, "optimize", e, p, *f)
}
