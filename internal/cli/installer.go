package cli

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/walk"
)

const installerLong = `Finds .dmg, .pkg, .mpkg, .iso and .xip files that were not changed for
--older-than. With no DIR it looks in ~/Downloads and ~/Desktop. It looks at
the files directly in each folder, not in subfolders, so build results inside
your projects are left alone.

Installers can be downloaded again, so they are selected. A disk image that
is mounted right now is left alone.`

// installerOlderThan is the default of --older-than, which the tool uses too.
const installerOlderThan = "30d"

func newInstallerCmd() *cobra.Command {
	var f cleanupFlags
	var olderThan string
	cmd := &cobra.Command{
		Use:   "installer [DIR...]",
		Short: "Find old installers (.dmg, .pkg, .iso) you no longer need",
		Long:  installerLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "installer", runInstaller(cmd, args, &f, olderThan))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().StringVar(&olderThan, "older-than", installerOlderThan, "only installers not changed for this long, e.g. 30d, 8w, 1y")
	return cmd
}

func runInstaller(cmd *cobra.Command, args []string, f *cleanupFlags, olderThan string) error {
	if err := f.check(); err != nil {
		return err
	}
	opts := finders.InstallerOptions{Now: time.Now()}
	var err error
	if opts.OlderThan, err = finders.ParseAge(olderThan); err != nil {
		return usageErr(fmt.Errorf("--older-than: %w", err))
	}
	if err := checkFoundDirs(args); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	roots := args
	if len(roots) == 0 {
		roots = installerDefaultDirs(e)
	}
	if useReview(cmd, f) {
		return runReview(cmd, e, func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
			return planInstaller(ctx, e, roots, opts)
		})
	}
	p, err := planInstaller(cmd.Context(), e, roots, opts)
	if err != nil {
		return err
	}
	return runPlan(cmd, "installer", e, p, *f)
}

// installerDefaultDirs are the folders sapu installer looks in without DIR.
func installerDefaultDirs(e env.Env) []string {
	return []string{filepath.Join(e.Home, "Downloads"), filepath.Join(e.Home, "Desktop")}
}

// planInstaller finds the old installers in roots and plans them; they are
// moderate, so they start selected. sapu installer and its tool share it.
func planInstaller(ctx context.Context, e env.Env, roots []string, opts finders.InstallerOptions) (*plan.Plan, error) {
	items, mounted, err := finders.Installers(ctx, e, roots, opts)
	if err != nil {
		return nil, err
	}
	p, err := engine.Plan(ctx, e, plan.KindInstaller, items)
	if err != nil {
		return nil, err
	}
	p.Skipped = append(p.Skipped, mounted...)
	slices.SortStableFunc(p.Skipped, func(a, b plan.Skip) int { return cmp.Compare(a.Path, b.Path) })
	return p, nil
}

// installerTool is the plan of the installer tool: sapu installer with its
// defaults (--older-than 30d).
func installerTool(e env.Env, dirs []string, _ string) review.PlanFunc {
	return func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
		opts := finders.InstallerOptions{Now: time.Now()}
		var err error
		if opts.OlderThan, err = finders.ParseAge(installerOlderThan); err != nil {
			return nil, err
		}
		roots := dirs
		if len(roots) == 0 {
			roots = installerDefaultDirs(e)
		}
		return planInstaller(ctx, e, roots, opts)
	}
}
