package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/explain"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/runlog"
	"github.com/khmuhtadin/sapu/internal/trends"
)

const gb = 1_000_000_000

// fakeDisk replaces the disk and swap readings for one test. free can be
// changed during the test.
type fakeDisk struct{ total, free int64 }

func fakeDiskSpace(t *testing.T, total, free int64) *fakeDisk {
	t.Helper()
	f := &fakeDisk{total: total, free: free}
	oldDisk, oldSwap := diskUsage, swapUsage
	diskUsage = func(string) (int64, int64, error) { return f.total, f.free, nil }
	swapUsage = func() (int64, int64, error) { return 1 * gb, 3 * gb, nil }
	t.Cleanup(func() { diskUsage, swapUsage = oldDisk, oldSwap })
	return f
}

func saveSnapshot(t *testing.T, e env.Env, date string, taken time.Time, home int64, folders ...trends.Folder) {
	t.Helper()
	s := trends.Snapshot{Date: date, Taken: taken, HomeBytes: home, MinBytes: trends.MinBytes, Folders: folders}
	if err := trends.Save(e, s); err != nil {
		t.Fatal(err)
	}
}

func twoWeeks(t *testing.T, e env.Env) {
	t.Helper()
	old := time.Date(2026, 11, 1, 9, 0, 0, 0, time.Local)
	saveSnapshot(t, e, "2026-11-01", old, 100*gb,
		trends.Folder{Path: "Library", Bytes: 50 * gb},
		trends.Folder{Path: "Library/Caches", Bytes: 20 * gb},
		trends.Folder{Path: "Library/Caches/CapCut", Bytes: 5 * gb},
		trends.Folder{Path: "Movies", Bytes: 30 * gb})
	saveSnapshot(t, e, "2026-11-08", old.AddDate(0, 0, 7), 140*gb,
		trends.Folder{Path: "Library", Bytes: 90 * gb},
		trends.Folder{Path: "Library/Caches", Bytes: 60 * gb},
		trends.Folder{Path: "Library/Caches/CapCut", Bytes: 43 * gb},
		trends.Folder{Path: "Movies", Bytes: 20 * gb})
}

// movedBytes is what the run id moved into quarantine, from the history.
func movedBytes(t *testing.T, e env.Env, id string) int64 {
	t.Helper()
	runs, err := runlog.List(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == id {
			return r.Bytes
		}
	}
	t.Fatalf("run %s not in the history", id)
	return 0
}

func TestDiffShowsBiggestGrowthAtTheDeepestFolder(t *testing.T) {
	e := env.New(t.TempDir())
	twoWeeks(t, e)
	out, errOut, code := sapu(t, e, "", "diff", "--since", "7d")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	checkHuman(t, out)
	for _, want := range []string{
		"Compared 2026-11-08 with 2026-11-01",
		"Biggest growth", "~/Library/Caches/CapCut", "+38.0 GB", "now 43.0 GB",
		"Biggest shrink", "~/Movies", "-10.0 GB",
		"140.0 GB, was 100.0 GB (+40.0 GB)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff output lacks %q:\n%s", want, out)
		}
	}
	// Library and Library/Caches grew only because CapCut did.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  ~/Library ") || strings.HasPrefix(line, "  ~/Library/Caches ") {
			t.Errorf("a parent folder is listed besides the folder that explains it: %q", line)
		}
	}
}

func TestDiffJSON(t *testing.T) {
	e := env.New(t.TempDir())
	twoWeeks(t, e)
	out, _, code := sapu(t, e, "", "diff", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "diff", out)
	var doc struct {
		Data struct {
			Snapshots int
			Diff      struct {
				Older, Newer string
				Growth       []struct {
					Path  string
					Delta int64
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	d := doc.Data
	if d.Snapshots != 2 || d.Diff.Older != "2026-11-01" || len(d.Diff.Growth) != 1 ||
		d.Diff.Growth[0].Path != "Library/Caches/CapCut" || d.Diff.Growth[0].Delta != 38*gb {
		t.Errorf("data %+v", d)
	}
}

func TestDiffWithoutEnoughSnapshotsSaysHowToGetThem(t *testing.T) {
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "diff")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	checkHuman(t, out)
	for _, want := range []string{"No size snapshots yet", "sapu diff --record", "sapu alerts on"} {
		if !strings.Contains(out, want) {
			t.Errorf("no-data output lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = sapu(t, e, "", "diff", "--json")
	validate(t, "diff", out)

	// One snapshot is not enough either.
	saveSnapshot(t, e, "2026-11-08", time.Now(), gb)
	out, _, code = sapu(t, e, "", "diff")
	if code != 0 || !strings.Contains(out, "1 snapshot") || !strings.Contains(out, "sapu diff --record") {
		t.Errorf("exit %d\n%s", code, out)
	}
	out, _, _ = sapu(t, e, "", "diff", "--json")
	validate(t, "diff", out)

	// Two snapshots a day apart: suggest the shorter window.
	saveSnapshot(t, e, "2026-11-07", time.Now(), gb)
	out, _, _ = sapu(t, e, "", "diff")
	if !strings.Contains(out, "sapu diff --since 1d") {
		t.Errorf("no suggestion of a shorter window:\n%s", out)
	}
}

func TestDiffRejectsBadSince(t *testing.T) {
	e := env.New(t.TempDir())
	for _, since := range []string{"", "7", "d", "0d", "-3d", "soon", "7h"} {
		if _, _, code := sapu(t, e, "", "diff", "--since", since); code != 2 {
			t.Errorf("--since %q: exit %d, want 2", since, code)
		}
	}
	for since, wantDays := range map[string]int{"7d": 7, "30d": 30, "2w": 14} {
		if got, err := parseSince(since); err != nil || got != wantDays {
			t.Errorf("parseSince(%q) = %d, %v; want %d", since, got, err, wantDays)
		}
	}
}

func TestDiffRecordWritesOnlyOneSnapshotADay(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)

	for range 2 {
		out, errOut, code := sapu(t, e, "", "diff", "--record")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if !strings.Contains(out, "Saved the sizes of "+trends.Date(time.Now())) {
			t.Errorf("output:\n%s", out)
		}
	}
	files, err := os.ReadDir(trends.Dir(e))
	if err != nil || len(files) != 1 || files[0].Name() != trends.Date(time.Now())+".json" {
		t.Fatalf("snapshot files: %v, %v", files, err)
	}
	sameSnapshot(t, before, snapshot(t, e)) // the home folder is untouched
	if exists(e.RunsDir()) || exists(e.QuarantineDir()) {
		t.Error("--record created runs or quarantine")
	}

	out, _, code := sapu(t, e, "", "diff", "--record", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "diff", out)
	if !strings.Contains(out, `"recorded"`) {
		t.Errorf("--json lacks the recorded snapshot:\n%s", out)
	}
}

func TestExplainReconcileAddsUp(t *testing.T) {
	e := env.New(t.TempDir())
	fakeDiskSpace(t, 500*gb, 120*gb) // used 380
	// The real rows outside home must not decide the sum: a CI Mac with
	// Xcode has more system assets than this fake disk has used.
	measure := explainRows
	t.Cleanup(func() { explainRows = measure })
	explainRows = func(ctx context.Context, e env.Env) ([]explain.Row, error) {
		rows, err := measure(ctx, e)
		for i := range rows {
			switch rows[i].ID {
			case "spotlight":
				rows[i].Bytes = 2 * gb
			case "system_assets":
				rows[i].Bytes = 30 * gb
			case "swap":
				rows[i].Bytes, rows[i].Unmeasured = -1, "needs admin"
			}
		}
		return rows, err
	}
	saveSnapshot(t, e, trends.Date(time.Now()), time.Now().Add(-3*time.Hour), 300*gb)

	out, errOut, code := sapu(t, e, "", "explain", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "explain", out)
	var doc struct{ Data explainData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	r := doc.Data.Reconcile
	if r == nil {
		t.Fatal("no reconcile in --json")
	}
	if r.TotalBytes != 500*gb || r.UsedBytes != 380*gb || r.HomeBytes != 300*gb {
		t.Errorf("reconcile %+v", *r)
	}
	if got := r.HomeBytes + r.SystemBytes + r.RestBytes; got != r.UsedBytes {
		t.Errorf("home + system + rest = %d, used = %d", got, r.UsedBytes)
	}

	text, _, _ := sapu(t, e, "", "explain")
	checkHuman(t, text)
	for _, want := range []string{
		"How this adds up", "Used on disk", "380.0 GB", "Your home folder", "300.0 GB",
		"System Data parts Sapu measured", "The rest (estimate)", "apps in /Applications", "could not read or measure", "estimate",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("explain lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasSuffix(text, "Sapu never runs these.\n") {
		t.Errorf("explain must still close with the promise:\n%s", text)
	}
}

func TestExplainReconcileWithoutHomeSnapshot(t *testing.T) {
	e := env.New(t.TempDir())
	fakeDiskSpace(t, 500*gb, 120*gb)
	// A snapshot older than 24 hours is not used.
	saveSnapshot(t, e, "2026-11-01", time.Now().Add(-30*time.Hour), 300*gb)

	out, _, _ := sapu(t, e, "", "explain", "--json")
	validate(t, "explain", out)
	var doc struct{ Data explainData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	if r := doc.Data.Reconcile; r == nil || r.HomeBytes != -1 || r.RestBytes != -1 || r.UsedBytes != 380*gb {
		t.Errorf("reconcile %+v", r)
	}
	text, _, _ := sapu(t, e, "", "explain")
	for _, want := range []string{"not measured", "sapu diff --record", "not known"} {
		if !strings.Contains(text, want) {
			t.Errorf("explain lacks %q:\n%s", want, text)
		}
	}
}

func TestReconcileTextMarksEstimates(t *testing.T) {
	var sb strings.Builder
	taken := time.Now().Add(-2 * time.Hour)
	writeReconcile(&sb, explain.Reconciliation{TotalBytes: 500 * gb, UsedBytes: 380 * gb, HomeBytes: 300 * gb, HomeTaken: &taken,
		SystemBytes: 5 * gb, SystemUnmeasured: 2, RestBytes: 75 * gb}, time.Now())
	out := sb.String()
	checkHuman(t, out)
	for _, want := range []string{"2 h ago", "at least this much, 2", "The rest (estimate)", "75.0 GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("reconcile text lacks %q:\n%s", want, out)
		}
	}
}

func TestQuarantineRunShowsFreeSpaceNowAndAfterEmpty(t *testing.T) {
	e := newHome(t)
	fakeDiskSpace(t, 500*gb, 12_400_000_000)

	out, errOut, code := sapu(t, e, "", "clean", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	id := runIDOf(t, out)
	moved := movedBytes(t, e, id)
	want := "Free space now 12.4 GB; after `sapu quarantine empty " + id + "`: about " + output.Bytes(12_400_000_000+moved) + "."
	if !strings.Contains(out, want) {
		t.Errorf("result lacks %q:\n%s", want, out)
	}
	if !strings.Contains(out, "The space is not free yet") {
		t.Errorf("result must say the space is not free yet:\n%s", out)
	}
}

func TestPermanentRunShowsTheRealFreeSpace(t *testing.T) {
	e := newHome(t)
	fakeDiskSpace(t, 500*gb, 12_400_000_000)

	out, errOut, code := sapu(t, e, "", "clean", "--permanent", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Free space now 12.4 GB; this run freed about ") {
		t.Errorf("result lacks the real free space:\n%s", out)
	}
	if strings.Contains(out, "after `sapu quarantine empty") {
		t.Errorf("a --permanent run has nothing left to empty:\n%s", out)
	}
}

func TestNothingMovedShowsNoFreeSpaceLine(t *testing.T) {
	e := env.New(t.TempDir())
	fakeDiskSpace(t, 500*gb, 12*gb)
	out, _, code := sapu(t, e, "", "clean", "--yes")
	if code != 0 || strings.Contains(out, "Free space now") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestStatusWithoutData(t *testing.T) {
	e := env.New(t.TempDir())
	fakeAgent(t, e)
	fakeDiskSpace(t, 500*gb, 120*gb)

	out, errOut, code := sapu(t, e, "", "status")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	checkHuman(t, out)
	for _, want := range []string{
		"380.0 GB used of 500.0 GB, 120.0 GB free",
		"Quarantine", "empty",
		"Scheduled cleanup", "off (turn on: sapu schedule on)",
		"Alerts", "off (turn on: sapu alerts on)",
		"Full Disk Access", "Swap in use", "1.0 GB of 3.0 GB",
		"Growth, 7 days", "no data yet", "sapu diff --record",
		"Last runs", "No runs yet.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	noState(t, e) // status only reads

	out, _, code = sapu(t, e, "", "status", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "status", out)
	if strings.Contains(out, `"growth"`) {
		t.Errorf("growth must be absent without snapshots:\n%s", out)
	}
}

func TestStatusWithData(t *testing.T) {
	e := newHome(t)
	f := fakeAgent(t, e)
	fakeDiskSpace(t, 500*gb, 120*gb)
	twoWeeks(t, e)

	// A quarantine run and a few more runs, newest last.
	var ids []string
	for i := range 4 {
		out, errOut, code := sapu(t, e, "", "clean", "--yes")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		ids = append(ids, runIDOf(t, out))
		for rel, data := range junk { // put the junk back so the next run has work
			writeFile(t, filepath.Join(e.Home, rel), data)
		}
		if i < 3 {
			time.Sleep(1100 * time.Millisecond) // run ids change once a second
		}
	}
	if _, _, code := sapu(t, e, "", "schedule", "on", "--every", "daily"); code != 0 {
		t.Fatal("schedule on failed")
	}
	f.loaded = true

	out, errOut, code := sapu(t, e, "", "status")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	checkHuman(t, out)
	for _, want := range []string{
		"Quarantine", "4 runs", "the earliest can expire after",
		"on, daily, safe caches only",
		"Growth, 7 days", "~/Library/Caches/CapCut +38.0 GB",
		"Last runs", ids[3], ids[2], ids[1],
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, ids[0]) {
		t.Errorf("status lists more than the last 3 runs:\n%s", out)
	}

	out, _, code = sapu(t, e, "", "status", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	validate(t, "status", out)
	var doc struct{ Data statusData }
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	d := doc.Data
	if d.Quarantine.Runs != 4 || len(d.LastRuns) != 3 || !d.Schedule.Enabled || d.Growth == nil ||
		d.Growth.Biggest == nil || d.Growth.Biggest.Delta != 38*gb || d.Swap == nil {
		t.Errorf("data %+v", d)
	}
}
