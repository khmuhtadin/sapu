package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/explain"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/trends"
)

// explainData is the data of `sapu explain --json`.
type explainData struct {
	Rows          []explain.Row `json:"rows"`
	MeasuredBytes int64         `json:"measured_bytes"` // sum of the measured rows: a lower bound
	Unmeasured    int           `json:"unmeasured"`     // rows that could not be measured
	// Reconcile is "How this adds up": used space split into home, measured
	// system parts and the rest. Nil is not shown.
	Reconcile *explain.Reconciliation `json:"reconcile,omitempty"`
}

// explainRows measures the System Data rows. Tests replace it.
var explainRows = explain.Explain

func newExplainCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "explain",
		Short: "Break down System Data and how to reclaim each part",
		Long: `Break down what fills "System Data" and show the official way to reclaim each part.

Explain only reads. It shows commands but never runs them.`,
		Args: cobra.NoArgs,
		RunE: runExplain,
	}
}

func runExplain(cmd *cobra.Command, args []string) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	p := newProgress(cmd)
	p.Status("explain", "Measuring System Data (a folder can take up to 20 seconds)…")
	rows, err := explainRows(cmd.Context(), e)
	p.Done()
	if err != nil {
		return err
	}

	data := explainData{Rows: rows}
	for _, r := range rows {
		if r.Bytes < 0 {
			data.Unmeasured++
		} else {
			data.MeasuredBytes += r.Bytes
		}
	}
	total, free, err := diskUsage(e.Home)
	if err != nil {
		return err
	}
	var home *trends.Snapshot
	switch s, ok, err := trends.Newest(e); {
	case err != nil:
		fmt.Fprintf(cmd.ErrOrStderr(), "sapu: not using the home folder snapshot: %v\n", err)
	case ok:
		home = &s
	}
	rec := explain.Reconcile(rows, total, total-free, home, time.Now())
	data.Reconcile = &rec

	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "explain", data)
	}
	printExplain(cmd, data)
	return nil
}

func printExplain(cmd *cobra.Command, d explainData) {
	w := cmd.OutOrStdout()
	st := output.NewStyle(w)

	fmt.Fprintln(w, st.Bold(`What fills "System Data"`))
	for _, r := range d.Rows {
		fmt.Fprintln(w)
		size := output.Bytes(r.Bytes)
		if r.Bytes < 0 {
			size = "not measured"
		}
		// Title on the left, size on the right edge.
		title := output.Truncate(r.Title, output.Width-len(size)-2)
		gap := strings.Repeat(" ", output.Width-len([]rune(title))-len(size))
		fmt.Fprintln(w, st.Bold(title)+gap+size)
		if r.Unmeasured != "" {
			fmt.Fprintln(w, "  "+st.Dim("Why: "+r.Unmeasured+"."))
		}
		output.WrapIndent(w, "  ", r.Explain)
		output.WrapIndent(w, "  ", "What to do: "+r.Action)
		if len(r.Command) > 0 {
			cmdline := "$ " + output.ShellCommand(r.Command)
			if r.NeedsAdmin {
				cmdline = "$ sudo " + output.ShellCommand(r.Command)
			}
			fmt.Fprintln(w, "    "+st.Bold(cmdline))
			if r.NeedsAdmin {
				fmt.Fprintln(w, "    "+st.Dim("(needs admin rights)"))
			}
		}
	}

	fmt.Fprintln(w)
	fmt.Fprintf(w, "%s %s at least", st.Bold("Measured total:"), output.Bytes(d.MeasuredBytes))
	if d.Unmeasured > 0 {
		fmt.Fprintf(w, " (%d not measured)", d.Unmeasured)
	}
	if d.Reconcile != nil {
		writeReconcile(w, *d.Reconcile, time.Now())
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Sapu never runs these.")
}

// writeReconcile prints "How this adds up". Every number that Sapu derives or
// that depends on how macOS counts is labelled as an estimate.
func writeReconcile(w io.Writer, r explain.Reconciliation, now time.Time) {
	st := newStyle(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold("How this adds up"))

	fmt.Fprintln(w, row(2, "Used on disk", output.Bytes(r.UsedBytes)))
	writeWrapped(w, st, 4, fmt.Sprintf("Of %s, as the disk reports it. This counts purgeable space as used, so it can be higher than System Settings says (estimate).", output.Bytes(r.TotalBytes)))

	if r.HomeBytes < 0 {
		fmt.Fprintln(w, row(2, "Your home folder", "not measured"))
		writeWrapped(w, st, 4, "Run `sapu diff --record` to measure it (a read-only walk, about a minute).")
	} else {
		fmt.Fprintln(w, row(2, "Your home folder", output.Bytes(r.HomeBytes)))
		writeWrapped(w, st, 4, "Measured "+ageText(now.Sub(*r.HomeTaken))+" ago by `sapu diff --record`. It counts only what Sapu may read.")
		if len(r.HomeUnmeasured) > 0 {
			writeWrapped(w, st, 4, "Not counted, because they did not answer in time or could not be read: "+strings.Join(r.HomeUnmeasured, ", ")+".")
		}
	}

	system := output.Bytes(r.SystemBytes)
	fmt.Fprintln(w, row(2, "System Data parts Sapu measured", system))
	text := "Spotlight, system assets, swap and the sleep image: at least this much."
	if r.SystemUnmeasured > 0 {
		text = fmt.Sprintf("Spotlight, system assets, swap and the sleep image: at least this much, %d not measured.", r.SystemUnmeasured)
	}
	writeWrapped(w, st, 4, text)

	if r.RestBytes < 0 {
		fmt.Fprintln(w, row(2, "The rest", "not known"))
	} else {
		fmt.Fprintln(w, row(2, "The rest (estimate)", output.Bytes(r.RestBytes)))
	}
	writeWrapped(w, st, 4, "Everything else: apps in /Applications and tools like Homebrew, macOS itself and its update snapshots, other users, purgeable space, and folders Sapu could not read or measure. `sapu analyze /` shows the parts Sapu can read.")
	if r.Note != "" {
		writeWrapped(w, st, 4, r.Note)
	}
}
