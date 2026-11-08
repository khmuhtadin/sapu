package menu

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/mascot"
)

// fake is a Backend that records what the menu asks for.
type fake struct {
	restored []string
	runs     []Held
}

func (f *fake) backend() Backend {
	text := func(s string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return s, nil }
	}
	return Backend{
		Review:   func(context.Context) error { return nil },
		Analyze:  func(context.Context) error { return nil },
		Explain:  text("What fills \"System Data\"\n\nXcode   3.4 GB"),
		History:  text("Run  Date  Kind\n20261108-1500-demo  2026-11-08 15:00  clean"),
		Doctor:   text("✓ Full Disk Access\n    on"),
		Settings: text("The whitelist is empty."),
		Quarantine: func() (Quarantine, error) {
			return Quarantine{Runs: f.runs, Retention: "Quarantined files still use disk space. Runs can expire after 7 days, or earlier when quarantine exceeds 20 GB."}, nil
		},
		Restore: func(id string) (string, error) {
			f.restored = append(f.restored, id)
			return "Restored 2 items from run " + id + ".", nil
		},
	}
}

func newFake() *fake {
	at := time.Date(2026, 11, 8, 15, 0, 0, 0, time.Local)
	return &fake{runs: []Held{
		{ID: "20261108-1500-demo", Started: at, Bytes: 1_400_000_000, ExpiresAfter: at.Add(7 * 24 * time.Hour)},
		{ID: "20261107-0900-demo", Started: at.Add(-30 * time.Hour), Bytes: 20_000_000, ExpiresAfter: at.Add(5 * 24 * time.Hour)},
	}}
}

func newTestModel(f *fake, w, h int, color bool) *model {
	m := newModel(context.Background(), f.backend(), color)
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func key(k string) tea.KeyMsg {
	switch k {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "space":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// press sends the keys, answers every command they start (a screen that
// loads), and reports whether one of them quit the program.
func press(m *model, keys ...string) (quit bool) {
	for _, k := range keys {
		_, cmd := m.Update(key(k))
		for cmd != nil {
			msg := cmd()
			if _, ok := msg.(tea.QuitMsg); ok {
				quit = true
				break
			}
			_, cmd = m.Update(msg)
		}
	}
	return quit
}

func TestMenuMatchesTheDesign(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	view := m.View()
	for _, want := range []string{
		"|  sapu ", "CLI / OPEN SOURCE", "A careful Mac cleanup.", "MENU",
		"> 1  Scan & review", "  2  Explore storage", "  3  Apps & build files", "  4  Quarantine",
		"  5  History", "  6  System check", "  7  Settings", "  q  Quit",
		"Review before moving files.", "up/down move   enter open   ? help   q quit",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("menu lacks %q:\n%s", want, view)
		}
	}
	for _, l := range mascot.Lines() {
		if !strings.Contains(view, l) {
			t.Errorf("menu lacks the mascot row %q:\n%s", l, view)
		}
	}
	rows := strings.Split(view, "\n")
	if rows[0] != "+"+strings.Repeat("-", 78)+"+" || rows[len(rows)-1] != rows[0] {
		t.Errorf("the box has no ASCII border:\n%s", view)
	}
	t.Logf("menu at 80 columns:\n%s", view)
}

func TestMenuRendersFitTheirWidth(t *testing.T) {
	for _, w := range []int{40, 48, 64, 80, 120} {
		for _, h := range []int{14, 24, 40} {
			t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
				m := newTestModel(newFake(), w, h, false)
				check := func(what string) {
					t.Helper()
					for _, l := range strings.Split(m.View(), "\n") {
						if n := width(l); n > w {
							t.Errorf("%s: a line is %d wide, want at most %d: %q", what, n, w, l)
						}
					}
				}
				check("menu")
				press(m, "?")
				check("help")
				press(m, "x", "2")
				check("explore")
				press(m, "2")
				check("system data")
				press(m, "esc", "3")
				check("apps")
				press(m, "q", "4")
				check("quarantine")
				press(m, "enter")
				check("restore confirmation")
				press(m, "n", "esc", "5")
				check("history")
				press(m, "esc", "6")
				check("system check")
				press(m, "esc", "7")
				check("settings")
			})
		}
	}
	// The mascot needs room: 48 columns get the menu alone.
	narrow := newTestModel(newFake(), 48, 24, false).View()
	if strings.Contains(narrow, "+##########") {
		t.Errorf("the mascot does not fit 48 columns:\n%s", narrow)
	}
	if !strings.Contains(narrow, "> 1  Scan & review") {
		t.Errorf("the narrow menu lost its choices:\n%s", narrow)
	}
	t.Logf("menu at 48 columns:\n%s", narrow)
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestColorOnlyAddsStyling(t *testing.T) {
	f := newFake()
	plain := newTestModel(f, 80, 24, false).View()
	if strings.Contains(plain, "\x1b") {
		t.Errorf("no color must mean no escape codes:\n%q", plain)
	}
	colored := newTestModel(f, 80, 24, true).View()
	if !strings.Contains(colored, "\x1b[38;2;189;238;99m=") {
		t.Error("the lime patch is not lime")
	}
	if got := ansi.ReplaceAllString(colored, ""); got != plain {
		t.Errorf("color changed the characters:\n%s\nvs\n%s", got, plain)
	}
}

func TestNavigation(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "up", "k")
	if m.cursor != 0 {
		t.Errorf("up at the top moved the cursor to %d", m.cursor)
	}
	press(m, "down", "j", "down")
	if m.cursor != 3 || !strings.Contains(m.View(), "> 4  Quarantine") {
		t.Errorf("down x3: cursor %d\n%s", m.cursor, m.View())
	}
	for range 10 {
		press(m, "down")
	}
	if m.cursor != 6 {
		t.Errorf("down at the bottom: cursor %d", m.cursor)
	}
	press(m, "up")
	if m.cursor != 5 {
		t.Errorf("up: cursor %d", m.cursor)
	}

	press(m, "?")
	if m.screen != screenHelp || !strings.Contains(strings.Join(strings.Fields(m.View()), " "), "do not move or delete files by themselves") {
		t.Errorf("? should show the keys:\n%s", m.View())
	}
	press(m, "x")
	if m.screen != screenMenu {
		t.Error("any key should leave the help")
	}

	if !press(m, "q") {
		t.Error("q on the menu quits")
	}
	if m.launch != launchNone {
		t.Error("q must not open a screen")
	}
	m = newTestModel(newFake(), 80, 24, false)
	if !press(m, "ctrl+c") || m.launch != launchNone {
		t.Error("ctrl-c quits")
	}
}

// Every choice opens its screen, and leaving it returns to the menu.
func TestEveryChoiceOpensAndReturns(t *testing.T) {
	for i, c := range []struct {
		name   string
		launch launch
		screen screen
		want   string
	}{
		{"Scan & review", launchReview, screenMenu, ""},
		{"Explore storage", launchNone, screenExplore, "Folder explorer"},
		{"Apps & build files", launchNone, screenPage, "sapu uninstall APP"},
		{"Quarantine", launchNone, screenQuarantine, "20261108-1500-demo"},
		{"History", launchNone, screenPage, "2026-11-08 15:00"},
		{"System check", launchNone, screenPage, "Full Disk Access"},
		{"Settings", launchNone, screenPage, "sapu whitelist add PATH"},
	} {
		for _, how := range []string{"number", "enter"} {
			t.Run(c.name+" by "+how, func(t *testing.T) {
				f := newFake()
				m := newTestModel(f, 80, 24, false)
				if how == "number" {
					press(m, fmt.Sprint(i+1))
				} else {
					press(m, strings.Repeat("down ", i)[:max(len(strings.Repeat("down ", i))-1, 0)])
					m.cursor = i
					press(m, "enter")
				}
				if m.launch != c.launch || m.screen != c.screen {
					t.Fatalf("launch %d screen %d, want %d and %d", m.launch, m.screen, c.launch, c.screen)
				}
				if c.launch != launchNone {
					return // the CLI runs the screen and opens the menu again
				}
				if !strings.Contains(m.View(), c.want) {
					t.Errorf("screen lacks %q:\n%s", c.want, m.View())
				}
				press(m, "esc")
				if m.screen != screenMenu || m.cursor != i {
					t.Errorf("esc should go back to the menu at choice %d: screen %d, cursor %d", i, m.screen, m.cursor)
				}
				if len(f.restored) != 0 {
					t.Error("opening a screen restored something")
				}
			})
		}
	}
}

func TestExploreOffersTheExplorerAndSystemData(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "2", "2")
	if m.screen != screenPage || !strings.Contains(m.View(), "What fills") || !strings.Contains(m.View(), "Xcode") {
		t.Errorf("System Data should show the explanation:\n%s", m.View())
	}
	press(m, "q")
	press(m, "2", "1")
	if m.launch != launchAnalyze {
		t.Error("Folder explorer should open the analyze screen")
	}
}

func TestAppsListsTheCommands(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	press(m, "3")
	view := m.View()
	for _, want := range []string{
		"sapu uninstall APP", "sapu purge DIR", "sapu dupes", "sapu installer", "sapu large",
		"Remove an app and the files it left behind",
		"asks before it moves anything",
	} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), want) {
			t.Errorf("apps screen lacks %q:\n%s", want, view)
		}
	}
}

func TestSlowAnswersForALeftScreenAreDropped(t *testing.T) {
	m := newTestModel(newFake(), 80, 24, false)
	_, cmd := m.Update(key("5")) // History loads
	press(m, "esc", "6")         // the user moved on
	m.Update(cmd())              // the history arrives late
	if strings.Contains(m.View(), "2026-11-08 15:00") || !strings.Contains(m.View(), "system check") {
		t.Errorf("a late answer replaced another screen:\n%s", m.View())
	}
}

func TestPageError(t *testing.T) {
	f := newFake()
	b := f.backend()
	b.History = func(context.Context) (string, error) { return "", errors.New("history.log is damaged") }
	m := newModel(context.Background(), b, false)
	press(m, "5")
	if !strings.Contains(m.View(), "Could not read this: history.log is damaged") {
		t.Errorf("an error should show:\n%s", m.View())
	}
}

func TestPageScrolls(t *testing.T) {
	f := newFake()
	b := f.backend()
	var long []string
	for i := range 60 {
		long = append(long, fmt.Sprintf("line %02d", i))
	}
	b.History = func(context.Context) (string, error) { return strings.Join(long, "\n"), nil }
	m := newModel(context.Background(), b, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	press(m, "5")
	if v := m.View(); !strings.Contains(v, "line 00") || strings.Contains(v, "line 59") || !strings.Contains(v, "more below") {
		t.Errorf("first page:\n%s", v)
	}
	press(m, "G")
	if v := m.View(); !strings.Contains(v, "line 59") || strings.Contains(v, "line 00") {
		t.Errorf("last page:\n%s", v)
	}
	press(m, "g", "down")
	if v := m.View(); strings.Contains(v, "line 00") || !strings.Contains(v, "line 01") {
		t.Errorf("one line down:\n%s", v)
	}
}

// The quarantine restore is the only change the menu can make, and it takes
// its own confirmation: y.
func TestRestoreNeedsItsOwnConfirmation(t *testing.T) {
	f := newFake()
	m := newTestModel(f, 80, 24, false)
	press(m, "4")
	view := m.View()
	for _, want := range []string{"20261108-1500-demo  1.4 GB", "20261107-0900-demo  20.0 MB", "can expire after", "Runs can expire after 7 days"} {
		if !strings.Contains(strings.Join(strings.Fields(view), " "), strings.Join(strings.Fields(want), " ")) {
			t.Errorf("quarantine screen lacks %q:\n%s", want, view)
		}
	}

	// Moving, and every key but enter, never ask or restore.
	press(m, "down", "up", "space", "r", "y", "Y")
	if len(f.restored) != 0 || m.confirming {
		t.Fatalf("a key other than enter asked or restored: %v", f.restored)
	}

	press(m, "enter")
	if !m.confirming || len(f.restored) != 0 {
		t.Fatal("enter should ask, not restore")
	}
	if v := m.View(); !strings.Contains(v, "Restore run 20261108-1500-demo?") || !strings.Contains(v, "[n] Keep in quarantine") || !strings.Contains(v, "[y] Restore") {
		t.Errorf("confirmation:\n%s", v)
	}
	press(m, "enter", "space", "q", "x", "yes", "down")
	if len(f.restored) != 0 || !m.confirming {
		t.Fatalf("only y may restore: %v", f.restored)
	}
	press(m, "n")
	if m.confirming || len(f.restored) != 0 {
		t.Fatal("n should keep the run in quarantine")
	}
	press(m, "enter", "esc")
	if m.confirming || len(f.restored) != 0 {
		t.Fatal("esc should keep the run in quarantine")
	}

	press(m, "down", "enter", "y")
	if len(f.restored) != 1 || f.restored[0] != "20261107-0900-demo" {
		t.Fatalf("y should restore the run under the cursor once: %v", f.restored)
	}
	if v := m.View(); !strings.Contains(v, "Restored 2 items from run 20261107-0900-demo.") {
		t.Errorf("the result should show:\n%s", v)
	}
}

func TestRestoreErrorShows(t *testing.T) {
	f := newFake()
	b := f.backend()
	b.Restore = func(id string) (string, error) { return "", errors.New("another sapu is working") }
	m := newModel(context.Background(), b, false)
	press(m, "4", "enter", "y")
	if v := m.View(); !strings.Contains(v, "Could not finish: another sapu is working") {
		t.Errorf("a failed restore should say so:\n%s", v)
	}
}

func TestEmptyQuarantine(t *testing.T) {
	f := newFake()
	f.runs = nil
	m := newTestModel(f, 80, 24, false)
	press(m, "4", "enter", "y")
	if m.confirming || len(f.restored) != 0 || !strings.Contains(m.View(), "The quarantine is empty") {
		t.Errorf("nothing to restore:\n%s", m.View())
	}
}

// A real terminal can call View before the first size message, and some
// report 0 columns. Found by running the menu in a pty: slice bounds out of
// range [:-2].
func TestViewAtAnySizeDoesNotPanic(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {1, 1}, {10, 5}, {0, 40}, {23, 30}} {
		for _, s := range []screen{screenMenu, screenPage, screenQuarantine} {
			m := newModel(context.Background(), newFake().backend(), false)
			m.width, m.height = size[0], size[1]
			m.screen = s
			_ = m.View()
		}
	}
}
