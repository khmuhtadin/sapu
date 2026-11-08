package cli

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
)

const installerLong = `Finds .dmg, .pkg, .mpkg, .iso and .xip files that were not changed for
--older-than. With no DIR it looks in ~/Downloads and ~/Desktop. It looks at
the files directly in each folder, not in subfolders, so build results inside
your projects are left alone.

Installers can be downloaded again, so they are selected. A disk image that
is mounted right now is left alone.`

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
	cmd.Flags().StringVar(&olderThan, "older-than", "30d", "only installers not changed for this long, e.g. 30d, 8w, 1y")
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
		roots = []string{filepath.Join(e.Home, "Downloads"), filepath.Join(e.Home, "Desktop")}
	}

	ctx := cmd.Context()
	items, mounted, err := finders.Installers(ctx, e, roots, opts)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindInstaller, items)
	if err != nil {
		return err
	}
	p.Skipped = append(p.Skipped, mounted...)
	slices.SortStableFunc(p.Skipped, func(a, b plan.Skip) int { return cmp.Compare(a.Path, b.Path) })
	return runPlan(cmd, "installer", e, p, *f)
}
