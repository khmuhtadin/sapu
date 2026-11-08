package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

const resetLong = `Reset an app to the state of a first start. The app stays; its settings,
sign-ins, caches and data go to quarantine.

APP is a name (Slack), a path (/Applications/Slack.app) or a bundle id
(com.tinyspeck.slackmacgap). Quit the app first.

Selected: the files in ~/Library named after the app's bundle id (Preferences,
Caches, Application Support, Containers, Group Containers, Saved Application
State, HTTPStorages, WebKit, Logs, Cookies).
Not selected: files that only share the app's name, because another app may
use that name too (select them with --risk risky).

Everything goes to quarantine, so "sapu undo" puts it back. The app itself is
never moved. To also remove its privacy permissions, see sapu uninstall
--reset-permissions.`

func newResetCmd() *cobra.Command {
	var f cleanupFlags
	var risk string
	cmd := &cobra.Command{
		Use:   "reset APP",
		Short: "Reset an app: move its settings and caches to quarantine",
		Long:  resetLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "reset", runReset(cmd, args, &f, risk))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().StringVar(&risk, "risk", "moderate", "highest risk to select: safe, moderate or risky")
	return cmd
}

func runReset(cmd *cobra.Command, args []string, f *cleanupFlags, risk string) error {
	if len(args) != 1 {
		return usageErr(errors.New("reset takes one APP: a name (Slack), a path (/Applications/Slack.app) or a bundle id"))
	}
	maxRisk, err := parseRisk(risk)
	if err != nil {
		return err
	}
	if err := f.check(); err != nil {
		return err
	}
	f.verbose = true // list every file before asking, not just the first five
	e, err := loadEnv()
	if err != nil {
		return err
	}
	installed, err := apps.List(cmd.Context(), e)
	if err != nil {
		return err
	}
	return resetApp(cmd, e, installed, args[0], maxRisk, f)
}

func resetApp(cmd *cobra.Command, e env.Env, installed []apps.App, query string, maxRisk rules.Risk, f *cleanupFlags) error {
	ctx := cmd.Context()
	t, err := pickApp(e, installed, query, "reset")
	if err != nil {
		return err
	}
	a := t.app
	items, notes, err := appFiles(ctx, e, t, true)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindReset, items)
	if err != nil {
		return err
	}
	for _, s := range p.Skipped {
		if s.Reason == plan.ReasonInUse && strings.HasSuffix(s.Detail, " is running") {
			return fmt.Errorf("quit %s first: it is running. Then run sapu reset again", a.Name)
		}
	}
	for i := range p.Items {
		p.Items[i].Selected = riskRank(p.Items[i].Risk) <= riskRank(maxRisk)
	}

	out := humanOut(cmd)
	writeAppTitle(out, e.Home, a)
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	writeUninstallNotes(out, e.Home, notes, p.Skipped, why)
	if len(p.Items) == 0 && f.planFile == "" {
		fmt.Fprintf(out, "Nothing to reset: Sapu found no settings or data of %s in your Library.\n", a.Name)
		return printData(cmd, "reset", runData{Summary: summarize(p), Notes: f.notes, Admin: f.admin})
	}
	return runPlan(cmd, "reset", e, p, *f)
}
