package review

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

func TestListMatchesTheDesign(t *testing.T) {
	m, _ := listed(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 34})
	view := m.View()
	lines := strings.Split(view, "\n")
	if lines[0] != "sapu / review                                             13 found / 4 skipped" {
		t.Errorf("header = %q", lines[0])
	}
	if lines[1] != strings.Repeat("-", 76) {
		t.Errorf("rule = %q", lines[1])
	}
	for _, want := range []string{
		"  Review what Sapu found. Nothing has moved.",
		"  Focus a row to read its location, reason and risk.",
		"> [v] SAFE / Safe to recreate ",
		"  [v] MODERATE / Needs review ",
		"  [>] RISKY / May contain personal data ",
		"      Not selected. Read the explanation before choosing an item.",
		"      SKIPPED / Protected and in use ",
		"    [x] chrome cache ",
		"  up/down move   space select   left close   / filter   enter review",
		"  a select safe   ? all keys   q cancel",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("list lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "\x1b") {
		t.Errorf("no color must mean no escape codes:\n%q", view)
	}

	// The focused item shows its full path, its reason and whether it comes back.
	goTo(t, m, rowItem, "xcode derived data")
	view = m.View()
	for _, want := range []string{
		"  > [x] xcode derived data ",
		"        ~/Library/Developer/Xcode/DerivedData/Sapu-gxbnqtzjvmbdkd",
		"        Xcode build output and indexes, one folder per project.",
		"        Regenerates: yes / Risk: MODERATE / Rule: xcode.derived-data",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("focused row lacks %q:\n%s", want, view)
		}
	}
	t.Logf("review at 80 columns:\n%s", m.View())
}

func TestConfirmMatchesTheDesign(t *testing.T) {
	m, f := listed(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	goTo(t, m, rowGroup, "RISKY")
	press(m, "right")
	goTo(t, m, rowItem, "telegram media")
	press(m, "space", "space")
	press(m, "enter")
	view := m.View()
	flatView := flat(view)
	for _, want := range []string{
		"| Move 12 items to quarantine?",
		"SAFE 7 / MODERATE 4 / RISKY 1",
		"1 RISKY item may contain personal data.",
		"| Sapu rechecks these exact items before moving them.",
		"| Protected, changed and open files are skipped.",
		"| Quarantined files still use disk space. Runs can expire after 7 days, or",
		"| earlier when quarantine exceeds 20.0 GB. Restore them before expiry.",
		"[n] Keep reviewing",
		"[y] Move to quarantine",
	} {
		if !strings.Contains(view, want) && !strings.Contains(flatView, want) {
			t.Errorf("confirm lacks %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "[n]") > strings.Index(view, "[y]") {
		t.Error("n, the safe default, comes first")
	}
	for _, bad := range []string{"Sapu for Mac", "Buy", "buy", "price", "upgrade", "Upgrade", "paid", "Pro "} {
		if strings.Contains(view, bad) {
			t.Errorf("no paid-product message belongs here: found %q", bad)
		}
	}
	// The box is ASCII and nothing in it is wider than the screen.
	rows := strings.Split(view, "\n")
	if !strings.HasPrefix(rows[1], "  +---") || !strings.HasSuffix(rows[1], "---+") {
		t.Errorf("the box has no ASCII border:\n%s", view)
	}
	// Enter, space and every other key keep the screen still. Only y moves.
	press(m, "enter", "space", "a", "q", "x", "Y ")
	if m.screen != screenConfirm || f.count() != 0 {
		t.Fatalf("a key other than y or n changed the screen: screen %d, moves %d", m.screen, f.count())
	}
	t.Logf("confirm at 80 columns:\n%s", view)
}

func TestReceiptMatchesTheDesign(t *testing.T) {
	m, _ := listed(t)
	finish(t, m, press(m, "enter", "y"))
	view := m.View()
	for _, want := range []string{
		"sapu / receipt",
		"  OK  Moved 11 items / 17.9 GB to quarantine.",
		"  Moved       11",
		"  Skipped      0",
		"  Failed       0",
		"  Quarantined files still use space.",
		"  Eligible to expire after: 15 November 2026",
		"  Older runs may expire sooner if quarantine exceeds 20.0 GB.",
		"  Restore this run:   sapu undo 20261108-1115-a3f9",
		"  Inspect held runs:  sapu quarantine list",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("receipt lacks %q:\n%s", want, view)
		}
	}
	// Moving to quarantine does not free space yet, and the receipt never says it did.
	if strings.Contains(strings.ToLower(view), "freed") {
		t.Errorf("a receipt must not claim the space is free:\n%s", view)
	}
	t.Logf("receipt at 80 columns:\n%s", view)
}

// A partial run reads as a neutral review, never as a celebration.
func TestPartialReceiptIsNeutral(t *testing.T) {
	m, f := listed(t)
	f.result = func(_ context.Context, p *plan.Plan, _ engine.Options) (*engine.Result, error) {
		r := movedAll(p)
		r.Moved = r.Moved[2:]
		r.Skipped = []plan.Skip{{Path: p.Items[0].Path, Reason: plan.ReasonChanged, Detail: "changed"}}
		r.Failed = []engine.Failure{{Item: p.Items[1], Error: "access denied"}}
		return r, nil
	}
	finish(t, m, press(m, "enter", "y"))
	view := m.View()
	for _, want := range []string{
		"  REVIEW  9 moved / 1 skipped / 1 failed",
		"  Skipped      1",
		"  Failed       1",
		"  SKIPPED  ~/Library/Caches/Google/Chrome/Default/Cache",
		"           Changed since the plan was made. Left in place.",
		"  FAILED   ~/Library/Caches/com.apple.Safari",
		"           Access denied.",
		"  The 9 items completed are in quarantine.",
		"  Restore this run:   sapu undo 20261108-1115-a3f9",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("partial receipt lacks %q:\n%s", want, view)
		}
	}
	for _, bad := range []string{"OK", "✓", "Moved 11"} {
		if strings.Contains(view, bad) {
			t.Errorf("a partial run must not celebrate: found %q\n%s", bad, view)
		}
	}
	t.Logf("partial receipt at 80 columns:\n%s", view)
}

// With NO_COLOR nothing moves: the scan cue stays three dots.
func TestNoMotionUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(context.Background(), env.New(home), nil, nil, nil)
	if m.motion {
		t.Fatal("NO_COLOR must turn motion off")
	}
	before := m.scanView()[3]
	m.Update(spinner.TickMsg{})
	if after := m.scanView()[3]; after != before || !strings.Contains(after, "Scanning caches...") {
		t.Errorf("the scan line changed: %q then %q", before, after)
	}
}

// Without NO_COLOR the cue moves in place: its width never changes, so the
// line next to it does not shift.
func TestScanCueMovesInPlace(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	m := newModel(context.Background(), env.New(home), nil, nil, nil)
	if !m.motion {
		t.Fatal("motion should be on")
	}
	first := m.cue()
	m.Update(m.spin.Tick())
	if m.cue() == first {
		t.Errorf("the cue did not change from %q", first)
	}
	if len(m.cue()) != len(first) {
		t.Errorf("the cue changed width: %q vs %q", first, m.cue())
	}
}
