package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/walk"
)

func newPurgeCmd() *cobra.Command {
	var f cleanupFlags
	var risk, profile, olderThan string
	cmd := &cobra.Command{
		Use:   "purge [DIR...]",
		Short: "Find build artifacts (node_modules, target, ...) in projects",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "purge", runPurge(cmd, args, &f, risk, profile, olderThan))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	f.addRunCommandsFlag(cmd)
	addProfileFlags(cmd, &risk, &profile)
	cmd.Flags().StringVar(&olderThan, "older-than", "", "only projects not touched for this long, e.g. 30d, 2w")
	return cmd
}

func runPurge(cmd *cobra.Command, args []string, f *cleanupFlags, risk, profile, olderThan string) error {
	if err := f.check(); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	maxRisk, err := maxRiskOf(risk, profile, cfg)
	if err != nil {
		return err
	}
	r, err := activeRules(cmd, e)
	if err != nil {
		return err
	}
	opts := purge.Options{Now: time.Now(), OlderThan: cfg.PurgeOlderThan, Types: r.Purge}
	if olderThan != "" {
		if opts.OlderThan, err = rules.ParseAge(olderThan); err != nil {
			return usageErr(fmt.Errorf("--older-than: %w", err))
		}
	}
	// Folders: the arguments, else [purge] roots of sapu.toml, else the
	// current folder.
	roots := args
	if len(roots) == 0 {
		for _, root := range cfg.PurgeRoots {
			roots = append(roots, e.Expand(root))
		}
	}
	if len(roots) == 0 {
		roots = []string{"."}
	}

	if useReview(cmd, f) {
		return runReview(cmd, e, func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
			return planPurge(ctx, e, roots, opts, maxRisk)
		})
	}
	p, err := planPurge(cmd.Context(), e, roots, opts, maxRisk)
	if err != nil {
		return err
	}
	return runPlan(cmd, "purge", e, p, *f)
}

// planPurge finds the build folders under roots and plans them, with the
// items up to maxRisk selected. sapu purge and the purge tool share it.
func planPurge(ctx context.Context, e env.Env, roots []string, opts purge.Options, maxRisk rules.Risk) (*plan.Plan, error) {
	items, err := purge.Find(ctx, e, roots, opts)
	if err != nil {
		return nil, err
	}
	p, err := engine.Plan(ctx, e, plan.KindPurge, items)
	if err != nil {
		return nil, err
	}
	selectItems(p, maxRisk, nil, nil, nil)
	return p, nil
}

// purgeDefaultDirs are the folders the purge tool looks in: the [purge]
// roots of sapu.toml, else the usual project folders that exist, else home.
// The typed sapu purge keeps the current folder as its default.
func purgeDefaultDirs(e env.Env) ([]string, error) {
	cfg, err := config.Load(e)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, root := range cfg.PurgeRoots {
		dirs = append(dirs, e.Expand(root))
	}
	if len(dirs) > 0 {
		return dirs, nil
	}
	for _, name := range []string{"Developer", "Projects", "code", "src", "dev", "work", "GitHub", "repos"} {
		dir := filepath.Join(e.Home, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) == 0 {
		dirs = []string{e.Home}
	}
	return dirs, nil
}

// purgeTool is the plan of the purge tool: sapu purge with sapu.toml's
// settings, in dirs or purgeDefaultDirs.
func purgeTool(e env.Env, dirs []string, _ string) review.PlanFunc {
	return func(ctx context.Context, _ func(walk.Event)) (*plan.Plan, error) {
		cfg, err := config.Load(e)
		if err != nil {
			return nil, err
		}
		maxRisk, err := maxRiskOf("", "", cfg)
		if err != nil {
			return nil, err
		}
		r, err := rulesupdate.Active(e)
		if err != nil {
			return nil, err
		}
		roots := dirs
		if len(roots) == 0 {
			if roots, err = purgeDefaultDirs(e); err != nil {
				return nil, err
			}
		}
		opts := purge.Options{Now: time.Now(), OlderThan: cfg.PurgeOlderThan, Types: r.Purge}
		return planPurge(ctx, e, roots, opts, maxRisk)
	}
}
