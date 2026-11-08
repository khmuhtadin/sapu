package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/agent"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// agentFake stands in for launchd, osascript and free space. Every test that
// runs schedule, alerts or agent commands uses it, so no test reaches the
// real launchd or shows a notification.
type agentFake struct {
	calls   []string // launchctl calls, arguments joined by spaces
	loaded  bool
	scripts []string // osascript scripts
	free    int64
	program string
}

func fakeAgent(t *testing.T, e env.Env) *agentFake {
	t.Helper()
	f := &agentFake{free: 100_000_000_000, program: filepath.Join(e.Home, "bin", "sapu")}
	writeFile(t, f.program, "sapu binary")
	oldL, oldO, oldF, oldE := agent.Launchctl, agent.Osascript, agent.FreeBytes, agent.Executable
	agent.Launchctl = func(args ...string) error {
		f.calls = append(f.calls, strings.Join(args, " "))
		switch args[0] {
		case "print":
			if !f.loaded {
				return errors.New("launchctl print: exit status 113")
			}
		case "bootstrap":
			f.loaded = true
		case "bootout":
			f.loaded = false
		}
		return nil
	}
	agent.Osascript = func(script string) error {
		f.scripts = append(f.scripts, script)
		return nil
	}
	agent.FreeBytes = func(env.Env) (int64, error) { return f.free, nil }
	agent.Executable = func() (string, error) { return f.program, nil }
	t.Cleanup(func() {
		agent.Launchctl, agent.Osascript, agent.FreeBytes, agent.Executable = oldL, oldO, oldF, oldE
	})
	return f
}

func (f *agentFake) changes() []string {
	var out []string
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "print ") {
			out = append(out, c)
		}
	}
	return out
}

func readConfig(t *testing.T, e env.Env) agent.Config {
	t.Helper()
	c, err := agent.LoadConfig(e)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonData(t *testing.T, out string, v any) {
	t.Helper()
	var env struct{ Data json.RawMessage }
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatal(err)
	}
}

func TestScheduleAndAlertsOnOff(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	service := domain + "/dev.sapu.agent"
	plist := filepath.Join(e.Home, "Library", "LaunchAgents", "dev.sapu.agent.plist")

	out, errOut, code := sapu(t, e, "", "schedule", "on")
	if code != 0 {
		t.Fatalf("schedule on: exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "Scheduled cleanup is on: weekly") || !strings.Contains(out, "loaded") {
		t.Errorf("schedule on:\n%s", out)
	}
	if c := readConfig(t, e); !c.Schedule.Enabled || c.Schedule.Every != "weekly" || c.Alerts.Enabled {
		t.Errorf("config: %+v", c)
	}
	b, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"<string>dev.sapu.agent</string>",
		"<string>" + f.program + "</string>\n\t\t<string>agent</string>\n\t\t<string>run</string>",
		"<key>StartInterval</key>\n\t<integer>3600</integer>",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q:\n%s", want, b)
		}
	}
	if got, want := f.changes(), []string{"bootstrap " + domain + " " + plist}; !slices.Equal(got, want) {
		t.Errorf("launchctl %q, want %q", got, want)
	}

	// An update boots the loaded agent out, then in again.
	f.calls = nil
	out, _, code = sapu(t, e, "", "schedule", "on", "--every", "daily", "--json")
	if code != 0 {
		t.Fatalf("schedule on --every daily: exit %d\n%s", code, out)
	}
	validate(t, "schedule", out)
	if got, want := f.changes(), []string{"bootout " + service, "bootstrap " + domain + " " + plist}; !slices.Equal(got, want) {
		t.Errorf("launchctl %q, want %q", got, want)
	}
	if c := readConfig(t, e); c.Schedule.Every != "daily" {
		t.Errorf("every = %q", c.Schedule.Every)
	}

	f.calls = nil
	out, _, code = sapu(t, e, "", "alerts", "on", "--min-free", "20GB", "--drop", "500MB", "--json")
	if code != 0 {
		t.Fatalf("alerts on: exit %d\n%s", code, out)
	}
	validate(t, "alerts", out)
	if c := readConfig(t, e); !c.Alerts.Enabled || c.Alerts.MinFreeBytes != 20_000_000_000 || c.Alerts.DropBytesPerDay != 500_000_000 {
		t.Errorf("config: %+v", c.Alerts)
	}

	// Schedule off while alerts are on: the agent stays.
	f.calls = nil
	out, _, code = sapu(t, e, "", "schedule", "off")
	if code != 0 || !strings.Contains(out, "Scheduled cleanup is off.") {
		t.Fatalf("schedule off: exit %d\n%s", code, out)
	}
	if !exists(plist) || slices.ContainsFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "bootout") }) {
		t.Errorf("the agent was removed while alerts are on: %q", f.calls)
	}

	// Both off: bootout, and the plist is gone.
	f.calls = nil
	out, _, code = sapu(t, e, "", "alerts", "off", "--json")
	if code != 0 {
		t.Fatalf("alerts off: exit %d\n%s", code, out)
	}
	validate(t, "alerts", out)
	if got, want := f.changes(), []string{"bootout " + service}; !slices.Equal(got, want) {
		t.Errorf("launchctl %q, want %q", got, want)
	}
	if exists(plist) {
		t.Error("plist still there with schedule and alerts off")
	}
	if c := readConfig(t, e); c.Schedule.Enabled || c.Alerts.Enabled || c.Schedule.Every != "daily" || c.Alerts.MinFreeBytes != 20_000_000_000 {
		t.Errorf("config after off: %+v", c)
	}

	out, _, code = sapu(t, e, "", "schedule", "status", "--json")
	if code != 0 {
		t.Fatalf("schedule status: exit %d", code)
	}
	validate(t, "schedule", out)
	var d scheduleData
	jsonData(t, out, &d)
	if d.Enabled || d.Agent.Installed || d.Agent.Loaded || d.NextClean != nil {
		t.Errorf("status after off: %+v", d)
	}
}

// Running on again (as the status warning says to after an upgrade) keeps
// the settings the user chose.
func TestOnAgainKeepsSettings(t *testing.T) {
	e := env.New(t.TempDir())
	fakeAgent(t, e)
	for _, args := range [][]string{
		{"schedule", "on", "--every", "daily"},
		{"alerts", "on", "--min-free", "3GB", "--drop", "0"},
		{"schedule", "on"},
		{"alerts", "on"},
	} {
		if out, errOut, code := sapu(t, e, "", args...); code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", args, code, out, errOut)
		}
	}
	c := readConfig(t, e)
	if c.Schedule.Every != "daily" || c.Alerts.MinFreeBytes != 3_000_000_000 || c.Alerts.DropBytesPerDay != 0 {
		t.Errorf("config: %+v", c)
	}
}

func TestScheduleAndAlertsUsageErrors(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	for _, args := range [][]string{
		{"schedule", "on", "--every", "hourly"},
		{"schedule", "on", "now"},
		{"schedule", "off", "x"},
		{"schedule", "status", "x"},
		{"alerts", "on", "--min-free", "lots"},
		{"alerts", "on", "--drop", "-1GB"},
		{"alerts", "on", "--min-free", "0", "--drop", "0"},
		{"agent", "run", "x"},
	} {
		out, _, code := sapu(t, e, "", append(args, "--json")...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if !strings.Contains(out, `"code": "usage"`) {
			t.Errorf("%v: %s", args, out)
		}
	}
	if exists(agent.ConfigPath(e)) || exists(agent.PlistPath(e)) || len(f.changes()) != 0 {
		t.Errorf("a usage error changed something: launchctl %q", f.calls)
	}
}

// Status only reads: it creates nothing, and asks launchctl print only.
func TestStatusOnlyReads(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	before := snapshot(t, e)
	for _, args := range [][]string{{"schedule", "status"}, {"alerts", "status"}, {"alerts", "status", "--json"}} {
		out, _, code := sapu(t, e, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d", args, code)
		}
		if args[len(args)-1] == "--json" {
			validate(t, "alerts", out)
			continue
		}
		for _, want := range []string{"Scheduled cleanup  off (turn on: sapu schedule on)", "Alerts             off (turn on: sapu alerts on)", "Agent              not installed"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v lacks %q:\n%s", args, want, out)
			}
		}
	}
	sameSnapshot(t, before, snapshot(t, e))
	if exists(e.StateDir) || len(f.changes()) != 0 {
		t.Errorf("status changed something: launchctl %q", f.calls)
	}
}

func TestStatusWarnsWhenTheAgentsSapuIsGone(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	if _, _, code := sapu(t, e, "", "schedule", "on"); code != 0 {
		t.Fatal("schedule on failed")
	}
	// Homebrew upgraded sapu: the old binary is gone.
	old := filepath.Join(e.Home, "Cellar", "sapu", "0.9", "bin", "sapu")
	if err := os.WriteFile(agent.PlistPath(e), []byte(agent.Plist(e, old)), 0o600); err != nil {
		t.Fatal(err)
	}

	out, _, code := sapu(t, e, "", "schedule", "status")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, want := range []string{"Warning: The agent runs " + old + ", which no longer exists", "Run sapu schedule on again"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = sapu(t, e, "", "schedule", "status", "--json")
	validate(t, "schedule", out)
	var d scheduleData
	jsonData(t, out, &d)
	if !d.Agent.ProgramMissing || d.Agent.Program != old || len(d.Agent.Warnings) != 1 {
		t.Errorf("agent: %+v", d.Agent)
	}

	// Running on again points the agent at this sapu.
	if _, _, code := sapu(t, e, "", "schedule", "on"); code != 0 {
		t.Fatal("schedule on failed")
	}
	out, _, _ = sapu(t, e, "", "schedule", "status")
	if strings.Contains(out, "Warning") || !strings.Contains(out, f.program) {
		t.Errorf("after schedule on:\n%s", out)
	}
}

// launchd runs sapu agent run: safe items go to quarantine, the rest stays,
// and status shows the run to undo.
func TestAgentRunCleansSafeItemsAndStatusShowsIt(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	for rel, data := range map[string]string{
		"Library/Logs/Homebrew/x/build.log":   "homebrew log", // safe
		"Library/Caches/node-gyp/headers.tar": "node headers", // safe
		".npm/_cacache/content-v2/blob":       "npm blob",     // moderate
		"Documents/important.txt":             "do not lose me",
	} {
		writeFile(t, filepath.Join(e.Home, rel), data)
	}
	for _, args := range [][]string{{"schedule", "on"}, {"alerts", "on", "--min-free", "200GB"}} {
		if _, _, code := sapu(t, e, "", args...); code != 0 {
			t.Fatalf("%v failed", args)
		}
	}
	f.calls = nil

	out, errOut, code := sapu(t, e, "", "agent", "run")
	if code != 0 || out != "" {
		t.Fatalf("agent run: exit %d\n%s%s", code, out, errOut)
	}
	for _, rel := range []string{"Library/Logs/Homebrew/x", "Library/Caches/node-gyp"} {
		if exists(filepath.Join(e.Home, rel)) {
			t.Errorf("safe item %s not moved", rel)
		}
	}
	for _, rel := range []string{".npm/_cacache/content-v2/blob", "Documents/important.txt"} {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s was touched", rel)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("agent run called launchctl: %q", f.calls)
	}

	runs, err := runlog.List(e)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	id := runs[0].ID
	if len(f.scripts) != 2 || !strings.Contains(f.scripts[0], "below your 200.0 GB alert") ||
		!strings.Contains(f.scripts[1], "of caches and logs to quarantine. Undo: sapu undo "+id) {
		t.Errorf("notifications: %q", f.scripts)
	}

	out, _, _ = sapu(t, e, "", "schedule", "status")
	for _, want := range []string{"Last clean", "moved", "run " + id, "undo: sapu undo " + id, "  Next clean       after", "100.0 GB free"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	out, _, _ = sapu(t, e, "", "schedule", "status", "--json")
	validate(t, "schedule", out)
	var sd scheduleData
	jsonData(t, out, &sd)
	if sd.LastClean == nil || sd.LastClean.RunID != id || sd.LastClean.Result != "moved" || sd.LastCheck == nil || sd.NextClean == nil {
		t.Errorf("schedule status: %+v", sd)
	}
	out, _, _ = sapu(t, e, "", "alerts", "status", "--json")
	validate(t, "alerts", out)
	var ad alertsData
	jsonData(t, out, &ad)
	if ad.FreeBytes == nil || *ad.FreeBytes != f.free || ad.Notified["low"].IsZero() {
		t.Errorf("alerts status: %+v", ad)
	}

	log, err := os.ReadFile(agent.LogPath(e))
	if err != nil || !strings.Contains(string(log), "alerts: ") || !strings.Contains(string(log), "clean: moved 2 items") {
		t.Errorf("agent.log: %s %v", log, err)
	}
}

func TestAgentRunWithLockHeldDoesNothing(t *testing.T) {
	e := env.New(t.TempDir())
	f := fakeAgent(t, e)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/node-gyp/headers.tar"), "node headers")
	if _, _, code := sapu(t, e, "", "schedule", "on"); code != 0 {
		t.Fatal("schedule on failed")
	}
	lock, err := os.OpenFile(filepath.Join(e.StateDir, "lock"), os.O_RDONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "agent", "run")
	if code != 0 || out != "" || errOut != "" {
		t.Errorf("agent run: exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	if exists(agent.LogPath(e)) || exists(agent.StatePath(e)) || len(f.scripts) != 0 {
		t.Error("agent run wrote something while another sapu held the lock")
	}
}
