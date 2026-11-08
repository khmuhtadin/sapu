package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/doctor"
	"github.com/khmuhtadin/sapu/internal/output"
)

// doctorData is the data of `sapu doctor --json`.
type doctorData struct {
	Checks []doctor.Check `json:"checks"`
}

func newDoctorCmd() *cobra.Command {
	var open bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Full Disk Access and other settings Sapu needs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, open)
		},
	}
	cmd.Flags().BoolVar(&open, "open", false, "open the Full Disk Access pane in System Settings")
	return cmd
}

// runDoctor exits 0 even when a check fails: the checks carry the result.
func runDoctor(cmd *cobra.Command, open bool) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	checks := doctor.Run(cmd.Context(), e)
	if open {
		if err := doctor.OpenFullDiskAccessSettings(); err != nil {
			return fmt.Errorf("open System Settings: %w", err)
		}
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "doctor", doctorData{Checks: checks})
	}

	w := cmd.OutOrStdout()
	st := output.NewStyle(w)
	failed := 0
	for _, c := range checks {
		mark := st.Green("✓")
		if !c.OK {
			mark = st.Red("✗")
			failed++
		}
		fmt.Fprintln(w, mark, st.Bold(c.Title))
		output.WrapIndent(w, "    ", c.Detail)
		if c.Fix != "" {
			output.WrapIndent(w, "    ", "Fix: "+c.Fix)
		}
	}
	fmt.Fprintln(w)
	switch failed {
	case 0:
		fmt.Fprintln(w, "All checks passed.")
	case 1:
		fmt.Fprintln(w, "1 check needs attention.")
	default:
		fmt.Fprintf(w, "%d checks need attention.\n", failed)
	}
	if open {
		fmt.Fprintln(w, "Opened System Settings.")
	}
	return nil
}
