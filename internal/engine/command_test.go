package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// script writes an executable shell script into a temp folder and returns
// its path. Every command these tests run is such a script.
func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-tool")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// command is an official-command item for ~/rel that runs args.
func command(t *testing.T, e env.Env, rel string, risk rules.Risk, args ...string) plan.Item {
	t.Helper()
	it := item(t, e, rel, risk)
	it.Source = "test.command"
	it.Action = rules.ActionOfficialCommand
	it.Command = args
	return it
}

func withCommands() Options {
	opts := DefaultOptions()
	opts.RunCommands = true
	return opts
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func eventsOf(t *testing.T, e env.Env, runID string, types ...string) []runlog.Event {
	t.Helper()
	evs, err := runlog.Events(e, runID)
	if err != nil {
		t.Fatal(err)
	}
	var out []runlog.Event
	for _, ev := range evs {
		if slices.Contains(types, ev.Type) {
			out = append(out, ev)
		}
	}
	return out
}

// With RunCommands, a selected command runs after the moves; an unselected
// one never runs.
func TestRunCommandsRunsSelectedAfterMoves(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/junk/a.db": "junk",
		"Library/Caches/tool/":     "",
		"Library/Caches/other/":    "",
	})
	junk := homePath(e, "Library/Caches/junk")
	ran := filepath.Join(t.TempDir(), "ran")
	notRan := filepath.Join(t.TempDir(), "not-ran")
	tool := script(t, `if [ -e "$2" ]; then echo junk-still-there > "$1"; else echo after-moves > "$1"; fi; echo cleaned 42 MB`)
	p := makePlan(t, e,
		item(t, e, "Library/Caches/junk", rules.RiskSafe),
		command(t, e, "Library/Caches/tool", rules.RiskSafe, tool, ran, junk),
		command(t, e, "Library/Caches/other", rules.RiskRisky, tool, notRan, junk),
	)

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := readFile(t, ran); got != "after-moves\n" {
		t.Errorf("command saw %q, want it to run after the moves", got)
	}
	requireMissing(t, notRan)
	if len(res.Commands) != 1 {
		t.Fatalf("commands %+v, want one", res.Commands)
	}
	cr := res.Commands[0]
	if cr.ExitCode != 0 || cr.Error != "" || !strings.Contains(cr.Output, "cleaned 42 MB") || cr.Item.Path != homePath(e, "Library/Caches/tool") {
		t.Errorf("command result %+v", cr)
	}
	if len(res.Suggested) != 0 || len(res.Failed) != 0 || len(res.Moved) != 1 {
		t.Errorf("suggested %v, failed %+v, moved %v", paths(res.Suggested), res.Failed, paths(res.Moved))
	}

	evs := eventsOf(t, e, res.RunID, runlog.EventCommandBegin, runlog.EventCommand)
	if len(evs) != 2 || evs[0].Type != runlog.EventCommandBegin || evs[1].Type != runlog.EventCommand {
		t.Fatalf("journal command events %+v", evs)
	}
	for _, ev := range evs {
		if ev.Item != cr.Item.ID || !slices.Equal(ev.Command, cr.Item.Command) {
			t.Errorf("event %+v does not name the item and its command", ev)
		}
	}
	if evs[0].ExitCode != nil || evs[1].ExitCode == nil || *evs[1].ExitCode != 0 {
		t.Errorf("exit codes in the journal: begin %v, end %v", evs[0].ExitCode, evs[1].ExitCode)
	}
	if got := runStatus(t, e, res.RunID); got != runlog.StatusDone {
		t.Errorf("run status %q, want %q", got, runlog.StatusDone)
	}
}

// Without RunCommands nothing runs and no run is made just for commands.
func TestWithoutRunCommandsNothingRuns(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	marker := filepath.Join(t.TempDir(), "ran")
	p := makePlan(t, e, command(t, e, "Library/Caches/tool", rules.RiskSafe, script(t, `touch "$1"`), marker))

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireMissing(t, marker)
	if res.RunID != "" || len(res.Commands) != 0 {
		t.Errorf("run %q, commands %+v; want neither", res.RunID, res.Commands)
	}
	if got := paths(res.Suggested); !slices.Equal(got, []string{homePath(e, "Library/Caches/tool")}) {
		t.Errorf("suggested %v", got)
	}
	requireMissing(t, e.StateDir)
}

// A command with only commands to run still gets a run and a journal.
func TestRunCommandsOnlyMakesARun(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	marker := filepath.Join(t.TempDir(), "ran")
	p := makePlan(t, e, command(t, e, "Library/Caches/tool", rules.RiskSafe, script(t, `touch "$1"`), marker))

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireExists(t, marker)
	if res.RunID == "" || len(eventsOf(t, e, res.RunID, runlog.EventCommand)) != 1 {
		t.Errorf("run %q has no command event", res.RunID)
	}
}

// Refused programs are never started, whatever name or path leads to them.
func TestRefusedProgramsNeverRun(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	// A harmless-looking name that is really a shell.
	disguised := filepath.Join(dir, "cleaner")
	if err := os.Symlink("/bin/sh", disguised); err != nil {
		t.Fatal(err)
	}
	relative := filepath.Join("subdir", "tool")
	cases := [][]string{
		{"sudo", "/usr/bin/touch", marker},
		{"/usr/bin/su", "-c", "touch " + marker},
		{"rm", "-f", marker},
		{"mv", marker, marker + ".2"},
		{"osascript", "-e", "do shell script \"touch " + marker + "\""},
		{"sh", "-c", "touch " + marker},
		{"/bin/BASH", "-c", "touch " + marker},
		{"zsh", "-c", "touch " + marker},
		{"env", "touch", marker},
		{disguised, "-c", "touch " + marker},
		{relative, marker},
		{},
	}
	var items []plan.Item
	for _, args := range cases {
		items = append(items, command(t, e, "Library/Caches/tool", rules.RiskSafe, args...))
	}
	p := makePlan(t, e, items...)
	if len(p.Items) != len(cases) {
		t.Fatalf("plan has %d items, want %d", len(p.Items), len(cases))
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireMissing(t, marker)
	if len(res.Commands) != 0 {
		t.Errorf("started %+v", res.Commands)
	}
	if len(res.Failed) != len(cases) {
		t.Fatalf("failed %+v, want all %d refused", res.Failed, len(cases))
	}
	for _, f := range res.Failed {
		if !strings.HasPrefix(f.Error, "refused: ") {
			t.Errorf("%v: %q, want a refusal", f.Item.Command, f.Error)
		}
	}
	if n := len(eventsOf(t, e, res.RunID, runlog.EventCommand)); n != len(cases) {
		t.Errorf("%d refusals journaled, want %d", n, len(cases))
	}
	if n := len(eventsOf(t, e, res.RunID, runlog.EventCommandBegin)); n != 0 {
		t.Errorf("%d command_begin events for refused programs", n)
	}
}

// A program that is not installed is skipped as gone, with a reason.
func TestMissingProgramIsSkipped(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	p := makePlan(t, e,
		command(t, e, "Library/Caches/tool", rules.RiskSafe, "sapu-no-such-tool-x9", "cache", "clean"),
		command(t, e, "Library/Caches/tool", rules.RiskSafe, filepath.Join(t.TempDir(), "missing"), "clean"),
	)

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Skipped) != 2 || len(res.Commands) != 0 || len(res.Failed) != 0 {
		t.Fatalf("skipped %+v, commands %+v, failed %+v", res.Skipped, res.Commands, res.Failed)
	}
	for _, s := range res.Skipped {
		if s.Reason != plan.ReasonGone || !strings.Contains(s.Detail, "not found on PATH") {
			t.Errorf("skip %+v", s)
		}
	}
	if n := len(eventsOf(t, e, res.RunID, runlog.EventSkip)); n != 2 {
		t.Errorf("%d skips journaled, want 2", n)
	}
}

// A failing command: its exit code and output are kept and journaled, and it
// counts as failed. The next command still runs.
func TestCommandExitCodeAndOutput(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/a/": "", "Library/Caches/b/": ""})
	marker := filepath.Join(t.TempDir(), "second")
	p := makePlan(t, e,
		command(t, e, "Library/Caches/a", rules.RiskSafe, script(t, `echo to-stdout; echo to-stderr >&2; exit 3`)),
		command(t, e, "Library/Caches/b", rules.RiskSafe, script(t, `touch "$1"`), marker),
	)

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireExists(t, marker)
	if len(res.Commands) != 2 {
		t.Fatalf("commands %+v", res.Commands)
	}
	cr := res.Commands[0]
	if cr.ExitCode != 3 || !cr.Failed() || cr.Error != "" || !strings.Contains(cr.Output, "to-stdout") || !strings.Contains(cr.Output, "to-stderr") || cr.Duration <= 0 {
		t.Errorf("result %+v", cr)
	}
	if len(res.Failed) != 1 || res.Failed[0].Error != "exit code 3" || res.Failed[0].Item.ID != cr.Item.ID {
		t.Errorf("failed %+v", res.Failed)
	}
	evs := eventsOf(t, e, res.RunID, runlog.EventCommand)
	if len(evs) != 2 || evs[0].ExitCode == nil || *evs[0].ExitCode != 3 {
		t.Errorf("journal %+v", evs)
	}
}

// Every gatekeeper runs again before a command, as before a move.
func TestCommandIsCheckedAgainBeforeRunning(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	marker := filepath.Join(t.TempDir(), "ran")
	p := makePlan(t, e, command(t, e, "Library/Caches/tool", rules.RiskSafe, script(t, `touch "$1"`), marker))
	if err := protect.AddWhitelist(e, "~/Library/Caches/tool"); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	requireMissing(t, marker)
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonWhitelisted {
		t.Errorf("skipped %+v, want the whitelisted command", res.Skipped)
	}
}

// The command runs in the plan's home, with HOME pointing there and no input.
func TestCommandEnvironment(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/tool/": ""})
	out := filepath.Join(t.TempDir(), "env")
	tool := script(t, `{ printenv HOME; pwd -P; cat; echo end; } > "$1"`)
	p := makePlan(t, e, command(t, e, "Library/Caches/tool", rules.RiskSafe, tool, out))

	if _, err := Apply(t.Context(), e, p, withCommands()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	real, err := filepath.EvalSymlinks(e.Home)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := readFile(t, out), e.Home+"\n"+real+"\nend\n"; got != want {
		t.Errorf("command saw %q, want %q", got, want)
	}
}

// Cancelling stops a long command and its children, and nothing after it
// starts.
func TestCancelStopsALongCommand(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/a/": "", "Library/Caches/b/": ""})
	marker := filepath.Join(t.TempDir(), "second")
	p := makePlan(t, e,
		command(t, e, "Library/Caches/a", rules.RiskSafe, script(t, `sleep 60; echo done`)),
		command(t, e, "Library/Caches/b", rules.RiskSafe, script(t, `touch "$1"`), marker),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts := withCommands()
	opts.Command = func(plan.Item) { time.AfterFunc(200*time.Millisecond, cancel) }

	start := time.Now()
	res, err := Apply(ctx, e, p, opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply: %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Errorf("Apply took %s after cancel", d)
	}
	requireMissing(t, marker)
	if res == nil || len(res.Commands) != 1 || !strings.Contains(res.Commands[0].Error, "cancelled") || res.Commands[0].ExitCode != -1 {
		t.Fatalf("result %+v", res)
	}
	if strings.Contains(res.Commands[0].Output, "done") {
		t.Error("the command finished instead of being stopped")
	}
	if got := runStatus(t, e, res.RunID); got != runlog.StatusDone {
		t.Errorf("run status %q: a cancelled run is still closed", got)
	}
}

// A command that runs past the limit is stopped and counts as failed.
// Not parallel: it sets commandTimeout.
func TestCommandTimeout(t *testing.T) {
	saved := commandTimeout
	commandTimeout = 300 * time.Millisecond
	t.Cleanup(func() { commandTimeout = saved })
	e := newHome(t, map[string]string{"Library/Caches/a/": ""})
	p := makePlan(t, e, command(t, e, "Library/Caches/a", rules.RiskSafe, script(t, `sleep 60`)))

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Commands) != 1 || !strings.Contains(res.Commands[0].Error, "still running after") || len(res.Failed) != 1 {
		t.Errorf("result %+v", res)
	}
}

// --permanent still empties the quarantine when only a command failed.
func TestPermanentWithAFailedCommand(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/junk/a.db": "junk", "Library/Caches/tool/": ""})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/junk", rules.RiskSafe),
		command(t, e, "Library/Caches/tool", rules.RiskSafe, script(t, `exit 1`)),
	)
	opts := withCommands()
	opts.Permanent = true

	res, err := Apply(t.Context(), e, p, opts)

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Failed) != 1 || res.Freed == 0 {
		t.Errorf("failed %+v, freed %d; want the quarantine emptied anyway", res.Failed, res.Freed)
	}
	if got := runStatus(t, e, res.RunID); got != runlog.StatusEmptied {
		t.Errorf("run status %q, want %q", got, runlog.StatusEmptied)
	}
}

func TestOutputKeepsTheTail(t *testing.T) {
	t.Parallel()
	var tl tail
	for i := 0; i < 5000; i++ {
		if _, err := tl.Write([]byte("line of output number ...\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tl.Write([]byte("the last line\n")); err != nil {
		t.Fatal(err)
	}
	got := tl.String()
	if !strings.HasPrefix(got, "[earlier output cut]\n") || !strings.HasSuffix(got, "the last line\n") || len(got) > maxOutput+50 {
		t.Errorf("kept %d bytes: %q...", len(got), got[:60])
	}
	var short tail
	if _, err := short.Write([]byte("short")); err != nil {
		t.Fatal(err)
	}
	if short.String() != "short" {
		t.Errorf("short output changed: %q", short.String())
	}
}
