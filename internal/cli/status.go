package cli

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/agent"
	"github.com/khmuhtadin/sapu/internal/doctor"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/runlog"
	"github.com/khmuhtadin/sapu/internal/trends"
)

// growthDays is how far back status looks for growth.
const growthDays = 7

// lastRuns is how many runs status lists.
const lastRuns = 3

// diskUsage returns the size and the free bytes of the volume that holds
// path. Tests replace it.
var diskUsage = func(path string) (total, free int64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("read disk space: %w", err)
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize), nil
}

// swapUsage returns the swap in use and its size. Tests replace it.
var swapUsage = func() (used, total int64, err error) {
	// struct xsw_usage: total, avail, used, each a uint64.
	b, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return 0, 0, err
	}
	if len(b) < 24 {
		return 0, 0, errors.New("unexpected vm.swapusage size")
	}
	return int64(binary.NativeEndian.Uint64(b[16:24])), int64(binary.NativeEndian.Uint64(b[0:8])), nil
}

type statusDisk struct {
	TotalBytes int64 `json:"total_bytes"`
	UsedBytes  int64 `json:"used_bytes"`
	FreeBytes  int64 `json:"free_bytes"`
}

type statusQuarantine struct {
	Runs           int        `json:"runs"`
	Bytes          int64      `json:"bytes"`
	EarliestExpiry *time.Time `json:"earliest_expiry,omitempty"` // earliest time a later run may expire a run
}

type statusSchedule struct {
	Enabled   bool         `json:"enabled"`
	Every     string       `json:"every"`
	NextClean *time.Time   `json:"next_clean,omitempty"`
	LastClean *agent.Clean `json:"last_clean,omitempty"`
}

type statusAlerts struct {
	Enabled         bool   `json:"enabled"`
	MinFreeBytes    int64  `json:"min_free_bytes"`
	DropBytesPerDay int64  `json:"drop_bytes_per_day"`
	FreeBytes       *int64 `json:"free_bytes,omitempty"` // free space at the last sample
}

type statusSwap struct {
	UsedBytes  int64 `json:"used_bytes"`
	TotalBytes int64 `json:"total_bytes"`
}

// statusGrowth is the biggest growth over SinceDays. It is left out of the
// data when there are not two snapshots that far apart.
type statusGrowth struct {
	SinceDays int            `json:"since_days"`
	Older     string         `json:"older"`
	Newer     string         `json:"newer"`
	Biggest   *trends.Change `json:"biggest,omitempty"` // absent when nothing grew
}

// statusData is the data of `sapu status --json`.
type statusData struct {
	Disk           statusDisk       `json:"disk"`
	Quarantine     statusQuarantine `json:"quarantine"`
	LastRuns       []runlog.Summary `json:"last_runs"`
	Schedule       statusSchedule   `json:"schedule"`
	Alerts         statusAlerts     `json:"alerts"`
	LastCheck      *time.Time       `json:"last_check,omitempty"` // last hourly run of the agent
	Agent          agent.Info       `json:"agent"`
	FullDiskAccess bool             `json:"full_disk_access"`
	Swap           *statusSwap      `json:"swap,omitempty"` // absent when it cannot be read
	Growth         *statusGrowth    `json:"growth,omitempty"`
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Disk, quarantine, last runs and schedule at a glance",
		Long: `Show the state of Sapu and the disk on one screen.

Status only reads, and it is quick: it never walks your folders. The growth
line comes from the size snapshots of sapu diff.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "status", runStatus(cmd))
		},
	}
}

func runStatus(cmd *cobra.Command) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	now := time.Now()
	d, err := gatherStatus(e, now)
	if err != nil {
		return err
	}
	writeStatus(humanOut(cmd), e, d, now)
	return printData(cmd, "status", d)
}

func gatherStatus(e env.Env, now time.Time) (statusData, error) {
	var d statusData
	total, free, err := diskUsage(e.Home)
	if err != nil {
		return d, err
	}
	d.Disk = statusDisk{TotalBytes: total, UsedBytes: total - free, FreeBytes: free}

	held, err := heldRuns(e)
	if err != nil {
		return d, err
	}
	d.Quarantine.Runs = len(held)
	for _, r := range held {
		d.Quarantine.Bytes += r.Bytes
		if d.Quarantine.EarliestExpiry == nil || r.ExpiresAfter.Before(*d.Quarantine.EarliestExpiry) {
			t := r.ExpiresAfter
			d.Quarantine.EarliestExpiry = &t
		}
	}

	runs, err := runlog.List(e)
	if err != nil {
		return d, err
	}
	d.LastRuns = runs[:min(lastRuns, len(runs))]
	if d.LastRuns == nil {
		d.LastRuns = []runlog.Summary{}
	}

	c, err := agent.LoadConfig(e)
	if err != nil {
		return d, err
	}
	st, err := agent.LoadState(e)
	if err != nil {
		return d, err
	}
	if d.Agent, err = agent.Inspect(e, c); err != nil {
		return d, err
	}
	d.Schedule = statusSchedule{Enabled: c.Schedule.Enabled, Every: c.Schedule.Every,
		NextClean: nextClean(c, st, now), LastClean: st.LastClean}
	d.Alerts = statusAlerts{Enabled: c.Alerts.Enabled, MinFreeBytes: c.Alerts.MinFreeBytes, DropBytesPerDay: c.Alerts.DropBytesPerDay}
	if n := len(st.Samples); n > 0 {
		d.Alerts.FreeBytes = &st.Samples[n-1].FreeBytes
	}
	d.LastCheck = st.LastCheck

	d.FullDiskAccess = doctor.HasFullDiskAccess(e)
	if used, total, err := swapUsage(); err == nil {
		d.Swap = &statusSwap{UsedBytes: used, TotalBytes: total}
	}

	diff, err := trends.Changes(e, growthDays, 1)
	switch {
	case errors.Is(err, trends.ErrNoSnapshots), errors.Is(err, trends.ErrTooRecent):
	case err != nil:
		return d, err
	default:
		d.Growth = &statusGrowth{SinceDays: growthDays, Older: diff.Older, Newer: diff.Newer}
		if len(diff.Growth) > 0 {
			d.Growth.Biggest = &diff.Growth[0]
		}
	}
	return d, nil
}

func writeStatus(w io.Writer, e env.Env, d statusData, now time.Time) {
	st := newStyle(w)
	line := func(label, format string, args ...any) {
		fmt.Fprintf(w, "%-20s%s\n", label, fmt.Sprintf(format, args...))
	}
	when := func(t time.Time) string { return t.Local().Format("Mon 2 Jan 15:04") }

	line("Disk", "%s used of %s, %s free", output.Bytes(d.Disk.UsedBytes), output.Bytes(d.Disk.TotalBytes), output.Bytes(d.Disk.FreeBytes))
	if q := d.Quarantine; q.Runs == 0 {
		line("Quarantine", "empty")
	} else {
		line("Quarantine", "%s, %s", plural(q.Runs, "run"), output.Bytes(q.Bytes))
		line("", "the earliest can expire after %s", q.EarliestExpiry.Local().Format("Mon 2 Jan"))
	}

	if d.Schedule.Enabled {
		text := "on, " + d.Schedule.Every + ", safe caches only"
		if n := d.Schedule.NextClean; n != nil && n.After(now) {
			text += "; next after " + when(*n)
		} else {
			text += "; due at the next hourly check"
		}
		line("Scheduled cleanup", "%s", text)
	} else {
		line("Scheduled cleanup", "off (turn on: sapu schedule on)")
	}
	if d.Alerts.Enabled {
		text := "on: " + alertRules(agent.Alerts{MinFreeBytes: d.Alerts.MinFreeBytes, DropBytesPerDay: d.Alerts.DropBytesPerDay})
		line("Alerts", "%s", output.Truncate(text, output.Width-20))
	} else {
		line("Alerts", "off (turn on: sapu alerts on)")
	}
	if d.LastCheck != nil {
		line("Last agent check", "%s", when(*d.LastCheck))
	}

	if d.FullDiskAccess {
		line("Full Disk Access", "yes")
	} else {
		line("Full Disk Access", "no: some folders cannot be measured (see sapu doctor)")
	}
	if d.Swap != nil {
		line("Swap in use", "%s of %s", output.Bytes(d.Swap.UsedBytes), output.Bytes(d.Swap.TotalBytes))
	}

	switch g := d.Growth; {
	case g == nil:
		line("Growth, 7 days", "no data yet: run sapu diff --record, and again in a week")
	case g.Biggest == nil:
		line("Growth, 7 days", "nothing grew by 10 MB or more since %s", g.Older)
	default:
		line("Growth, 7 days", "%s", growthText(e.Home, *g.Biggest))
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, st.bold("Last runs"))
	if len(d.LastRuns) == 0 {
		fmt.Fprintln(w, "  No runs yet.")
	} else {
		var rows [][]string
		for _, r := range d.LastRuns {
			rows = append(rows, []string{r.ID, r.Started.Local().Format("2006-01-02 15:04"), r.Kind,
				fmt.Sprint(r.Items), output.Bytes(r.Bytes), r.Status})
		}
		writeTable(w, []string{"Run", "Date", "Kind", "Items", "Size", "Status"}, rows, 3, 4)
	}

	for _, warn := range d.Agent.Warnings {
		fmt.Fprintf(w, "\nWarning: %s\n", warn)
	}
}

// growthText is "~/Library/Caches/CapCut +38.0 GB", the path cut at the front when too long.
func growthText(home string, c trends.Change) string {
	size := signedBytes(c.Delta)
	if c.Approx {
		size = "about " + size
	}
	path := tilde(home, home+"/"+c.Path)
	room := output.Width - 20 - len(size) - 1
	if r := []rune(path); len(r) > room {
		path = "…" + string(r[len(r)-room+1:])
	}
	return path + " " + size
}

// signedBytes is "+38.0 GB" or "-4.1 GB".
func signedBytes(n int64) string {
	if n < 0 {
		return "-" + output.Bytes(-n)
	}
	return "+" + output.Bytes(n)
}
