package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

const gb = 1_000_000_000

func turnOn(t *testing.T, e env.Env, schedule, alerts bool) {
	t.Helper()
	c := DefaultConfig()
	c.Schedule.Enabled = schedule
	c.Schedule.Every = EveryDaily
	c.Alerts.Enabled = alerts
	if err := SaveConfig(e, c); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, e env.Env, now time.Time) {
	t.Helper()
	if err := Run(context.Background(), e, now); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func readLog(t *testing.T, e env.Env) string {
	t.Helper()
	b, err := os.ReadFile(LogPath(e))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func state(t *testing.T, e env.Env) State {
	t.Helper()
	st, err := LoadState(e)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestNotifyEscapes(t *testing.T) {
	f, _ := fake(t)
	if err := Notify(`Say "hi" \ bye`); err != nil {
		t.Fatal(err)
	}
	want := `display notification "Say \"hi\" \\ bye" with title "Sapu"`
	if len(f.scripts) != 1 || f.scripts[0] != want {
		t.Errorf("osascript %q, want %q", f.scripts, want)
	}
}

func TestLowSpaceAlertOncePerDay(t *testing.T) {
	f, e := fake(t)
	turnOn(t, e, false, true)
	f.free = 8 * gb
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	run(t, e, t0)
	if len(f.scripts) != 1 {
		t.Fatalf("notifications: %q", f.scripts)
	}
	for _, want := range []string{"8.0 GB free", "10.0 GB", "sapu explain", `with title "Sapu"`} {
		if !strings.Contains(f.scripts[0], want) {
			t.Errorf("notification lacks %q: %s", want, f.scripts[0])
		}
	}

	run(t, e, t0.Add(time.Hour))
	run(t, e, t0.Add(23*time.Hour))
	if len(f.scripts) != 1 {
		t.Errorf("notified again within 24 hours: %q", f.scripts)
	}
	run(t, e, t0.Add(24*time.Hour))
	if len(f.scripts) != 2 {
		t.Errorf("no notification after 24 hours: %q", f.scripts)
	}

	// Above the threshold: nothing.
	f.free = 11 * gb
	run(t, e, t0.Add(60*time.Hour))
	if len(f.scripts) != 2 {
		t.Errorf("notified above the threshold: %q", f.scripts)
	}

	st := state(t, e)
	if st.Notified[AlertLow] != t0.Add(24*time.Hour) {
		t.Errorf("notified[low] = %v", st.Notified[AlertLow])
	}
	if st.LastCheck == nil || !st.LastCheck.Equal(t0.Add(60*time.Hour)) {
		t.Errorf("last_check = %v", st.LastCheck)
	}
	log := readLog(t, e)
	if n := strings.Count(log, "alerts: "); n != 5 {
		t.Errorf("%d alert lines, want one per run:\n%s", n, log)
	}
	if !strings.Contains(log, "below 10.0 GB (notified)") || !strings.Contains(log, "(already notified today)") {
		t.Errorf("log:\n%s", log)
	}
}

func TestDropAlert(t *testing.T) {
	f, e := fake(t)
	turnOn(t, e, false, true)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	// A fall spread over more than 24 hours is not an alert.
	f.free = 60 * gb
	run(t, e, t0)
	f.free = 50 * gb
	run(t, e, t0.Add(25*time.Hour))
	if len(f.scripts) != 0 {
		t.Fatalf("notified for a fall over 25 hours: %q", f.scripts)
	}

	// 2 GB in an hour: below the 5 GB threshold.
	f.free = 48 * gb
	run(t, e, t0.Add(26*time.Hour))
	if len(f.scripts) != 0 {
		t.Fatalf("notified for a 2 GB drop: %q", f.scripts)
	}

	// 6 GB below the highest sample of the last 24 hours.
	f.free = 44 * gb
	run(t, e, t0.Add(27*time.Hour))
	if len(f.scripts) != 1 {
		t.Fatalf("no notification for a 6 GB drop: %q", f.scripts)
	}
	for _, want := range []string{"fell by 6.0 GB", "44.0 GB", "sapu explain"} {
		if !strings.Contains(f.scripts[0], want) {
			t.Errorf("notification lacks %q: %s", want, f.scripts[0])
		}
	}

	// Still falling, but only once a day.
	f.free = 30 * gb
	run(t, e, t0.Add(28*time.Hour))
	if len(f.scripts) != 1 {
		t.Errorf("second drop notification within a day: %q", f.scripts)
	}
}

func TestSamplesKeptAboutEightDays(t *testing.T) {
	f, e := fake(t)
	turnOn(t, e, false, true)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	old := State{Samples: []Sample{
		{Time: now.Add(-9 * 24 * time.Hour), FreeBytes: 1},
		{Time: now.Add(-7 * 24 * time.Hour), FreeBytes: 2},
	}}
	if err := saveState(e, old); err != nil {
		t.Fatal(err)
	}
	f.free = 50 * gb
	run(t, e, now)
	st := state(t, e)
	if len(st.Samples) != 2 || st.Samples[0].FreeBytes != 2 || st.Samples[1] != (Sample{Time: now, FreeBytes: 50 * gb}) {
		t.Errorf("samples: %+v", st.Samples)
	}
}

// A fixture home: safe items (logs.homebrew, node-gyp.cache), and files that
// must stay: a moderate item (npm.cache), a risky one and a safe official
// command (test rules), and a document no rule targets.
var (
	safeItems = []string{"Library/Logs/Homebrew/x", "Library/Caches/node-gyp"}
	keptFiles = []string{".npm/_cacache/blob", "Library/Caches/risky-app/data", "Library/Caches/safe-command/c", "Documents/important.txt"}
)

func fixtureHome(t *testing.T, e env.Env) {
	t.Helper()
	for _, rel := range []string{
		"Library/Logs/Homebrew/x/build.log",
		"Library/Caches/node-gyp/headers.tar",
	} {
		put(t, filepath.Join(e.Home, rel), "junk "+rel)
	}
	for _, rel := range keptFiles {
		put(t, filepath.Join(e.Home, rel), "keep "+rel)
	}
}

// withTestRules adds a risky rule and a safe official command to the
// built-in rules, and records what Run hands to engine.Apply.
func withTestRules(t *testing.T) (plans *[]*plan.Plan, opts *[]engine.Options) {
	t.Helper()
	oldRules, oldApply := cleanerRules, apply
	t.Cleanup(func() { cleanerRules, apply = oldRules, oldApply })
	yes := true
	cleanerRules = func(e env.Env) ([]rules.Rule, error) {
		rs, err := oldRules(e)
		return append(rs,
			rules.Rule{ID: "test.risky", Category: "apps", Paths: []string{"~/Library/Caches/risky-app"},
				Risk: rules.RiskRisky, Regenerates: &yes, Explain: map[string]string{"en": "risky"}, Action: rules.ActionQuarantine},
			rules.Rule{ID: "test.safe-command", Category: "dev", Paths: []string{"~/Library/Caches/safe-command"},
				Risk: rules.RiskSafe, Regenerates: &yes, Explain: map[string]string{"en": "command"},
				Action: rules.ActionOfficialCommand, Command: []string{"true"}},
		), err
	}
	plans, opts = &[]*plan.Plan{}, &[]engine.Options{}
	apply = func(ctx context.Context, e env.Env, p *plan.Plan, o engine.Options) (*engine.Result, error) {
		*plans = append(*plans, p)
		*opts = append(*opts, o)
		return engine.Apply(ctx, e, p, o)
	}
	return plans, opts
}

func TestScheduledCleanMovesOnlySafeItems(t *testing.T) {
	f, e := fake(t)
	plans, opts := withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, false)

	now := time.Now()
	run(t, e, now)

	if len(*opts) != 1 {
		t.Fatalf("Apply called %d times", len(*opts))
	}
	if o := (*opts)[0]; o.Permanent || o.RunCommands {
		t.Errorf("Apply options: Permanent=%v RunCommands=%v, want both false", o.Permanent, o.RunCommands)
	}
	for _, it := range (*plans)[0].Items {
		if it.Risk != rules.RiskSafe || it.Action != rules.ActionQuarantine {
			t.Errorf("plan has %s (%s, %s); only safe quarantine items belong in it", it.Path, it.Risk, it.Action)
		}
	}

	for _, rel := range safeItems {
		if exists(filepath.Join(e.Home, rel)) {
			t.Errorf("safe item %s was not moved", rel)
		}
	}
	for _, rel := range keptFiles {
		if b, err := os.ReadFile(filepath.Join(e.Home, rel)); err != nil || string(b) != "keep "+rel {
			t.Errorf("%s changed: %q, %v", rel, b, err)
		}
	}

	runs, err := runlog.List(e)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	id := runs[0].ID
	if runs[0].Kind != plan.KindClean || runs[0].Items != len(safeItems) {
		t.Errorf("run: %+v", runs[0])
	}

	st := state(t, e)
	lc := st.LastClean
	if lc == nil || lc.Result != CleanMoved || lc.RunID != id || lc.Items != len(safeItems) || lc.Bytes != runs[0].Bytes || !lc.Time.Equal(now) {
		t.Errorf("last clean: %+v (run %+v)", lc, runs[0])
	}

	want := `display notification "Sapu moved ` + output.Bytes(runs[0].Bytes) +
		` of caches and logs to quarantine. Undo: sapu undo ` + id + `" with title "Sapu"`
	if len(f.scripts) != 1 || f.scripts[0] != want {
		t.Errorf("notifications %q, want %q", f.scripts, want)
	}
	if log := readLog(t, e); !strings.Contains(log, "clean: moved 2 items") || !strings.Contains(log, id) {
		t.Errorf("log:\n%s", log)
	}
}

func TestScheduledCleanOnlyWhenDue(t *testing.T) {
	f, e := fake(t)
	plans, _ := withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, false) // daily
	now := time.Now()
	last := &Clean{Time: now.Add(-23 * time.Hour), Result: CleanNothing}
	if err := saveState(e, State{LastClean: last}); err != nil {
		t.Fatal(err)
	}

	run(t, e, now)
	if len(*plans) != 0 {
		t.Fatal("cleaned before it was due")
	}
	for _, rel := range safeItems {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s moved before the clean was due", rel)
		}
	}
	if len(f.scripts) != 0 {
		t.Errorf("notifications: %q", f.scripts)
	}
	if log := readLog(t, e); !strings.Contains(log, "clean: not due until") {
		t.Errorf("log:\n%s", log)
	}

	run(t, e, now.Add(time.Hour))
	if len(*plans) != 1 {
		t.Error("no clean once it was due")
	}
}

func TestScheduledCleanWithNothingToMoveIsQuiet(t *testing.T) {
	f, e := fake(t)
	turnOn(t, e, true, false)
	now := time.Now()
	run(t, e, now)
	if len(f.scripts) != 0 {
		t.Errorf("notified with nothing to move: %q", f.scripts)
	}
	if lc := state(t, e).LastClean; lc == nil || lc.Result != CleanNothing || lc.RunID != "" {
		t.Errorf("last clean: %+v", lc)
	}
	if log := readLog(t, e); !strings.Contains(log, "clean: nothing to move") {
		t.Errorf("log:\n%s", log)
	}
}

func TestFailedCleanNotifiesOncePerDay(t *testing.T) {
	f, e := fake(t)
	withTestRules(t)
	apply = func(context.Context, env.Env, *plan.Plan, engine.Options) (*engine.Result, error) {
		return nil, errors.New("disk on fire")
	}
	turnOn(t, e, true, false)
	t0 := time.Now()

	if err := Run(context.Background(), e, t0); err == nil {
		t.Error("Run hid the failure")
	}
	want := `display notification "Sapu's scheduled run failed: clean: disk on fire" with title "Sapu"`
	if len(f.scripts) != 1 || f.scripts[0] != want {
		t.Errorf("notifications %q, want %q", f.scripts, want)
	}
	if lc := state(t, e).LastClean; lc == nil || lc.Result != CleanFailed || lc.Error != "disk on fire" {
		t.Errorf("last clean: %+v", lc)
	}
	if log := readLog(t, e); !strings.Contains(log, "error: clean: disk on fire") {
		t.Errorf("log:\n%s", log)
	}

	// Due again after a day, fails again, but notifies only once a day.
	_ = Run(context.Background(), e, t0.Add(24*time.Hour-time.Minute))
	if len(f.scripts) != 1 {
		t.Errorf("notifications: %q", f.scripts)
	}
}

func TestUnreadableConfigIsReported(t *testing.T) {
	f, e := fake(t)
	put(t, ConfigPath(e), "{not json")
	if err := Run(context.Background(), e, time.Now()); err == nil {
		t.Error("Run hid an unreadable config")
	}
	if len(f.scripts) != 1 || !strings.Contains(f.scripts[0], "Sapu's scheduled run failed: read the settings") {
		t.Errorf("notifications: %q", f.scripts)
	}
	if log := readLog(t, e); !strings.Contains(log, "error: read the settings") {
		t.Errorf("log:\n%s", log)
	}
}

// Another sapu holding the lock: Run returns nil and changes nothing, not
// even its own log or state.
func TestHeldLockMeansNoChanges(t *testing.T) {
	f, e := fake(t)
	withTestRules(t)
	fixtureHome(t, e)
	turnOn(t, e, true, true)
	f.free = 1 * gb // would alert

	lock, err := os.OpenFile(filepath.Join(e.StateDir, "lock"), os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	run(t, e, time.Now())

	for _, rel := range safeItems {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s moved while another sapu held the lock", rel)
		}
	}
	for _, p := range []string{LogPath(e), StatePath(e), e.RunsDir(), e.QuarantineDir()} {
		if exists(p) {
			t.Errorf("%s written while another sapu held the lock", p)
		}
	}
	if len(f.scripts) != 0 || len(f.launchctl) != 0 {
		t.Errorf("osascript %q, launchctl %q", f.scripts, f.launchctl)
	}
}

func TestNothingOnLogsAndDoesNothing(t *testing.T) {
	f, e := fake(t)
	fixtureHome(t, e)
	run(t, e, time.Now())
	if log := readLog(t, e); !strings.Contains(log, "nothing is on") {
		t.Errorf("log:\n%s", log)
	}
	if len(f.scripts) != 0 || exists(e.RunsDir()) {
		t.Error("Run did something with everything off")
	}
}

func TestSnapshotOnceADayWhenSomethingIsOn(t *testing.T) {
	_, e := fake(t)
	fixtureHome(t, e)
	turnOn(t, e, false, true)
	t0 := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	run(t, e, t0)
	run(t, e, t0.Add(time.Hour)) // same day: no second walk
	if log := readLog(t, e); strings.Count(log, "trends: saved") != 1 || !strings.Contains(log, "today's snapshot exists") {
		t.Errorf("log:\n%s", log)
	}
	files, err := os.ReadDir(filepath.Join(e.StateDir, "trends"))
	if err != nil || len(files) != 1 || files[0].Name() != "2026-11-08.json" {
		t.Fatalf("snapshots: %v, %v", files, err)
	}

	run(t, e, t0.Add(24*time.Hour))
	if files, _ := os.ReadDir(filepath.Join(e.StateDir, "trends")); len(files) != 2 {
		t.Errorf("the next day needs its own snapshot: %v", files)
	}
}

func TestNoSnapshotWithEverythingOff(t *testing.T) {
	_, e := fake(t)
	fixtureHome(t, e)
	run(t, e, time.Now())
	if exists(filepath.Join(e.StateDir, "trends")) {
		t.Error("a snapshot was taken with schedule and alerts off")
	}
}
