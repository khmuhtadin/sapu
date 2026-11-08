package review

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// The fake plan uses a made-up home that does not exist: these tests never
// touch the disk.
const home = "/nonexistent/home"

var created = time.Date(2026, 11, 8, 11, 15, 0, 0, time.UTC)

func item(id, source, rel string, bytes int64, risk rules.Risk, regen bool, explain string) plan.Item {
	return plan.Item{
		ID: id, Source: source, Path: filepath.Join(home, rel), Bytes: bytes, Risk: risk,
		Regenerates: regen, Explain: explain, Action: rules.ActionQuarantine,
		Selected: risk != rules.RiskRisky, // what engine.Plan does
	}
}

const (
	gb = 1_000_000_000
	mb = 1_000_000
)

// fakePlan looks like a real `sapu clean` plan: 7 SAFE, 4 MODERATE and 2
// RISKY items, one official command and 4 skipped paths.
func fakePlan() *plan.Plan {
	items := []plan.Item{
		item("0001", "chrome.cache", "Library/Caches/Google/Chrome/Default/Cache", 1300*mb, rules.RiskSafe, true,
			"Chrome's web cache: copies of pages and images. Chrome downloads them again as you browse."),
		item("0002", "safari.cache", "Library/Caches/com.apple.Safari", 640*mb, rules.RiskSafe, true,
			"Safari's web cache. Safari rebuilds it as you browse."),
		item("0003", "logs.diagnostic-reports", "Library/Logs/DiagnosticReports", 120*mb, rules.RiskSafe, true,
			"Crash and hang reports macOS keeps for developers. New ones are written when an app crashes."),
		item("0004", "apple.help-cache", "Library/Caches/com.apple.helpd", 45*mb, rules.RiskSafe, true,
			"The cache of the Help viewer. macOS rebuilds it."),
		item("0005", "logs.npm", ".npm/_logs/2026-11-08T07_23_09_563Z-debug-0.log", 40_000, rules.RiskSafe, true,
			"A debug log npm wrote. npm writes new ones."),
		item("0006", "logs.npm", ".npm/_logs/2026-11-08T07_26_49_230Z-debug-0.log", 30_000, rules.RiskSafe, true,
			"A debug log npm wrote. npm writes new ones."),
		item("0007", "logs.npm", ".npm/_logs/2026-11-08T07_28_15_228Z-debug-0.log", 20_000, rules.RiskSafe, true,
			"A debug log npm wrote. npm writes new ones."),
		item("0008", "xcode.derived-data", "Library/Developer/Xcode/DerivedData/Sapu-gxbnqtzjvmbdkd", 5200*mb, rules.RiskModerate, true,
			"Xcode build output and indexes, one folder per project. Xcode rebuilds it, so the next build is slower."),
		item("0009", "homebrew.downloads", "Library/Caches/Homebrew/downloads", 4100*mb, rules.RiskModerate, true,
			"Bottles and source archives Homebrew downloaded. brew downloads them again when needed."),
		item("0010", "npm.cache", ".npm/_cacache", 3600*mb, rules.RiskModerate, true,
			"npm's package cache. npm downloads packages again on the next install."),
		item("0011", "spotify.cache", "Library/Caches/com.spotify.client/Data", 2900*mb, rules.RiskModerate, true,
			"Songs you saved offline download again."),
		item("0012", "xcode.archives", "Library/Developer/Xcode/Archives/2025-03-01/Sapu 1.0.xcarchive", 2100*mb, rules.RiskRisky, false,
			"An app build you archived in Xcode. You need it to read crash reports of that version."),
		item("0013", "telegram.media", "Library/Group Containers/6N38VWS5BX.ru.keepcoder.Telegram/media", 1000*mb, rules.RiskRisky, false,
			"Photos and videos Telegram downloaded. Media from deleted chats cannot come back."),
	}
	cmd := item("0014", "go.build-cache", "Library/Caches/go-build", 1200*mb, rules.RiskModerate, true,
		"Go's build cache. `go clean -cache` empties it; Go rebuilds what it needs.")
	cmd.Action = rules.ActionOfficialCommand
	cmd.Command = []string{"go", "clean", "-cache"}
	items = append(items, cmd)
	return &plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		Kind:          plan.KindClean,
		CreatedAt:     created,
		Home:          home,
		Items:         items,
		Skipped: []plan.Skip{
			{Path: home + "/.claude", Source: "claude.cache", Reason: plan.ReasonProtected, Detail: "protected:claude"},
			{Path: home + "/.npm/_npx/9f3c1a2b", Source: "npm.cache", Reason: plan.ReasonInUse, Detail: "open in node (pid 4242)"},
			{Path: home + "/Library/Caches/JetBrains", Source: "jetbrains.cache", Reason: plan.ReasonProtected, Detail: "protected:jetbrains"},
			{Path: home + "/Projects/keep", Source: "dev.cache", Reason: plan.ReasonWhitelisted, Detail: "whitelisted:~/Projects/keep"},
		},
	}
}

func why(detail string) string {
	return map[string]string{
		"protected:claude":    "Claude Code memory and settings",
		"protected:jetbrains": "JetBrains IDE settings and indexes",
	}[detail]
}

// fakeApply records every call and returns what result says.
type fakeApply struct {
	mu     sync.Mutex
	calls  []*plan.Plan
	opts   []engine.Options
	result func(ctx context.Context, p *plan.Plan, opts engine.Options) (*engine.Result, error)
}

func (f *fakeApply) apply(ctx context.Context, _ env.Env, p *plan.Plan, opts engine.Options) (*engine.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, p)
	f.opts = append(f.opts, opts)
	f.mu.Unlock()
	if f.result != nil {
		return f.result(ctx, p, opts)
	}
	return movedAll(p), nil
}

func (f *fakeApply) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func movedAll(p *plan.Plan) *engine.Result {
	r := &engine.Result{RunID: "20261108-1115-a3f9", Moved: []plan.Item{}, Skipped: []plan.Skip{}, Failed: []engine.Failure{}}
	for _, it := range p.Items {
		r.Moved = append(r.Moved, it)
		r.Bytes += it.Bytes
	}
	return r
}

// listed returns a model that shows the fake plan at 80x40.
func listed(t *testing.T) (*model, *fakeApply) {
	t.Helper()
	f := &fakeApply{}
	m := newModel(context.Background(), env.New(home), nil, f.apply, why)
	m.now = func() time.Time { return created }
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	m.Update(planMsg{plan: fakePlan()})
	return m, f
}

func press(m *model, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
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
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
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

// typeText sends each rune as its own key, like typing.
func typeText(m *model, text string) {
	for _, r := range text {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// goTo moves the cursor to the first row of kind whose text holds want.
func goTo(t *testing.T, m *model, kind rowKind, want string) {
	t.Helper()
	for i, r := range m.rows() {
		if r.kind == kind && strings.Contains(m.rowLines(r, false)[0], want) {
			m.cursor = i
			m.scroll()
			return
		}
	}
	t.Fatalf("no row of kind %d with %q:\n%s", kind, want, m.View())
}

// flat joins wrapped lines, so a test can look for a whole sentence.
func flat(view string) string { return strings.Join(strings.Fields(view), " ") }

// keysText is the two lines of keys under the list.
func (m *model) keysText() string { return strings.Join(m.keyLines(), " ") }

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// finish runs the apply messages until the receipt shows.
func finish(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	for i := 0; m.screen == screenApplying; i++ {
		if cmd == nil || i > 1000 {
			t.Fatal("apply never finished")
		}
		_, cmd = m.Update(cmd())
	}
}

func groupLine(t *testing.T, m *model, risk rules.Risk) string {
	t.Helper()
	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, riskWord(risk)+" ") && strings.Contains(line, " items ") || strings.Contains(line, riskWord(risk)+" ") && strings.Contains(line, " item ") {
			return line
		}
	}
	t.Fatalf("no %s heading:\n%s", risk, m.View())
	return ""
}

func selectedIDs(p *plan.Plan) []string {
	var ids []string
	for _, it := range p.Items {
		if it.Selected {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

func TestStartsWithSafeAndModerateSelectedAndRiskyClosed(t *testing.T) {
	m, _ := listed(t)
	view := m.View()
	safe, moderate, risky := groupLine(t, m, rules.RiskSafe), groupLine(t, m, rules.RiskModerate), groupLine(t, m, rules.RiskRisky)
	if !strings.Contains(safe, "[v] SAFE / Safe to recreate") || !strings.HasSuffix(safe, "[x]") || !strings.Contains(safe, "7 items") {
		t.Errorf("SAFE heading = %q", safe)
	}
	if !strings.HasSuffix(moderate, "[x]") || !strings.Contains(moderate, "4 items") {
		t.Errorf("MODERATE heading = %q", moderate)
	}
	if !strings.Contains(risky, "[>] RISKY / May contain personal data") || !strings.HasSuffix(risky, "[ ]") {
		t.Errorf("RISKY must start closed and unselected: %q", risky)
	}
	if strings.Contains(view, "xcarchive") {
		t.Errorf("RISKY items must be hidden while the group is closed:\n%s", view)
	}
	for _, want := range []string{"sapu / review", "Review what Sapu found. Nothing has moved.", "Focus a row to read its location, reason and risk.",
		"SAFE / Safe to recreate", "MODERATE / Needs review", "RISKY / May contain personal data", "SKIPPED / Protected and in use",
		"Not selected. Read the explanation before choosing an item.",
		"chrome cache", "~/Library/Caches/com.apple.Safari", "1.3 GB",
		"~/.claude", "protected", "Claude Code memory and settings", "in use", "open in node (pid 4242)",
		"whitelisted", "Commands you can run yourself", "go clean -cache",
		"up/down move", "space select", "left close", "/ filter", "enter review", "a select safe", "? all keys", "q cancel",
		"11 items /  17.9 GB selected", "-> quarantine, undo up to 7 days"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
}

func TestGroupCheckboxStates(t *testing.T) {
	m, _ := listed(t)
	goTo(t, m, rowItem, "chrome cache")
	press(m, "space")
	if got := groupLine(t, m, rules.RiskSafe); !strings.HasSuffix(got, "[-]") {
		t.Errorf("some selected: %q", got)
	}
	goTo(t, m, rowGroup, "SAFE")
	press(m, "space") // some -> all
	if got := groupLine(t, m, rules.RiskSafe); !strings.HasSuffix(got, "[x]") {
		t.Errorf("all selected: %q", got)
	}
	press(m, "space") // all -> none
	if got := groupLine(t, m, rules.RiskSafe); !strings.HasSuffix(got, "[ ]") {
		t.Errorf("none selected: %q", got)
	}
	for _, i := range m.groups[0].items {
		if m.plan.Items[i].Selected {
			t.Errorf("%s still selected", m.plan.Items[i].ID)
		}
	}
}

func TestASelectsAllSafeAndNeverRisky(t *testing.T) {
	m, _ := listed(t)
	goTo(t, m, rowGroup, "SAFE")
	press(m, "space") // all safe off
	goTo(t, m, rowGroup, "MODERATE")
	press(m, "space") // all moderate off
	goTo(t, m, rowGroup, "RISKY")
	press(m, "right", "down") // on a risky item
	press(m, "a")
	for _, it := range m.plan.Items {
		want := it.Risk == rules.RiskSafe && it.Action == rules.ActionQuarantine
		if it.Selected != want {
			t.Errorf("%s (%s) selected = %v after a", it.ID, it.Risk, it.Selected)
		}
	}
	if !strings.Contains(m.View(), "Selected all 7 safe items.") {
		t.Errorf("a should say what it did:\n%s", m.View())
	}
}

func TestRiskyNeedsASecondSpace(t *testing.T) {
	m, _ := listed(t)
	goTo(t, m, rowGroup, "RISKY")
	press(m, "space") // a RISKY group is never selected in bulk
	for _, it := range m.plan.Items {
		if it.Risk == rules.RiskRisky && it.Selected {
			t.Fatalf("space on the RISKY heading selected %s", it.ID)
		}
	}
	if !m.groups[2].open {
		t.Error("space on the RISKY heading should open it")
	}

	goTo(t, m, rowItem, "xcode archives")
	i := m.rows()[m.cursor].index
	press(m, "space")
	view := flat(m.View())
	if m.plan.Items[i].Selected {
		t.Fatal("the first space selected a RISKY item")
	}
	for _, want := range []string{"You need it to read crash reports", "~/Library/Developer/Xcode/Archives/2025-03-01/Sapu 1.0.xcarchive", "Regenerates: no / Risk: RISKY", "Press space again to select it"} {
		if !strings.Contains(view, want) {
			t.Errorf("after the first space the view lacks %q:\n%s", want, view)
		}
	}

	press(m, "down", "up", "space") // moving away and back asks again
	if m.plan.Items[i].Selected {
		t.Fatal("a space after moving away selected a RISKY item")
	}
	press(m, "space")
	if !m.plan.Items[i].Selected {
		t.Fatal("the second space did not select the RISKY item")
	}
	if got := groupLine(t, m, rules.RiskRisky); !strings.HasSuffix(got, "[-]") {
		t.Errorf("RISKY heading = %q", got)
	}
	press(m, "space") // unselecting takes one space
	if m.plan.Items[i].Selected {
		t.Error("one space should unselect a RISKY item")
	}
}

func TestFilter(t *testing.T) {
	m, _ := listed(t)
	press(m, "/")
	typeText(m, "NPM")
	if !strings.Contains(m.View(), "/ NPM█") {
		t.Errorf("the footer should show the filter being typed:\n%s", m.View())
	}
	press(m, "enter")
	view := m.View()
	for _, want := range []string{"logs npm", "npm cache", "~/.npm/_npx/9f3c1a2b", "Filter: NPM"} {
		if !strings.Contains(view, want) {
			t.Errorf("filtered view lacks %q:\n%s", want, view)
		}
	}
	for _, gone := range []string{"chrome cache", "Homebrew", "~/.claude", "go clean"} {
		if strings.Contains(view, gone) {
			t.Errorf("filtered view still has %q:\n%s", gone, view)
		}
	}
	if got := groupLine(t, m, rules.RiskSafe); !strings.Contains(got, "3 items") {
		t.Errorf("the SAFE heading counts the matches: %q", got)
	}

	// A group toggle under a filter changes only what is shown.
	goTo(t, m, rowGroup, "SAFE")
	press(m, "space")
	for _, it := range m.plan.Items {
		if it.Source == "logs.npm" && it.Selected || it.Source == "chrome.cache" && !it.Selected {
			t.Errorf("%s selected = %v", it.ID, it.Selected)
		}
	}

	press(m, "esc")
	if m.filter != "" || !strings.Contains(m.View(), "chrome cache") {
		t.Errorf("esc should clear the filter:\n%s", m.View())
	}

	press(m, "/")
	typeText(m, "zzz")
	press(m, "backspace", "backspace", "backspace", "esc")
	if m.filter != "" || m.filtering {
		t.Errorf("esc while typing clears the filter: %q %v", m.filter, m.filtering)
	}
}

func TestExpandAndCollapse(t *testing.T) {
	m, _ := listed(t)
	// More than 6 SAFE items: 5 shown, then "… 2 more".
	if !strings.Contains(m.View(), "… 2 more") {
		t.Fatalf("SAFE should end with … 2 more:\n%s", m.View())
	}
	goTo(t, m, rowMore, "more")
	if !m.enterExpands() || !strings.Contains(m.keysText(), "enter expand") {
		t.Error("enter on … more should expand, and the footer should say so")
	}
	press(m, "enter")
	if strings.Contains(m.View(), "… 2 more") || m.screen != screenList {
		t.Errorf("enter should show all SAFE items:\n%s", m.View())
	}

	goTo(t, m, rowItem, "safari cache")
	press(m, "right", "down") // pinned details stay open
	if !strings.Contains(m.View(), "Safari rebuilds it as you browse.") {
		t.Errorf("→ should keep the details open:\n%s", m.View())
	}
	press(m, "up", "left")
	if strings.Count(m.View(), "Safari rebuilds it as you browse.") != 1 {
		t.Error("← on a pinned item closes its pinned details only")
	}
	press(m, "left") // closes the group, cursor to its heading
	if r := m.rows()[m.cursor]; r.kind != rowGroup || m.groups[r.group].risk != rules.RiskSafe || m.groups[0].open {
		t.Errorf("← should close SAFE and put the cursor on it: %+v", r)
	}
}

func TestConfirmIsTheOnlyWayToApply(t *testing.T) {
	m, f := listed(t)
	// Every key of the list, many times. None of them may start a move.
	for range 3 {
		for _, k := range []string{"down", "space", "right", "left", "a", "?", "x", "y", "Y", "up", "/", "y", "enter", "esc", "j", "k", "G", "g"} {
			if m.screen == screenConfirm {
				break
			}
			press(m, k)
		}
	}
	if f.count() != 0 || m.screen == screenApplying {
		t.Fatalf("a key on the list started a move")
	}

	m, f = listed(t)
	before := selectedIDs(m.plan)
	press(m, "enter")
	if m.screen != screenConfirm {
		t.Fatalf("enter should open the confirm screen, got %d", m.screen)
	}
	press(m, "n")
	if m.screen != screenList || !slices.Equal(selectedIDs(m.plan), before) {
		t.Fatal("n should go back with the selection unchanged")
	}
	press(m, "enter", "esc")
	if m.screen != screenList || !slices.Equal(selectedIDs(m.plan), before) {
		t.Fatal("esc should go back with the selection unchanged")
	}
	press(m, "enter")
	for _, k := range []string{"enter", "space", "a", "q", "x", "yes"} {
		press(m, k)
	}
	if f.count() != 0 || m.screen != screenConfirm {
		t.Fatalf("only y may move: calls %d, screen %d", f.count(), m.screen)
	}
	finish(t, m, press(m, "y"))
	if f.count() != 1 {
		t.Fatalf("y should apply once, got %d calls", f.count())
	}
}

func TestNothingSelectedCannotConfirm(t *testing.T) {
	m, _ := listed(t)
	goTo(t, m, rowGroup, "SAFE")
	press(m, "space")
	goTo(t, m, rowGroup, "MODERATE")
	press(m, "space", "enter")
	if m.screen != screenList || !strings.Contains(m.View(), "Nothing selected.") {
		t.Errorf("with nothing selected enter must stay on the list:\n%s", m.View())
	}
}

func TestApplyGetsExactlyTheSelectedItems(t *testing.T) {
	m, f := listed(t)
	goTo(t, m, rowItem, "chrome cache")
	press(m, "space") // off
	goTo(t, m, rowItem, "npm cache")
	press(m, "space") // off
	goTo(t, m, rowGroup, "RISKY")
	press(m, "right")
	goTo(t, m, rowItem, "telegram media")
	press(m, "space", "space") // on
	goTo(t, m, rowCommand, "go clean")
	press(m, "space") // never selected
	if !strings.Contains(m.View(), "Sapu does not run commands") {
		t.Errorf("space on a command should say Sapu does not run it:\n%s", m.View())
	}

	press(m, "enter")
	confirm := m.View()
	for _, want := range []string{"Move 10 items to quarantine?", "SAFE 6 / MODERATE 3 / RISKY 1", "1 RISKY item may contain personal data"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirm lacks %q:\n%s", want, confirm)
		}
	}
	finish(t, m, press(m, "y"))

	if f.count() != 1 {
		t.Fatalf("apply called %d times", f.count())
	}
	p, opts := f.calls[0], f.opts[0]
	var got []string
	for _, it := range p.Items {
		got = append(got, it.ID)
		if !it.Selected || it.Action != rules.ActionQuarantine {
			t.Errorf("apply got %s selected=%v action=%s", it.ID, it.Selected, it.Action)
		}
	}
	want := []string{"0002", "0003", "0004", "0005", "0006", "0007", "0008", "0009", "0011", "0013"}
	if !slices.Equal(got, want) {
		t.Errorf("apply got %v, want %v", got, want)
	}
	if opts.Permanent || opts.RunCommands || opts.Progress == nil || opts.MaxAge <= 0 || opts.MaxBytes <= 0 {
		t.Errorf("apply options = %+v", opts)
	}
	if p.Home != home || !p.CreatedAt.Equal(created) || p.Kind != plan.KindClean {
		t.Errorf("apply got a plan for %s at %s", p.Home, p.CreatedAt)
	}
	if m.plan.Items[13].Selected {
		t.Error("the official command was selected")
	}
}

// stepper is an apply that moves one item each time next receives.
type stepper struct {
	next    chan struct{}
	started chan struct{}
}

func newStepper() *stepper {
	return &stepper{next: make(chan struct{}), started: make(chan struct{}, 1)}
}

func (s *stepper) apply(ctx context.Context, p *plan.Plan, opts engine.Options) (*engine.Result, error) {
	r := &engine.Result{RunID: "20261108-1115-a3f9", Moved: []plan.Item{}, Skipped: []plan.Skip{}, Failed: []engine.Failure{}}
	s.started <- struct{}{}
	for i, it := range p.Items {
		select {
		case <-s.next:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			return r, ctx.Err() // stops between items, like the engine
		}
		r.Moved = append(r.Moved, it)
		r.Bytes += it.Bytes
		opts.Progress(i+1, len(p.Items), it)
	}
	return r, nil
}

func TestProgressUpdates(t *testing.T) {
	m, f := listed(t)
	s := newStepper()
	f.result = s.apply
	cmd := press(m, "enter", "y")
	<-s.started
	if m.screen != screenApplying || !strings.Contains(m.View(), "0 / 11 items") || !strings.Contains(m.View(), "Current: chrome cache") {
		t.Fatalf("before the first item:\n%s", m.View())
	}
	s.next <- struct{}{}
	_, cmd = m.Update(cmd()) // progress 1/11
	view := m.View()
	for _, want := range []string{"sapu / moving to quarantine", "Moving selected items. You can stop after the current item.",
		"[##.", "1 / 11 items", "Current: safari cache", "~/Library/Caches/com.apple.Safari",
		"Completed items stay in quarantine if this run is interrupted.", "ctrl-c stop"} {
		if !strings.Contains(view, want) {
			t.Errorf("progress lacks %q:\n%s", want, view)
		}
	}
	for range 10 {
		s.next <- struct{}{}
		_, cmd = m.Update(cmd())
	}
	if !strings.Contains(m.View(), "11 / 11 items") {
		t.Errorf("after the last item:\n%s", m.View())
	}
	finish(t, m, cmd)
	if !strings.Contains(m.View(), "OK  Moved 11 items / 17.9 GB to quarantine.") {
		t.Errorf("receipt:\n%s", m.View())
	}
}

func TestCtrlCWhileMovingCancelsAndWaits(t *testing.T) {
	m, f := listed(t)
	s := newStepper()
	f.result = s.apply
	cmd := press(m, "enter", "y")
	<-s.started
	s.next <- struct{}{}
	_, cmd = m.Update(cmd())

	if quit := press(m, "ctrl+c"); quit != nil {
		t.Fatal("ctrl+c while moving must not quit")
	}
	if m.screen != screenApplying || !m.cancelled || !strings.Contains(m.View(), "Stopping after the current item") {
		t.Fatalf("ctrl+c should stop after the current item:\n%s", m.View())
	}
	press(m, "ctrl+c", "q", "esc") // nothing else ends the screen either
	if m.screen != screenApplying {
		t.Fatal("left the progress screen while the move runs")
	}
	finish(t, m, cmd)
	view := m.View()
	for _, want := range []string{"STOPPED  Moved 1 of 11 items / 1.3 GB", "sapu undo 20261108-1115-a3f9"} {
		if !strings.Contains(view, want) {
			t.Errorf("receipt lacks %q:\n%s", want, view)
		}
	}
	if !errors.Is(m.run.err, context.Canceled) {
		t.Errorf("apply's context was not cancelled: %v", m.run.err)
	}
	if !isQuit(press(m, "q")) {
		t.Error("q should quit the receipt")
	}
}

func TestReceipt(t *testing.T) {
	m, f := listed(t)
	m.now = func() time.Time { return created }
	f.result = func(ctx context.Context, p *plan.Plan, opts engine.Options) (*engine.Result, error) {
		r := movedAll(p)
		r.Moved = r.Moved[2:]
		r.Bytes -= p.Items[0].Bytes + p.Items[1].Bytes
		r.Skipped = []plan.Skip{{Path: p.Items[0].Path, Source: p.Items[0].Source, Reason: plan.ReasonChanged, Detail: "changed"}}
		r.Failed = []engine.Failure{{Item: p.Items[1], Error: "refused: outside home"}}
		r.Freed = 3 * gb
		return r, nil
	}
	finish(t, m, press(m, "enter", "y"))
	view := flat(m.View())
	for _, want := range []string{
		"sapu / receipt",
		"REVIEW 9 moved / 1 skipped / 1 failed",
		"Moved 9", "Skipped 1", "Failed 1",
		"SKIPPED ~/Library/Caches/Google/Chrome/Default/Cache", "Changed since the plan was made. Left in place.",
		"FAILED ~/Library/Caches/com.apple.Safari", "Refused: outside home.",
		"The 9 items completed are in quarantine.",
		"Quarantined files still use space.",
		"Eligible to expire after: 15 November 2026",
		"Older runs may expire sooner if quarantine exceeds 20.0 GB.",
		"Restore this run: sapu undo 20261108-1115-a3f9",
		"Inspect held runs: sapu quarantine list",
		"Free the space now: sapu quarantine empty 20261108-1115-a3f9",
		"Also deleted 3.0 GB from older runs",
		"q quit",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("receipt lacks %q:\n%s", want, view)
		}
	}
}

func TestApplyErrorShowsOnReceipt(t *testing.T) {
	m, f := listed(t)
	f.result = func(context.Context, *plan.Plan, engine.Options) (*engine.Result, error) {
		return nil, errors.New("another sapu is working on this Mac right now")
	}
	finish(t, m, press(m, "enter", "y"))
	view := m.View()
	if !strings.Contains(view, "FAILED  Nothing was moved.") || !strings.Contains(view, "another sapu is working") {
		t.Errorf("receipt:\n%s", view)
	}
}

func TestQuitAndCtrlCBeforeApply(t *testing.T) {
	m, f := listed(t)
	if !isQuit(press(m, "q")) || m.cancelled || f.count() != 0 {
		t.Error("q on the list quits without cancelling or moving")
	}
	m, f = listed(t)
	if !isQuit(press(m, "ctrl+c")) || !m.cancelled || f.count() != 0 {
		t.Error("ctrl+c on the list quits as cancelled")
	}
	m, _ = listed(t)
	press(m, "enter")
	if !isQuit(press(m, "ctrl+c")) || !m.cancelled {
		t.Error("ctrl+c on the confirm screen quits as cancelled")
	}
}

func TestInitScansThenLists(t *testing.T) {
	planned := make(chan struct{})
	planFn := func(ctx context.Context, progress func(walk.Event)) (*plan.Plan, error) {
		progress(walk.Event{Path: home + "/Library/Caches", Files: 12345, Bytes: 3 * gb})
		<-planned
		return fakePlan(), nil
	}
	m := newModel(context.Background(), env.New(home), planFn, (&fakeApply{}).apply, why)
	m.Init()
	m.Update(m.waitScan()) // the progress event
	view := m.View()
	for _, want := range []string{"sapu / review", "Scanning caches", "12,345 files checked / 3.0 GB measured", "~/Library/Caches"} {
		if !strings.Contains(view, want) {
			t.Errorf("scan view lacks %q:\n%s", want, view)
		}
	}
	close(planned)
	m.Update(m.waitScan())
	if m.screen != screenList || !strings.Contains(m.View(), "SAFE") {
		t.Fatalf("after the plan:\n%s", m.View())
	}
}

func TestPlanErrorQuits(t *testing.T) {
	planFn := func(context.Context, func(walk.Event)) (*plan.Plan, error) { return nil, errors.New("disk on fire") }
	m := newModel(context.Background(), env.New(home), planFn, (&fakeApply{}).apply, why)
	m.Init()
	_, cmd := m.Update(m.waitScan())
	if !isQuit(cmd) || m.fatal == nil {
		t.Errorf("a failed plan should quit with the error: %v", m.fatal)
	}
}

func TestEmptyPlan(t *testing.T) {
	m, _ := listed(t)
	m.Update(planMsg{plan: &plan.Plan{SchemaVersion: plan.SchemaVersion, Home: home, Items: []plan.Item{}, Skipped: []plan.Skip{}}})
	press(m, "down", "space", "right", "left", "a", "enter")
	if m.screen != screenList || !strings.Contains(m.View(), "Nothing found that Sapu can move.") {
		t.Errorf("empty plan:\n%s", m.View())
	}
}

func TestStartingSelectionWithRiskyOpensRisky(t *testing.T) {
	p := fakePlan()
	p.Items[11].Selected = true // as with --risk risky
	m, _ := listed(t)
	m.Update(planMsg{plan: p})
	if !m.groups[2].open || !strings.Contains(m.View(), "xcarchive") {
		t.Errorf("a selected RISKY item must be visible:\n%s", m.View())
	}
}

// Every screen, at 40, 80 and 120 columns, and short heights: no panic and
// no line wider than the terminal.
func TestViewFitsTheWidth(t *testing.T) {
	for _, w := range []int{40, 48, 80, 120} {
		for _, h := range []int{8, 24, 50} {
			t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
				m, f := listed(t)
				s := newStepper()
				f.result = s.apply
				m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				check := func(what string) {
					t.Helper()
					view := m.View()
					for _, line := range strings.Split(view, "\n") {
						if n := width(line); n > w {
							t.Errorf("%s: line is %d wide, want at most %d: %q", what, n, w, line)
						}
					}
				}
				check("list")
				goTo(t, m, rowGroup, "RISKY")
				press(m, "right", "down", "space")
				check("risky armed")
				press(m, "?")
				check("help")
				press(m, "x", "G")
				check("skipped and commands")
				goTo(t, m, rowSkip, "claude")
				check("skip details")
				press(m, "/")
				typeText(m, "cache")
				check("filtering")
				press(m, "enter", "esc", "g", "enter")
				check("confirm")
				cmd := press(m, "y")
				<-s.started
				check("applying")
				s.next <- struct{}{}
				_, cmd = m.Update(cmd())
				check("progress")
				press(m, "ctrl+c")
				check("stopping")
				finish(t, m, cmd)
				check("receipt")
			})
		}
	}
	m := newModel(context.Background(), env.New(home), nil, nil, nil)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 10})
	for _, line := range strings.Split(m.View(), "\n") {
		if width(line) > 40 {
			t.Errorf("scan view line too wide: %q", line)
		}
	}
}

// The quarantine settings of sapu.toml reach engine.Apply and every text
// that names them: the list, the confirm screen and the receipt.
func TestQuarantineSettingsReachApplyAndTexts(t *testing.T) {
	m, f := listed(t)
	m.cfg.MaxAge = 3 * 24 * time.Hour
	m.cfg.MaxBytes = 5_000_000_000
	until := created.Add(m.cfg.MaxAge).Format("2 January 2006")

	if view := flat(m.View()); !strings.Contains(view, "quarantine, undo up to 3 days") || strings.Contains(view, "7 days") {
		t.Errorf("list does not show the configured age:\n%s", view)
	}
	press(m, "enter")
	for _, text := range []string{"Runs can expire after 3 days", "quarantine exceeds 5.0 GB"} {
		if view := flat(m.View()); !strings.Contains(view, text) {
			t.Errorf("confirm lacks %q:\n%s", text, view)
		}
	}
	finish(t, m, press(m, "y"))

	if got := f.opts[0]; got.MaxAge != m.cfg.MaxAge || got.MaxBytes != m.cfg.MaxBytes {
		t.Errorf("apply options MaxAge %v, MaxBytes %d; want the configured 72h and 5 GB", got.MaxAge, got.MaxBytes)
	}
	view := flat(m.View())
	for _, text := range []string{"Eligible to expire after: " + until, "quarantine exceeds 5.0 GB"} {
		if !strings.Contains(view, text) {
			t.Errorf("receipt lacks %q:\n%s", text, view)
		}
	}
	if strings.Contains(view, "20.0 GB") {
		t.Errorf("receipt still names the built-in 20.0 GB:\n%s", view)
	}
}
