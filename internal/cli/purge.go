package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/rules"
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

	ctx := cmd.Context()
	items, err := purge.Find(ctx, e, roots, opts)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindPurge, items)
	if err != nil {
		return err
	}
	selectItems(p, maxRisk, nil, nil, nil)
	return runPlan(cmd, "purge", e, p, *f)
}
