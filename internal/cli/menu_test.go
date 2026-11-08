package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/mascot"
	"github.com/khmuhtadin/sapu/internal/menu"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

func TestUseMenu(t *testing.T) {
	jsonOut = false
	t.Cleanup(func() { jsonOut = false })
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }()
	cmd := &cobra.Command{}
	cmd.SetIn(devNull)
	cmd.SetOut(devNull)
	if useMenu(cmd) {
		t.Error("/dev/null is not a terminal")
	}
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	if useMenu(cmd) {
		t.Error("a pipe is not a terminal")
	}

	fakeTerminal(t)
	if !useMenu(cmd) {
		t.Fatal("stdin and stdout on a terminal should open the menu")
	}
	jsonOut = true
	if useMenu(cmd) {
		t.Error("--json should not open the menu")
	}
	jsonOut = false
	t.Setenv("SAPU_NO_TUI", "1")
	if useMenu(cmd) {
		t.Error("SAPU_NO_TUI should not open the menu")
	}
}

// With a terminal, SAPU_NO_TUI and --json still print the summary. (Without
// them the menu would open, and there is no terminal to draw on.)
func TestSummaryOnATerminalWithoutTheMenu(t *testing.T) {
	fakeTerminal(t)
	t.Setenv("NO_COLOR", "1")
	t.Setenv("COLUMNS", "80")
	e := junkHome(t)

	t.Setenv("SAPU_NO_TUI", "1")
	out, _, code := sapu(t, e, "", "")
	if code != 0 || !strings.Contains(out, "FOUND FOR REVIEW") {
		t.Fatalf("SAPU_NO_TUI: exit %d\n%s", code, out)
	}
	noMascot(t, out)

	t.Setenv("SAPU_NO_TUI", "")
	out, _, code = sapu(t, e, "", "--json")
	if code != 0 {
		t.Fatalf("--json: exit %d\n%s", code, out)
	}
	validate(t, "summary", out)
	noMascot(t, out)
}

var ansiCodes = regexp.MustCompile("\x1b\\[[0-9;]*m")

// noMascot fails when out has the mascot or any ANSI escape code.
func noMascot(t *testing.T, out string) {
	t.Helper()
	if ansiCodes.MatchString(out) || strings.Contains(out, "\x1b") {
		t.Errorf("output has ANSI codes:\n%q", out)
	}
	for _, l := range mascot.Lines()[1:] {
		if strings.Contains(out, l) {
			t.Errorf("output has the mascot (%q):\n%s", l, out)
			return
		}
	}
}

// hasMascot reports whether out starts with the whole mascot, then the intro.
func hasMascot(out string) bool {
	return strings.HasPrefix(strings.TrimLeft(out, "\n"), strings.Join(mascot.Lines(), "\n")+"\n")
}

// The mascot appears in the help of an interactive terminal, and nowhere
// else: not in piped help, completion, --json or progress output.
func TestMascotPlacement(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	e := env.New(t.TempDir())

	t.Run("piped help", func(t *testing.T) {
		out, _, code := sapu(t, e, "", "--help")
		if code != 0 || !strings.Contains(out, "Usage:") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		noMascot(t, out)
	})

	t.Run("interactive help", func(t *testing.T) {
		fakeTerminal(t)
		out, _, code := sapu(t, e, "", "--help")
		if code != 0 || !strings.Contains(out, "Usage:") {
			t.Fatalf("exit %d\n%s", code, out)
		}
		if !hasMascot(out) {
			t.Errorf("interactive help should start with the mascot:\n%s", out)
		}
		if !strings.Contains(out, "sapu / CLI / Open source: a careful Mac cleanup.\n") {
			t.Errorf("interactive help lacks the introduction:\n%s", out)
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("NO_COLOR help has ANSI codes:\n%q", out)
		}

		// A subcommand's help has no mascot, and neither has SAPU_NO_TUI.
		out, _, _ = sapu(t, e, "", "clean", "--help")
		noMascot(t, out)
		t.Setenv("SAPU_NO_TUI", "1")
		out, _, _ = sapu(t, e, "", "--help")
		noMascot(t, out)
	})

	t.Run("interactive help with color", func(t *testing.T) {
		fakeTerminal(t)
		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "xterm-256color")
		out, _, _ := sapu(t, e, "", "--help")
		if !strings.Contains(out, "\x1b[38;2;189;238;99m=") {
			t.Errorf("the lime patch is not lime:\n%q", out)
		}
		if plain := ansiCodes.ReplaceAllString(out, ""); !hasMascot(plain) {
			t.Errorf("color changed the characters:\n%s", plain)
		}
	})

	t.Run("completion", func(t *testing.T) {
		fakeTerminal(t)
		for _, shell := range []string{"bash", "zsh", "fish"} {
			out, _, code := sapu(t, e, "", "completion", shell)
			if code != 0 || out == "" {
				t.Fatalf("%s: exit %d", shell, code)
			}
			noMascot(t, out)
		}
	})

	t.Run("json", func(t *testing.T) {
		fakeTerminal(t)
		for _, args := range [][]string{{"version", "--json"}, {"--json"}, {"doctor", "--json"}, {"history", "--json"}, {"--json", "--help"}} {
			out, _, _ := sapu(t, junkHome(t), "", args...)
			noMascot(t, out)
			if args[len(args)-1] == "--help" {
				continue
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(out), &doc); err != nil {
				t.Errorf("%v: stdout is not one JSON document: %v\n%s", args, err, out)
			}
		}
	})

	t.Run("progress", func(t *testing.T) {
		fakeTerminal(t)
		file := filepath.Join(t.TempDir(), "plan.json")
		out, errOut, code := sapu(t, newHome(t), "", "clean", "--plan", file, "--progress", "ndjson")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, errOut)
		}
		noMascot(t, out)
		noMascot(t, errOut)
		for _, line := range strings.Split(strings.TrimSpace(errOut), "\n") {
			if line == "" {
				continue
			}
			var ev map[string]any
			if err := json.Unmarshal([]byte(line), &ev); err != nil || ev["type"] != "progress" {
				t.Errorf("progress line is not an event: %q", line)
			}
		}
	})
}

// driveMenu sends keys to the menu model, answers the commands they start
// (a screen that loads) and returns what the user would see.
func driveMenu(t *testing.T, m tea.Model, keys ...string) (view string, quit bool) {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		var cmd tea.Cmd
		m, cmd = m.Update(msg)
		for cmd != nil {
			out := cmd()
			if _, ok := out.(tea.QuitMsg); ok {
				return m.View(), true
			}
			m, cmd = m.Update(out)
		}
	}
	return m.View(), false
}

// Every menu choice, on a temp home that holds junk and a quarantined run,
// shows its screen and changes nothing.
func TestMenuChoicesChangeNothing(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	e := newHome(t)
	if _, errOut, code := sapu(t, e, "", "clean", "--yes"); code != 0 {
		t.Fatalf("clean: exit %d\n%s", code, errOut)
	}
	runs, err := runlog.List(e)
	if err != nil || len(runs) != 1 {
		t.Fatalf("want one run in quarantine: %v %v", runs, err)
	}
	id := runs[0].ID
	// More junk to be found, so "Scan & review" has something it must not take.
	writeFile(t, filepath.Join(e.Home, "Library/Logs/Homebrew/y/build.log"), "new junk")
	before := snapshot(t, e)
	oldLoad := loadEnv
	loadEnv = func() (env.Env, error) { return e, nil }
	t.Cleanup(func() { loadEnv = oldLoad })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	backend := menuBackend(cmd)
	reviewed, analyzed := 0, 0
	backend.Review = func(context.Context) error { reviewed++; return nil }
	backend.Analyze = func(context.Context) error { analyzed++; return nil }
	restoreCalls := 0
	realRestore := backend.Restore
	backend.Restore = func(id string) (string, error) { restoreCalls++; return realRestore(id) }

	for choice, want := range map[string]string{
		"1": "",                        // opens the review screen: the CLI runs it
		"2": "Folder explorer",         // explore
		"3": "Build folders",           // tools
		"4": id,                        // quarantine lists the run
		"5": id,                        // history shows it
		"6": "Full Disk Access",        // doctor
		"7": "sapu whitelist add PATH", // settings
	} {
		m := menu.New(context.Background(), backend, false)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
		view, quit := driveMenu(t, m, choice)
		if choice == "1" {
			if !quit {
				t.Error("choice 1 should leave the menu for the review screen")
			}
			continue
		}
		if quit || !strings.Contains(view, want) {
			t.Errorf("choice %s: quit=%v, view lacks %q:\n%s", choice, quit, want, view)
		}
		if view, quit = driveMenu(t, m, "esc"); quit || !strings.Contains(view, "> ") || !strings.Contains(view, "MENU") {
			t.Errorf("choice %s: esc should return to the menu:\n%s", choice, view)
		}
	}
	// System Data and the explorer.
	m := menu.New(context.Background(), backend, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	view, _ := driveMenu(t, m, "2", "2")
	if !strings.Contains(view, `What fills "System Data"`) {
		t.Errorf("System Data lacks the explanation:\n%s", view)
	}
	driveMenu(t, m, "esc")
	if _, quit := driveMenu(t, m, "2", "1"); !quit {
		t.Error("the folder explorer should leave the menu for the analyze screen")
	}
	if reviewed != 0 || analyzed != 0 || restoreCalls != 0 {
		t.Errorf("the menu model ran a screen or a restore by itself: %d %d %d", reviewed, analyzed, restoreCalls)
	}

	sameSnapshot(t, before, snapshot(t, e))
	if after, err := runlog.List(e); err != nil || len(after) != 1 || after[0].ID != id || after[0].Status != runs[0].Status {
		t.Errorf("history changed: %v %v", after, err)
	}

	// Browsing the quarantine and answering n or esc to the confirmation
	// restores nothing.
	m = menu.New(context.Background(), backend, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	driveMenu(t, m, "4", "enter", "n", "enter", "esc", "enter", "x", " ")
	if restoreCalls != 0 {
		t.Fatalf("restore ran without y: %d", restoreCalls)
	}
	sameSnapshot(t, before, snapshot(t, e))
	if exists(filepath.Join(e.Home, junkItems[1])) {
		t.Fatal("a quarantined item came back without y")
	}

	// y restores, the way `sapu undo RUN_ID` does.
	view, _ = driveMenu(t, m, "y")
	if restoreCalls != 1 || !strings.Contains(view, "Restored 4 items from run "+id) {
		t.Errorf("y should restore the run once: calls %d\n%s", restoreCalls, view)
	}
	moved := 0
	for _, rel := range junkItems {
		if exists(filepath.Join(e.Home, rel)) {
			moved++
		}
	}
	if moved != len(junkItems) {
		t.Errorf("restore put back %d of %d items", moved, len(junkItems))
	}
	assertDecoys(t, e)
}
