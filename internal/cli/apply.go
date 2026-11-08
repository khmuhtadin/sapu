package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/plan"
)

func newApplyCmd() *cobra.Command {
	f := cleanupFlags{selectedOnly: true}
	cmd := &cobra.Command{
		Use:   "apply PLAN.json",
		Short: "Apply a saved plan; only the items in it are touched",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "apply", runApply(cmd, args, &f))
		},
	}
	f.addApplyFlags(cmd)
	f.addRunCommandsFlag(cmd)
	return cmd
}

func runApply(cmd *cobra.Command, args []string, f *cleanupFlags) error {
	if len(args) != 1 {
		return usageErr(errors.New("apply takes one argument: PLAN.json, or - for stdin"))
	}
	if err := f.check(); err != nil {
		return err
	}
	file := args[0]
	if file == "-" && !f.yes {
		return usageErr(errors.New("the plan comes from stdin, so there is nobody to ask; add --yes"))
	}

	p, err := readPlan(cmd, file)
	if err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	if p.Home != e.Home {
		return fmt.Errorf("%s was made for the home folder %s, not %s; Sapu will not apply it", file, p.Home, e.Home)
	}
	return runPlan(cmd, "apply", e, p, *f)
}

func readPlan(cmd *cobra.Command, file string) (*plan.Plan, error) {
	r := cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }() // read only
		r = f
	}
	p, err := plan.Read(r)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}
