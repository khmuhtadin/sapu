package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/pkgs"
	"github.com/khmuhtadin/sapu/internal/plan"
)

func newPackagesCmd() *cobra.Command {
	var f cleanupFlags
	var risk string
	cmd := &cobra.Command{
		Use:   "packages",
		Short: "Clean package manager caches with each tool's own command",
		Long: `Clean package manager caches with each tool's own command.

Sapu finds brew, npm, pnpm, yarn, pip, uv, go, CocoaPods, Xcode simulators
and docker, and runs each tool's own cleanup after you confirm; it never
deletes their files itself. These commands are not undoable.

Old Node, Python and Ruby versions from nvm, pyenv and rbenv are listed too,
never selected by default (use --risk risky). Your default version is never
offered, and the others go to quarantine, so undo can bring them back.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "packages", runPackages(cmd, args, &f, risk))
		},
	}
	f.addPlanFlag(cmd)
	f.addConfirmFlags(cmd)
	cmd.Flags().StringVar(&risk, "risk", "moderate", "highest risk to select: safe, moderate or risky")
	return cmd
}

func runPackages(cmd *cobra.Command, args []string, f *cleanupFlags, risk string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("packages takes no arguments, got %q", args[0]))
	}
	maxRisk, err := parseRisk(risk)
	if err != nil {
		return err
	}
	if err := f.check(); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	prog := newProgress(cmd)
	prog.Status("scan", "Looking for package managers and their caches…")
	items, notes, err := pkgs.Find(ctx, e)
	prog.Done()
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindPackages, items)
	if err != nil {
		return err
	}
	selectItems(p, maxRisk, nil, nil, nil)
	for i := range p.Items {
		if pkgs.NeverSelected(p.Items[i]) {
			p.Items[i].Selected = false
		}
	}
	// Running each tool's cleaner is what this command is for, so it does
	// not need --run-commands; the confirmation lists every command first.
	f.runCommands = true
	f.runWhy = "Running each tool's own cleanup is what sapu packages is for."
	f.notes = notes
	return runPlan(cmd, "packages", e, p, *f)
}
