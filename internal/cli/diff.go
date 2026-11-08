package cli

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/trends"
)

const diffLong = `Show which folders grew or shrank between two size snapshots.

A snapshot is the size of your home folder and of every folder in it that
uses 100 MB or more, saved once a day in ~/.sapu/trends. Diff compares the
newest snapshot with the newest one that is at least --since older, and lists
the folders that changed most. A folder is listed once, at the deepest level
that explains the change.

Take snapshots with "sapu diff --record" (a read-only walk of your home
folder that writes only the snapshot file), or turn on "sapu alerts on" and
the background agent takes one a day. Snapshots are small and are kept.`

// maxChanges is how many folders of growth and of shrink diff lists.
const maxChanges = 10

// diffData is the data of `sapu diff --json`.
type diffData struct {
	SinceDays int              `json:"since_days"`
	Snapshots int              `json:"snapshots"`          // snapshots on disk
	Recorded  *trends.Snapshot `json:"recorded,omitempty"` // with --record: the snapshot just saved
	Diff      *trends.Diff     `json:"diff,omitempty"`     // absent without two snapshots far enough apart
	Hint      string           `json:"hint,omitempty"`     // why there is no diff, and what to do
}

func newDiffCmd() *cobra.Command {
	var since string
	var record bool
	cmd := &cobra.Command{
		Use:   "diff [--since 7d]",
		Short: "Show which folders grew the most since an earlier day",
		Long:  diffLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "diff", runDiff(cmd, since, record))
		},
	}
	cmd.Flags().StringVar(&since, "since", "7d", "how far back to compare: 7d, 30d, 2w")
	cmd.Flags().BoolVar(&record, "record", false, "walk the home folder now and save today's snapshot")
	return cmd
}

// parseSince reads "7d" or "2w" as a number of days.
func parseSince(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(s, "d"), "w"))
	if err != nil || n < 1 || n > 3650 || !strings.HasSuffix(s, "d") && !strings.HasSuffix(s, "w") {
		return 0, usageErr(fmt.Errorf("--since must be a number of days or weeks such as 7d or 2w, not %q", s))
	}
	if strings.HasSuffix(s, "w") {
		n *= 7
	}
	return n, nil
}

func runDiff(cmd *cobra.Command, since string, record bool) error {
	days, err := parseSince(since)
	if err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	out := humanOut(cmd)
	data := diffData{SinceDays: days}

	if record {
		p := newProgress(cmd)
		p.Status("scan", "Measuring "+output.Tilde(e.Home, e.Home)+" (about a minute)…")
		s, err := trends.Record(cmd.Context(), e, time.Now(), p.Scan)
		p.Done()
		if err != nil {
			return err
		}
		data.Recorded = &s
		fmt.Fprintf(out, "Saved the sizes of %s: your home folder is %s, with %s of 100 MB or more.\n",
			s.Date, output.Bytes(s.HomeBytes), plural(len(s.Folders), "folder"))
		if len(s.Unmeasured) > 0 {
			fmt.Fprintf(out, "Left out because they did not answer in time or could not be read at all: %s. A stuck iCloud Drive folder often causes this.\n",
				strings.Join(s.Unmeasured, ", "))
		}
		fmt.Fprintln(out)
	}

	dates, err := trends.Dates(e)
	if err != nil {
		return err
	}
	data.Snapshots = len(dates)
	diff, err := trends.Changes(e, days, maxChanges)
	switch {
	case errors.Is(err, trends.ErrNoSnapshots), errors.Is(err, trends.ErrTooRecent):
		data.Hint = notEnoughText(dates, days)
		fmt.Fprintln(out, data.Hint)
	case err != nil:
		return err
	default:
		data.Diff = &diff
		writeDiff(out, e.Home, diff)
	}
	return printData(cmd, "diff", data)
}

// notEnoughText says why there is nothing to compare and how to get it.
func notEnoughText(dates []string, days int) string {
	var b strings.Builder
	switch len(dates) {
	case 0:
		fmt.Fprintf(&b, "No size snapshots yet. Diff needs two, at least %d days apart.\n", days)
	default:
		fmt.Fprintf(&b, "You have %s, from %s to %s. None is at least %d days older than the newest.\n",
			plural(len(dates), "snapshot"), dates[len(dates)-1], dates[0], days)
	}
	b.WriteString("  Take one now:   sapu diff --record\n")
	b.WriteString("  Take another in a few days, then run: sapu diff\n")
	b.WriteString("  Or let Sapu take one a day in the background: sapu alerts on")
	if len(dates) > 1 {
		oldest, _ := time.Parse("2006-01-02", dates[len(dates)-1])
		newest, _ := time.Parse("2006-01-02", dates[0])
		if span := int(newest.Sub(oldest).Hours() / 24); span >= 1 && span < days {
			fmt.Fprintf(&b, "\n  To compare what you have: sapu diff --since %dd", span)
		}
	}
	return b.String()
}

func writeDiff(w io.Writer, home string, d trends.Diff) {
	st := newStyle(w)
	fmt.Fprintf(w, "%s\n", st.bold(fmt.Sprintf("Compared %s with %s", d.Newer, d.Older)))
	fmt.Fprintln(w, row(2, "Your home folder", fmt.Sprintf("%s, was %s (%s)", output.Bytes(d.HomeAfter),
		output.Bytes(d.HomeBefore), signedBytes(d.HomeAfter-d.HomeBefore))))
	if len(d.Unmeasured) > 0 {
		fmt.Fprintf(w, "  Not compared, because a snapshot left them out: %s\n", strings.Join(d.Unmeasured, ", "))
	}
	if len(d.Unreadable) > 0 {
		fmt.Fprintf(w, "  Not compared, because what Sapu could read there changed (Full Disk Access?): %s\n", strings.Join(d.Unreadable, ", "))
	}

	approx := false
	list := func(title, none string, changes []trends.Change) {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(title))
		if len(changes) == 0 {
			fmt.Fprintln(w, "  "+none)
		}
		for _, c := range changes {
			size := signedBytes(c.Delta)
			if c.Approx {
				size = "about " + size
				approx = true
			}
			right := size + "  now " + output.Bytes(c.After)
			path := tilde(home, home+"/"+c.Path)
			room := tableWidth - 2 - len([]rune(right)) - 2
			if r := []rune(path); len(r) > room {
				path = "…" + string(r[len(r)-room+1:])
			}
			fmt.Fprintln(w, row(2, path, right))
		}
	}
	list("Biggest growth", "Nothing grew by 10 MB or more.", d.Growth)
	list("Biggest shrink", "Nothing shrank by 10 MB or more.", d.Shrink)

	fmt.Fprintln(w)
	if approx {
		writeWrapped(w, st, 0, "\"about\": the folder was under 100 MB in one of the two snapshots, so the change is an estimate.")
	}
	writeWrapped(w, st, 0, "Only folders of 100 MB or more are measured. Sizes are what the disk really uses.")
}
