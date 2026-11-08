package analyze

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// writeFile makes a file of size bytes, with any missing folders.
func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture builds:
//
//	root/big/a.bin 2 MB, big/b.bin 1 MB, big/deep/c.bin 256 KB
//	root/medium.bin 512 KB
//	root/small.txt 8 KB
//	root/empty/
func fixture(t *testing.T) (e env.Env, root string) {
	t.Helper()
	e = env.New(t.TempDir())
	root = filepath.Join(e.Home, "data")
	writeFile(t, filepath.Join(root, "big", "a.bin"), 2<<20)
	writeFile(t, filepath.Join(root, "big", "b.bin"), 1<<20)
	writeFile(t, filepath.Join(root, "big", "deep", "c.bin"), 256<<10)
	writeFile(t, filepath.Join(root, "medium.bin"), 512<<10)
	writeFile(t, filepath.Join(root, "small.txt"), 8<<10)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	return e, root
}

// browser returns a model that already shows the fixture tree.
func browser(t *testing.T) (*model, string) {
	t.Helper()
	e, root := fixture(t)
	tree, err := walk.Tree(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), e, root)
	m.setTree(tree, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m, root
}

func press(m *model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		case "left":
			msg = tea.KeyMsg{Type: tea.KeyLeft}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
		case "ctrl+c":
			msg = tea.KeyMsg{Type: tea.KeyCtrlC}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

func names(n *walk.Node) []string {
	var out []string
	for _, c := range n.Children {
		out = append(out, c.Name)
	}
	return out
}

func TestSortedLargestFirstAndSizes(t *testing.T) {
	m, _ := browser(t)
	got := strings.Join(names(m.tree), " ")
	if got != "big medium.bin small.txt empty" {
		t.Fatalf("order = %q", got)
	}
	view := m.View()
	for _, want := range []string{"Sapu analyze", "big/", "medium.bin", "empty/", "%"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "big/") > strings.Index(view, "medium.bin") {
		t.Errorf("big/ should be listed before medium.bin:\n%s", view)
	}
}

func TestNavigationStaysInsideRoot(t *testing.T) {
	m, root := browser(t)

	press(m, "backspace", "left", "h")
	if len(m.trail) != 1 || m.current().node.Path != root {
		t.Fatalf("went above root: trail=%d", len(m.trail))
	}

	press(m, "enter") // cursor starts on big/
	if m.current().node.Name != "big" {
		t.Fatalf("enter did not open big, at %q", m.current().node.Name)
	}
	if !strings.Contains(m.View(), "a.bin") {
		t.Errorf("big's children not shown:\n%s", m.View())
	}

	press(m, "down", "down", "enter") // deep/ is third: a.bin, b.bin, deep
	if m.current().node.Name != "deep" {
		t.Fatalf("at %q, want deep", m.current().node.Name)
	}
	press(m, "backspace")
	if m.current().node.Name != "big" {
		t.Fatalf("backspace went to %q, want big", m.current().node.Name)
	}
	press(m, "h", "h", "h", "left")
	if m.current().node.Path != root {
		t.Fatalf("not back at root: %s", m.current().node.Path)
	}

	// enter on a file does nothing
	press(m, "down", "enter")
	if len(m.trail) != 1 {
		t.Fatalf("enter on a file opened it")
	}
}

func TestCursorStaysInRange(t *testing.T) {
	m, _ := browser(t)
	press(m, "up", "k")
	if m.current().cursor != 0 {
		t.Fatalf("cursor = %d above the list", m.current().cursor)
	}
	press(m, "down", "down", "down", "down", "down", "j")
	if got := m.current().cursor; got != 3 {
		t.Fatalf("cursor = %d, want last entry 3", got)
	}
	// an empty folder has no cursor movement problems
	press(m, "enter")
	press(m, "down", "up", "space")
	if len(m.marked) != 0 {
		t.Fatalf("marked something in an empty folder")
	}
	if !strings.Contains(m.View(), "(empty)") {
		t.Errorf("empty folder not announced:\n%s", m.View())
	}
}

func TestMarkingUpdatesFooter(t *testing.T) {
	m, _ := browser(t)
	if !strings.Contains(m.View(), "Nothing marked") {
		t.Fatalf("footer without marks:\n%s", m.View())
	}

	press(m, "space") // big/
	big := m.tree.Children[0].Bytes
	want := "1 marked, " + humanBytes(big)
	if !strings.Contains(m.View(), want) {
		t.Fatalf("footer lacks %q:\n%s", want, m.View())
	}

	press(m, "down", "space") // medium.bin
	want = "2 marked, " + humanBytes(big+m.tree.Children[1].Bytes)
	if !strings.Contains(m.View(), want) {
		t.Fatalf("footer lacks %q:\n%s", want, m.View())
	}
	if !strings.Contains(m.View(), "> *") {
		t.Errorf("cursor row should show a mark:\n%s", m.View())
	}

	press(m, "space") // unmark medium.bin
	want = "1 marked, " + humanBytes(big)
	if !strings.Contains(m.View(), want) {
		t.Fatalf("footer lacks %q after unmark:\n%s", want, m.View())
	}
	press(m, "up", "space")
	if !strings.Contains(m.View(), "Nothing marked") {
		t.Fatalf("footer after unmarking all:\n%s", m.View())
	}
}

func TestMarksNeverNest(t *testing.T) {
	m, root := browser(t)

	// mark a file inside big/, then the folder: the file mark goes away
	press(m, "enter", "space") // big/a.bin
	press(m, "backspace", "space")
	if len(m.marked) != 1 || m.marked[filepath.Join(root, "big")] == nil {
		t.Fatalf("marks = %v, want only big", m.marked)
	}

	// inside a marked folder, marking is refused with a reason
	press(m, "enter", "space")
	if len(m.marked) != 1 {
		t.Fatalf("marked inside a marked folder: %v", m.marked)
	}
	if !strings.Contains(m.View(), "Already covered") {
		t.Errorf("no reason shown:\n%s", m.View())
	}
}

func TestReviewBuildsPlanItems(t *testing.T) {
	m, root := browser(t)
	var gotKind string
	var gotItems []plan.Item
	m.planFn = func(_ context.Context, _ env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
		gotKind, gotItems = kind, items
		out := append([]plan.Item(nil), items...)
		for i := range out {
			out[i].ID = "000" + string(rune('1'+i))
		}
		return &plan.Plan{Kind: kind, Items: out, Skipped: []plan.Skip{
			{Path: filepath.Join(root, "protected-thing"), Source: "analyze", Reason: plan.ReasonProtected},
			{Path: filepath.Join(root, "busy"), Source: "analyze", Reason: plan.ReasonInUse, Detail: "open in Safari"},
		}}, nil
	}

	// nothing marked: x does nothing
	if cmd := press(m, "x"); cmd != nil || m.screen != screenBrowsing {
		t.Fatalf("x with no marks went to screen %v", m.screen)
	}
	if !strings.Contains(m.View(), "Nothing marked. Press space") {
		t.Errorf("no hint:\n%s", m.View())
	}

	press(m, "space", "down", "space") // big/ and medium.bin
	cmd := press(m, "x")
	if m.screen != screenPlanning || cmd == nil {
		t.Fatalf("screen = %v, cmd nil = %v", m.screen, cmd == nil)
	}
	m.Update(cmd())

	if gotKind != plan.KindAnalyze {
		t.Errorf("kind = %q", gotKind)
	}
	if len(gotItems) != 2 {
		t.Fatalf("%d items, want 2", len(gotItems))
	}
	big, medium := m.tree.Children[0], m.tree.Children[1]
	wantBig := plan.Item{
		Source: "analyze", Path: filepath.Join(root, "big"), Bytes: big.Bytes,
		Risk: rules.RiskRisky, Regenerates: false, Explain: "Chosen by you in sapu analyze",
		Action: rules.ActionQuarantine,
	}
	wantMedium := plan.Item{
		Source: "analyze", Path: filepath.Join(root, "medium.bin"), Bytes: medium.Bytes,
		Risk: rules.RiskRisky, Regenerates: false, Explain: "Chosen by you in sapu analyze",
		Action: rules.ActionQuarantine,
	}
	for i, want := range []plan.Item{wantBig, wantMedium} { // path order
		got := gotItems[i]
		wantID, err := plan.IdentityOf(want.Path)
		if err != nil {
			t.Fatal(err)
		}
		want.Identity = wantID
		if got.Source != want.Source || got.Path != want.Path || got.Bytes != want.Bytes ||
			got.Risk != want.Risk || got.Regenerates != want.Regenerates || got.Explain != want.Explain ||
			got.Action != want.Action || got.Identity != want.Identity || got.Selected {
			t.Errorf("item %d = %+v, want %+v", i, got, want)
		}
	}

	if m.screen != screenReviewing {
		t.Fatalf("screen = %v, want review", m.screen)
	}
	view := m.View()
	for _, want := range []string{
		"Will move 2 entries", "data/big", "data/medium.bin", "Skipped 2 entries",
		"protected", "protected-thing", "in use", "open in Safari",
		"These go to quarantine. Put them back with `sapu undo`.", "Press y to confirm, n to go back.",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("review lacks %q:\n%s", want, view)
		}
	}
}

func TestReviewDropsVanishedEntries(t *testing.T) {
	m, root := browser(t)
	m.planFn = func(_ context.Context, _ env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
		return &plan.Plan{Kind: kind, Items: items}, nil
	}
	press(m, "down", "space") // medium.bin
	if err := os.Remove(filepath.Join(root, "medium.bin")); err != nil {
		t.Fatal(err)
	}
	cmd := press(m, "x")
	if cmd != nil || m.screen != screenBrowsing {
		t.Fatalf("planned an entry that is gone: screen %v", m.screen)
	}
	if len(m.marked) != 0 || strings.Contains(strings.Join(names(m.tree), " "), "medium.bin") {
		t.Errorf("gone entry still listed or marked: %v", names(m.tree))
	}
}

func TestPlanErrorGoesBackToBrowsing(t *testing.T) {
	m, _ := browser(t)
	m.planFn = func(context.Context, env.Env, string, []plan.Item) (*plan.Plan, error) {
		return nil, errors.New("boom")
	}
	press(m, "space")
	m.Update(press(m, "x")())
	if m.screen != screenBrowsing || !strings.Contains(m.View(), "Could not build the plan: boom") {
		t.Fatalf("screen %v:\n%s", m.screen, m.View())
	}
}

// reviewing marks big/ and medium.bin and walks to the review screen.
func reviewing(t *testing.T, m *model, applyFn func(context.Context, env.Env, *plan.Plan, engine.Options) (*engine.Result, error)) {
	t.Helper()
	m.planFn = func(_ context.Context, _ env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
		return &plan.Plan{Kind: kind, Items: items}, nil
	}
	m.applyFn = applyFn
	press(m, "space", "down", "space")
	m.Update(press(m, "x")())
	if m.screen != screenReviewing {
		t.Fatalf("screen = %v", m.screen)
	}
}

func TestNGoesBackWithoutApplying(t *testing.T) {
	m, _ := browser(t)
	calls := 0
	reviewing(t, m, func(context.Context, env.Env, *plan.Plan, engine.Options) (*engine.Result, error) {
		calls++
		return &engine.Result{}, nil
	})
	press(m, "n")
	if m.screen != screenBrowsing || calls != 0 {
		t.Fatalf("screen %v, apply calls %d", m.screen, calls)
	}
	if len(m.marked) != 2 {
		t.Errorf("marks lost after going back: %d", len(m.marked))
	}
	m.Update(press(m, "x")())
	press(m, "esc")
	if m.screen != screenBrowsing || calls != 0 {
		t.Fatalf("esc: screen %v, apply calls %d", m.screen, calls)
	}
}

func TestYSelectsAllAndAppliesOnce(t *testing.T) {
	m, root := browser(t)
	totalBefore := m.tree.Bytes
	filesBefore := m.tree.Files
	var applied *plan.Plan
	calls := 0
	reviewing(t, m, func(_ context.Context, _ env.Env, p *plan.Plan, opts engine.Options) (*engine.Result, error) {
		calls++
		applied = p
		if opts.MaxAge == 0 || opts.Now == nil {
			t.Errorf("not DefaultOptions: %+v", opts)
		}
		return &engine.Result{
			RunID: "20261108-1115-a3f9",
			Moved: p.Items[:1], // only big/ moves
			Skipped: []plan.Skip{
				{Path: p.Items[1].Path, Source: "analyze", Reason: plan.ReasonInUse, Detail: "open in Preview"},
			},
			Bytes: p.Items[0].Bytes,
		}, nil
	})
	bigBytes, bigFiles := m.tree.Children[0].Bytes, m.tree.Children[0].Files

	press(m, "x", "q") // other keys do nothing on the review screen
	if m.screen != screenReviewing {
		t.Fatalf("screen %v", m.screen)
	}
	cmd := press(m, "y")
	if m.screen != screenApplying || cmd == nil {
		t.Fatalf("screen %v", m.screen)
	}
	m.Update(cmd())

	if calls != 1 {
		t.Fatalf("apply calls = %d", calls)
	}
	if len(applied.Items) != 2 {
		t.Fatalf("applied %d items", len(applied.Items))
	}
	for _, it := range applied.Items {
		if !it.Selected {
			t.Errorf("%s not selected", it.Path)
		}
	}

	// the moved entry left the tree, the skipped one stayed
	if got := strings.Join(names(m.tree), " "); got != "medium.bin small.txt empty" {
		t.Fatalf("tree = %q", got)
	}
	if m.tree.Bytes != totalBefore-bigBytes || m.tree.Files != filesBefore-bigFiles {
		t.Errorf("totals = %d bytes %d files, want %d and %d",
			m.tree.Bytes, m.tree.Files, totalBefore-bigBytes, filesBefore-bigFiles)
	}
	if m.marked[filepath.Join(root, "big")] != nil || m.marked[filepath.Join(root, "medium.bin")] == nil {
		t.Errorf("marks after apply: %v", m.marked)
	}

	view := m.View()
	for _, want := range []string{
		"Moved 1 entry", "to quarantine", "sapu undo 20261108-1115-a3f9",
		"Skipped 1 entry", "in use", "open in Preview", "Press any key",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("result lacks %q:\n%s", want, view)
		}
	}

	press(m, "z")
	if m.screen != screenBrowsing {
		t.Fatalf("screen %v after key", m.screen)
	}
	if want := "1 marked, " + humanBytes(m.tree.Children[0].Bytes); !strings.Contains(m.View(), want) {
		t.Errorf("footer lacks %q:\n%s", want, m.View())
	}
}

func TestMovedEntryInsideFolderUpdatesEveryParent(t *testing.T) {
	m, root := browser(t)
	press(m, "enter", "down", "down", "enter", "space") // big/deep/c.bin
	c := filepath.Join(root, "big", "deep", "c.bin")
	node := m.marked[c]
	if node == nil {
		t.Fatalf("c.bin not marked: %v", m.marked)
	}
	rootBefore, bigBefore := m.tree.Bytes, m.tree.Children[0].Bytes

	m.planFn = func(_ context.Context, _ env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
		return &plan.Plan{Kind: kind, Items: items}, nil
	}
	m.applyFn = func(_ context.Context, _ env.Env, p *plan.Plan, _ engine.Options) (*engine.Result, error) {
		return &engine.Result{RunID: "r1", Moved: p.Items, Bytes: p.Items[0].Bytes}, nil
	}
	m.Update(press(m, "x")())
	m.Update(press(m, "y")())

	if m.tree.Bytes != rootBefore-node.Bytes || m.tree.Children[0].Bytes != bigBefore-node.Bytes {
		t.Errorf("parents not reduced: root %d (was %d), big %d (was %d), file %d",
			m.tree.Bytes, rootBefore, m.tree.Children[0].Bytes, bigBefore, node.Bytes)
	}
	press(m, "k")
	if m.current().cursor != 0 {
		t.Errorf("cursor = %d", m.current().cursor)
	}
}

func TestApplyFailureIsShown(t *testing.T) {
	m, _ := browser(t)
	reviewing(t, m, func(context.Context, env.Env, *plan.Plan, engine.Options) (*engine.Result, error) {
		return nil, errors.New("disk on fire")
	})
	m.Update(press(m, "y")())
	if m.screen != screenResult || !strings.Contains(m.View(), "Apply failed: disk on fire") {
		t.Fatalf("screen %v:\n%s", m.screen, m.View())
	}
	if len(m.tree.Children) != 4 {
		t.Errorf("tree changed after a failed apply")
	}
}

func TestResultShowsFailures(t *testing.T) {
	m, root := browser(t)
	reviewing(t, m, func(_ context.Context, _ env.Env, p *plan.Plan, _ engine.Options) (*engine.Result, error) {
		return &engine.Result{
			RunID:  "r2",
			Failed: []engine.Failure{{Item: p.Items[0], Error: "permission denied"}},
		}, nil
	})
	m.Update(press(m, "y")())
	view := m.View()
	for _, want := range []string{"Nothing was moved", "Failed 1 entry", "data/big", "permission denied"} {
		if !strings.Contains(view, want) {
			t.Errorf("result lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "sapu undo") {
		t.Errorf("undo offered when nothing moved:\n%s", view)
	}
	_ = root
}

func TestScanCancelGivesPartialTree(t *testing.T) {
	e, root := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the scan stops before it starts
	m := newModel(ctx, e, root)
	m.Init()
	for m.screen == screenScanning {
		m.Update(m.wait())
	}
	if m.tree == nil || !m.partial {
		t.Fatalf("tree nil = %v, partial = %v", m.tree == nil, m.partial)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !strings.Contains(m.View(), "partial") {
		t.Errorf("browse screen does not say partial:\n%s", m.View())
	}
}

func TestEscStopsTheScan(t *testing.T) {
	e, root := fixture(t)
	m := newModel(context.Background(), e, root)
	m.Init()
	press(m, "esc")
	if m.scanCtx.Err() == nil {
		t.Fatal("esc did not cancel the scan")
	}
	for m.screen == screenScanning {
		m.Update(m.wait())
	}
	if m.tree == nil || m.screen != screenBrowsing {
		t.Fatalf("screen %v, tree nil = %v", m.screen, m.tree == nil)
	}
}

func TestScanFinishesAndShowsLiveTotals(t *testing.T) {
	e, root := fixture(t)
	m := newModel(context.Background(), e, root)
	m.Update(progressMsg(walk.Event{Path: root, Files: 1234, Bytes: 5_600_000, Errors: 3}))
	view := m.View()
	for _, want := range []string{"Scanning", "1,234", "5.6 MB", "3", "esc/q to stop"} {
		if !strings.Contains(view, want) {
			t.Errorf("scan view lacks %q:\n%s", want, view)
		}
	}

	m.Init()
	for m.screen == screenScanning {
		m.Update(m.wait())
	}
	if m.partial || m.tree == nil || m.tree.Name != "data" {
		t.Fatalf("partial = %v, tree = %+v", m.partial, m.tree)
	}
	if strings.Contains(m.View(), "partial") {
		t.Errorf("complete scan marked partial:\n%s", m.View())
	}
}

func TestScanViewHasStableLayout(t *testing.T) {
	e, root := fixture(t)
	m := newModel(context.Background(), e, root)
	m.Update(progressMsg(walk.Event{Path: root, Files: 1, Bytes: 10}))
	a := strings.Split(m.View(), "\n")
	m.Update(progressMsg(walk.Event{Path: filepath.Join(root, "big"), Files: 9_999_999, Bytes: 987_654_321_000, Errors: 12345}))
	b := strings.Split(m.View(), "\n")
	if len(a) != len(b) {
		t.Fatalf("line count changed: %d -> %d", len(a), len(b))
	}
	for _, i := range []int{2, 3, 4} {
		if width(a[i]) != width(b[i]) {
			t.Errorf("line %d changed width: %d -> %d", i, width(a[i]), width(b[i]))
		}
	}
}

func TestViewFitsNarrowTerminals(t *testing.T) {
	for _, w := range []int{1, 5, 20, 40, 80, 140} {
		m, root := browser(t)
		m.Update(tea.WindowSizeMsg{Width: w, Height: 12})
		m.tree.Children[0].Name = strings.Repeat("a-very-long-folder-name-", 6)
		views := map[string]string{"browse": m.View()}

		press(m, "?")
		views["help"] = m.View()
		press(m, "?")

		m.planFn = func(_ context.Context, _ env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
			return &plan.Plan{Kind: kind, Items: items, Skipped: []plan.Skip{
				{Path: filepath.Join(root, "x"), Reason: plan.ReasonInUse, Detail: strings.Repeat("detail ", 30)},
			}}, nil
		}
		m.applyFn = func(_ context.Context, _ env.Env, p *plan.Plan, _ engine.Options) (*engine.Result, error) {
			return &engine.Result{RunID: "r", Moved: p.Items[:1], Bytes: 1}, nil
		}
		press(m, "space")
		m.Update(press(m, "x")())
		views["review"] = m.View()
		m.Update(press(m, "y")())
		views["result"] = m.View()

		for name, v := range views {
			if v == "" {
				t.Errorf("width %d: %s view is empty", w, name)
			}
			if w < 40 {
				continue // too small to be usable; it only must not panic
			}
			for _, line := range strings.Split(v, "\n") {
				if width(line) > w {
					t.Errorf("width %d: %s line is %d wide: %q", w, name, width(line), line)
				}
			}
		}
	}
}

func TestBrowseViewScrollsWithCursor(t *testing.T) {
	e := env.New(t.TempDir())
	root := filepath.Join(e.Home, "many")
	for i := range 30 {
		writeFile(t, filepath.Join(root, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), (i+1)*4096)
	}
	tree, err := walk.Tree(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), e, root)
	m.setTree(tree, false)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})

	lines := len(strings.Split(m.View(), "\n"))
	for range 25 {
		press(m, "down")
	}
	if got := len(strings.Split(m.View(), "\n")); got != lines {
		t.Errorf("view height changed while scrolling: %d -> %d", lines, got)
	}
	cursorRow := ""
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.HasPrefix(l, ">") {
			cursorRow = l
		}
	}
	if !strings.Contains(cursorRow, tree.Children[25].Name) {
		t.Errorf("cursor row %q is not entry 25 (%s)", cursorRow, tree.Children[25].Name)
	}
}

func TestNamesCannotInjectEscapeCodes(t *testing.T) {
	m, _ := browser(t)
	m.tree.Children[1].Name = "evil\x1b[2Jname\n"
	if strings.Contains(m.View(), "\x1b[2J") {
		t.Errorf("escape code reached the terminal")
	}
}

func TestQuitKeys(t *testing.T) {
	m, _ := browser(t)
	if cmd := press(m, "q"); cmd == nil || cmd() != tea.Quit() {
		t.Errorf("q does not quit")
	}
	m, _ = browser(t)
	if cmd := press(m, "ctrl+c"); cmd == nil || cmd() != tea.Quit() {
		t.Errorf("ctrl+c does not quit")
	}
}

func TestTop(t *testing.T) {
	_, root := fixture(t)
	full, err := walk.Tree(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	top, err := Top(context.Background(), root, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if top.Bytes != full.Bytes || top.Files != full.Files {
		t.Errorf("totals %d/%d, want %d/%d", top.Bytes, top.Files, full.Bytes, full.Files)
	}
	if got := strings.Join(names(top), " "); got != "big medium.bin" {
		t.Errorf("level 1 = %q", got)
	}
	big := top.Children[0]
	if got := strings.Join(names(big), " "); got != "a.bin b.bin" {
		t.Errorf("level 2 = %q", got)
	}
	if big.Bytes != full.Children[0].Bytes {
		t.Errorf("big bytes %d, want %d", big.Bytes, full.Children[0].Bytes)
	}
	for _, c := range big.Children {
		if len(c.Children) != 0 {
			t.Errorf("%s keeps children below depth 2", c.Name)
		}
	}

	one, err := Top(context.Background(), root, 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Children) != 4 || len(one.Children[0].Children) != 0 {
		t.Errorf("depth 1: %d children, big has %d", len(one.Children), len(one.Children[0].Children))
	}

	zero, err := Top(context.Background(), root, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(zero.Children) != 0 || zero.Bytes != full.Bytes {
		t.Errorf("n=0: %d children, %d bytes", len(zero.Children), zero.Bytes)
	}

	if _, err := Top(context.Background(), filepath.Join(root, "missing"), 3, 3); err == nil {
		t.Errorf("missing root gave no error")
	}
}

func TestHumanBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 999: "999 B", 1000: "1.0 KB", 1500: "1.5 KB",
		999_949: "999.9 KB", 999_950: "1.0 MB", 12_300_000_000: "12.3 GB", 5_000_000_000_000: "5.0 TB",
	} {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	for _, c := range []struct {
		in   string
		w    int
		want string
	}{
		{"short", 10, "short"},
		{"node_modules_backup_2021", 10, "node_…2021"},
		{"abcdef", 1, "…"},
		{"abcdef", 0, ""},
		{"日本語のファイル名", 7, "日本…名"},
	} {
		got := truncateMiddle(c.in, c.w)
		if width(got) > c.w {
			t.Errorf("truncateMiddle(%q, %d) = %q is %d wide", c.in, c.w, got, width(got))
		}
		if c.w >= 10 && got != c.want {
			t.Errorf("truncateMiddle(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
	}
	if got := commas(1234567); got != "1,234,567" {
		t.Errorf("commas = %q", got)
	}
	if got := commas(12); got != "12" {
		t.Errorf("commas = %q", got)
	}
}

// The whole program: scan, stop with q, browse, quit with q. No terminal.
func TestProgramRunsEndToEnd(t *testing.T) {
	e, root := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	in, out := io.Pipe()
	defer func() { _ = in.Close() }()
	go func() {
		for {
			if _, err := out.Write([]byte("q")); err != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	m := newModel(ctx, e, root)
	final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithInput(in), tea.WithOutput(io.Discard)).Run()
	if err != nil {
		t.Fatal(err)
	}
	if final.(*model).tree == nil {
		t.Fatal("program quit before it had a tree")
	}
}

// The quarantine settings of sapu.toml reach engine.Apply.
func TestApplyUsesConfiguredQuarantineSettings(t *testing.T) {
	m, _ := browser(t)
	m.cfg.MaxAge = 3 * 24 * time.Hour
	m.cfg.MaxBytes = 5_000_000_000
	var got engine.Options
	reviewing(t, m, func(_ context.Context, _ env.Env, p *plan.Plan, opts engine.Options) (*engine.Result, error) {
		got = opts
		return &engine.Result{}, nil
	})
	m.Update(press(m, "y")())
	if got.MaxAge != 3*24*time.Hour || got.MaxBytes != 5_000_000_000 {
		t.Errorf("apply options MaxAge %v, MaxBytes %d; want 72h and 5 GB", got.MaxAge, got.MaxBytes)
	}
}
