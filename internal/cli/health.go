package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/health"
	"github.com/khmuhtadin/sapu/internal/output"
)

// healthData is the data of `sapu health --json`.
type healthData struct {
	Rows []health.Row `json:"rows"`
}

func newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Battery, SSD and temperature, read-only",
		Long: `Show how the hardware is doing: battery, startup drive, temperature, memory
and uptime. Each line has a status (good, worth watching, needs attention) and
one sentence about what it means.

Health only reads, and needs no admin rights. The exit code is 0 whatever the
status: the rows carry the result.`,
		Args: cobra.NoArgs,
		RunE: runHealth,
	}
}

func runHealth(cmd *cobra.Command, args []string) error {
	p := newProgress(cmd)
	p.Status("scan", "Reading the hardware (a few seconds)…")
	rows := health.Check(cmd.Context())
	p.Done()
	if err := cmd.Context().Err(); err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "health", healthData{Rows: rows})
	}

	w := cmd.OutOrStdout()
	st := output.NewStyle(w)
	watch, attention := 0, 0
	for _, r := range rows {
		mark, label := "–", "not applicable"
		switch r.Status {
		case health.Good:
			mark, label = st.Green("✓"), "good"
		case health.Watch:
			mark, label = st.Yellow("!"), "worth watching"
			watch++
		case health.Attention:
			mark, label = st.Red("✗"), "needs attention"
			attention++
		case health.Unknown:
			mark, label = "?", "not available"
		}
		fmt.Fprintf(w, "%s %s: %s\n", mark, st.Bold(r.Title), label)
		output.WrapIndent(w, "    ", r.Value)
		output.WrapIndent(w, "    ", r.Meaning)
	}
	fmt.Fprintln(w)
	switch {
	case attention > 0:
		fmt.Fprintf(w, "%d of %d checks need attention.\n", attention, len(rows))
	case watch > 0:
		fmt.Fprintf(w, "%d of %d checks are worth watching.\n", watch, len(rows))
	default:
		fmt.Fprintln(w, "Nothing needs attention.")
	}
	return nil
}
