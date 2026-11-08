package cli

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/doctor"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/mascot"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/scan"
)

// summaryData is the data of `sapu --json`.
type summaryData struct {
	FreeBytes      int64          `json:"free_bytes"`
	TotalBytes     int64          `json:"total_bytes"`
	Reclaimable    summaryReclaim `json:"reclaimable"`
	ProtectedItems int            `json:"protected_items"` // protected or whitelisted
	InUseItems     int            `json:"in_use_items"`
	FullDiskAccess bool           `json:"full_disk_access"`
}

// summaryReclaim counts what `sapu clean` would move to quarantine.
// Selected is safe plus moderate; risky items are never selected.
type summaryReclaim struct {
	SafeBytes     int64 `json:"safe_bytes"`
	ModerateBytes int64 `json:"moderate_bytes"`
	SelectedBytes int64 `json:"selected_bytes"`
	RiskyBytes    int64 `json:"risky_bytes"`
	Items         int   `json:"items"`

	// Counts for the text summary only; they are not part of --json.
	SafeItems     int `json:"-"`
	ModerateItems int `json:"-"`
	RiskyItems    int `json:"-"`
}

// runSummary runs for plain `sapu`. On a terminal it opens the menu. Otherwise
// (a pipe, SAPU_NO_TUI, --json) it prints free space, reclaimable space by
// risk, Full Disk Access status and next steps.
func runSummary(cmd *cobra.Command, args []string) error {
	if useMenu(cmd) {
		return runMenu(cmd)
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	free, total, err := freeSpace(e)
	if err != nil {
		return err
	}
	data, err := reclaimSummary(cmd, e)
	if err != nil {
		return err
	}
	data.FreeBytes = free
	data.TotalBytes = total
	data.FullDiskAccess = doctor.HasFullDiskAccess(e)

	if jsonOut {
		return output.JSON(cmd.OutOrStdout(), "summary", data)
	}
	return printSummary(cmd, e, data)
}

// freeSpace returns free and total bytes of the volume holding the home folder.
func freeSpace(e env.Env) (free, total int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(e.Home, &st); err != nil {
		return 0, 0, fmt.Errorf("read free space: %w", err)
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize), nil
}

// reclaimSummary scans with the rules in use and plans, which only reads.
// Items that Sapu would only suggest a command for (official_command) are
// not counted: Sapu does not move them.
func reclaimSummary(cmd *cobra.Command, e env.Env) (summaryData, error) {
	var data summaryData
	rs, err := loadCleaners(cmd, e)
	if err != nil {
		return data, err
	}
	p := newProgress(cmd)
	defer p.Done()
	items, err := scan.Scan(cmd.Context(), e, rs, p.Scan)
	if err != nil {
		return data, err
	}
	pl, err := engine.Plan(cmd.Context(), e, plan.KindClean, items)
	if err != nil {
		return data, err
	}

	for _, it := range pl.Items {
		if it.Action != rules.ActionQuarantine {
			continue
		}
		data.Reclaimable.Items++
		switch it.Risk {
		case rules.RiskSafe:
			data.Reclaimable.SafeBytes += it.Bytes
			data.Reclaimable.SafeItems++
		case rules.RiskModerate:
			data.Reclaimable.ModerateBytes += it.Bytes
			data.Reclaimable.ModerateItems++
		case rules.RiskRisky:
			data.Reclaimable.RiskyBytes += it.Bytes
			data.Reclaimable.RiskyItems++
		}
	}
	data.Reclaimable.SelectedBytes = data.Reclaimable.SafeBytes + data.Reclaimable.ModerateBytes
	for _, s := range pl.Skipped {
		switch s.Reason {
		case plan.ReasonProtected, plan.ReasonWhitelisted:
			data.ProtectedItems++
		case plan.ReasonInUse:
			data.InUseItems++
		}
	}
	return data, nil
}

// narrowWidth is the width below which the summary stacks its rows (design 10).
const narrowWidth = 60

// printSummary writes the summary of design 04b, or its narrow layout of
// design 10 below 60 columns. It never draws the mascot.
func printSummary(cmd *cobra.Command, e env.Env, d summaryData) error {
	runs, err := heldRuns(e)
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	if termWidth(w) < narrowWidth {
		writeNarrowSummary(w, d, runs, termWidth(w))
	} else {
		writeSummary(w, d, runs)
	}
	return nil
}

// termWidth is the width of the terminal w, else $COLUMNS, else 80.
func termWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ); err == nil && ws.Col > 0 {
			return int(ws.Col)
		}
	}
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	return 80
}

// riskRow is one line of FOUND FOR REVIEW.
type riskRow struct {
	word  string
	paint func(string) string
	bytes int64
	items int
	wide  string // the explanation of the 80-column layout
	short string // the explanation of the narrow layout
}

func riskRows(st output.Style, r summaryReclaim) []riskRow {
	return []riskRow{
		{"SAFE", st.Green, r.SafeBytes, r.SafeItems, "Recreated by their applications", "recreated by applications"},
		{"MODERATE", st.Yellow, r.ModerateBytes, r.ModerateItems, "Review the download or rebuild cost", "needs review"},
		{"RISKY", st.Red, r.RiskyBytes, r.RiskyItems, "May contain personal data", "not selected"},
	}
}

func itemsText(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

func writeSummary(w io.Writer, d summaryData, runs []held) {
	st := output.NewStyle(w)
	color := colorOK(w)
	used := max(d.TotalBytes-d.FreeBytes, 0)

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+mascot.Accent("sapu", color)+" / CLI / Open source")
	fmt.Fprintln(w, "  A careful Mac cleanup.")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  %-37s%s used / %s total\n", "Disk", output.Bytes(used), output.Bytes(d.TotalBytes))
	fmt.Fprintf(w, "  [%s]   %s free\n", diskBar(used, d.TotalBytes, 32), output.Bytes(d.FreeBytes))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+st.Bold("FOUND FOR REVIEW"))
	fmt.Fprintln(w)
	for _, r := range riskRows(st, d.Reclaimable) {
		count := itemsText(r.items)
		fmt.Fprintf(w, "    %s%s%12s    %8s    %s\n", r.paint(r.word), strings.Repeat(" ", 8-len(r.word)), output.Bytes(r.bytes), count, r.wide)
	}
	fmt.Fprintln(w)

	quarantine, hint := quarantineParts(runs, time.Now())
	access, accessHint := "Full", "All folders can be measured"
	if !d.FullDiskAccess {
		access, accessHint = "Partial", "Some folders were not measured"
	}
	left := fmt.Sprintf("%d protected", d.ProtectedItems)
	if d.InUseItems > 0 {
		left += fmt.Sprintf(" / %d open", d.InUseItems)
	}
	fmt.Fprintf(w, "  %-16s%-22s%s\n", "Quarantine", quarantine, hint)
	fmt.Fprintf(w, "  %-16s%-22s%s\n", "Left alone", left, "Sapu leaves them where they are")
	fmt.Fprintf(w, "  %-16s%-22s%s\n", "Disk access", access, accessHint)
	fmt.Fprintln(w)

	fmt.Fprintln(w, "  "+st.Bold("NEXT"))
	for _, n := range [][2]string{
		{"sapu clean --plan plan.json", "Save a plan. Changes no files."},
		{"sapu clean", "Review candidates interactively."},
		{"sapu explain", "Understand System Data."},
		{"sapu analyze", "Explore folders by size."},
		{"sapu doctor", "Check access and setup."},
	} {
		fmt.Fprintf(w, "    %-35s%s\n", n[0], n[1])
	}
}

// writeNarrowSummary stacks the rows for a narrow terminal (design 10).
func writeNarrowSummary(w io.Writer, d summaryData, runs []held, width int) {
	st := output.NewStyle(w)
	edge := min(width-2, 34) // where the right-aligned sizes end
	row := func(label, value string) {
		fmt.Fprintf(w, "  %s%*s\n", label, max(edge-2-len(label), len(value)), value)
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+mascot.Accent("sapu", colorOK(w))+" / CLI / Open source")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Disk")
	row("Free", output.Bytes(d.FreeBytes))
	fmt.Fprintln(w)
	for _, r := range riskRows(st, d.Reclaimable) {
		fmt.Fprintf(w, "  %s%*s\n", r.paint(r.word), max(edge-2-len(r.word), 8), output.Bytes(r.bytes))
		fmt.Fprintf(w, "  %s / %s\n\n", itemsText(r.items), r.short)
	}
	quarantine, _ := quarantineParts(runs, time.Now())
	access := "Full"
	if !d.FullDiskAccess {
		access = "Partial"
	}
	row("Quarantine", quarantine)
	row("Disk access", access)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Save a plan:")
	fmt.Fprintln(w, "  sapu clean --plan plan.json")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Review candidates:")
	fmt.Fprintln(w, "  sapu clean")
}

// diskBar draws used of total in width cells: # for used space, . for free.
func diskBar(used, total int64, width int) string {
	filled := 0
	if total > 0 {
		filled = int(float64(used) / float64(total) * float64(width))
	}
	filled = min(max(filled, 0), width)
	return strings.Repeat("#", filled) + strings.Repeat(".", width-filled)
}

// quarantineParts is "2 runs / 1.4 GB" and when the first can expire.
// Nothing expires by itself: a later sapu run deletes what is past its date.
func quarantineParts(runs []held, now time.Time) (value, hint string) {
	if len(runs) == 0 {
		return "Empty", "Nothing is held"
	}
	var bytes int64
	first := runs[0].ExpiresAfter
	for _, r := range runs {
		bytes += r.Bytes
		if r.ExpiresAfter.Before(first) {
			first = r.ExpiresAfter
		}
	}
	hint = "First can expire after " + first.Local().Format("Mon 2 Jan")
	if !first.After(now) {
		hint = "The oldest can expire now"
	}
	return fmt.Sprintf("%s / %s", plural(len(runs), "run"), output.Bytes(bytes)), hint
}
