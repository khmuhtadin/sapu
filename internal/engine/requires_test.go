package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// appHome is a home with an app bundle in ~/Programs (after ~/Library in
// path order, so the plan lists the leftovers first) and two leftovers.
func appHome(t *testing.T) env.Env {
	t.Helper()
	return newHome(t, map[string]string{
		"Programs/Tool.app/Contents/Info.plist":           "plist",
		"Library/Application Support/com.example.tool/db": "data",
		"Library/Caches/com.example.tool/c":               "cache",
	})
}

// appPlan plans the app, its leftovers and a command on the app's path
// that writes marker, the way sapu uninstall does: everything but the app
// requires the app.
func appPlan(t *testing.T, e env.Env, marker string) *plan.Plan {
	t.Helper()
	cmd := command(t, e, "Programs/Tool.app", rules.RiskModerate, script(t, `touch "$1"`), marker)
	p := makePlan(t, e,
		cmd, // before the app: Plan drops an item at the app's path after the app as covered by it
		item(t, e, "Programs/Tool.app", rules.RiskModerate),
		item(t, e, "Library/Application Support/com.example.tool", rules.RiskModerate),
		item(t, e, "Library/Caches/com.example.tool", rules.RiskModerate),
	)
	app := ""
	for _, it := range p.Items {
		if it.Path == homePath(e, "Programs/Tool.app") && it.Action == rules.ActionQuarantine {
			app = it.ID
		}
	}
	if app == "" || len(p.Items) != 4 {
		t.Fatalf("plan items %+v", p.Items)
	}
	for i := range p.Items {
		if p.Items[i].ID != app {
			p.Items[i].Requires = app
		}
	}
	return p
}

var leftovers = []string{"Library/Application Support/com.example.tool", "Library/Caches/com.example.tool"}

// The app changed after planning, so it is not moved: its leftovers stay,
// and the command that requires it does not run.
func TestRequiresSkipsWhenTheAppWasNotMoved(t *testing.T) {
	t.Parallel()
	e := appHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	p := appPlan(t, e, marker)
	if err := os.WriteFile(homePath(e, "Programs/Tool.app/Contents/new"), []byte("update"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Moved) != 0 || len(res.Commands) != 0 || len(res.Failed) != 0 {
		t.Fatalf("moved %v, commands %+v, failed %+v", paths(res.Moved), res.Commands, res.Failed)
	}
	requireMissing(t, marker)
	for _, rel := range leftovers {
		requireExists(t, homePath(e, rel))
	}
	app := homePath(e, "Programs/Tool.app")
	var requires []string
	for _, s := range res.Skipped {
		switch {
		case s.Path == app && s.Reason == plan.ReasonChanged:
		case s.Reason == plan.ReasonRequires && strings.HasSuffix(s.Detail, "because "+app+" was not moved"):
			requires = append(requires, s.Path)
		default:
			t.Errorf("skip %+v", s)
		}
	}
	want := append(slices.Clone(leftovers), "Programs/Tool.app") // the leftovers and the command
	if len(requires) != len(want) {
		t.Errorf("skipped as requires: %v, want %v", requires, want)
	}
	if n := len(eventsOf(t, e, res.RunID, "skip")); n != 4 {
		t.Errorf("%d skips journaled, want 4", n)
	}
}

// The app was moved: the items that require it follow, even though they
// come before it in the plan, and the command runs after the moves.
func TestRequiresProceedsWhenTheAppWasMoved(t *testing.T) {
	t.Parallel()
	e := appHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	p := appPlan(t, e, marker)

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{homePath(e, "Programs/Tool.app"), homePath(e, leftovers[0]), homePath(e, leftovers[1])}
	if got := paths(res.Moved); !slices.Equal(got, want) {
		t.Errorf("moved %v, want %v (the app first)", got, want)
	}
	requireExists(t, marker)
	if len(res.Skipped) != 0 || len(res.Failed) != 0 || len(res.Commands) != 1 {
		t.Errorf("skipped %+v, failed %+v, commands %+v", res.Skipped, res.Failed, res.Commands)
	}
}

// An unselected app is not moved either, so nothing that requires it goes.
func TestRequiresAnUnselectedApp(t *testing.T) {
	t.Parallel()
	e := appHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	p := appPlan(t, e, marker)
	for i := range p.Items {
		p.Items[i].Selected = p.Items[i].Requires != "" // all but the app
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Moved) != 0 || len(res.Commands) != 0 || len(res.Skipped) != 3 {
		t.Errorf("moved %v, commands %+v, skipped %+v", paths(res.Moved), res.Commands, res.Skipped)
	}
	requireMissing(t, marker)
}

// The plan is the user's: without Requires, the leftovers and the command go
// even though the app changed.
func TestPlanWithoutRequiresIsAppliedAsItIs(t *testing.T) {
	t.Parallel()
	e := appHome(t)
	marker := filepath.Join(t.TempDir(), "ran")
	p := appPlan(t, e, marker)
	for i := range p.Items {
		p.Items[i].Requires = ""
	}
	if err := os.WriteFile(homePath(e, "Programs/Tool.app/Contents/new"), []byte("update"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(t.Context(), e, p, withCommands())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{homePath(e, leftovers[0]), homePath(e, leftovers[1])}
	if got := paths(res.Moved); !slices.Equal(got, want) {
		t.Errorf("moved %v, want %v", got, want)
	}
	requireExists(t, marker)
	requireExists(t, homePath(e, "Programs/Tool.app"))
}

// A Requires that names no item to move of the plan is refused before
// anything moves: a missing id, a command, or an item that has a Requires
// of its own.
func TestDanglingRequiresIsRefused(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(p *plan.Plan){
		"missing id": func(p *plan.Plan) { p.Items[0].Requires = "9999" },
		"a command":  func(p *plan.Plan) { p.Items[0].Requires = p.Items[3].ID },
		"a chain":    func(p *plan.Plan) { p.Items[0].Requires = p.Items[1].ID },
		"itself":     func(p *plan.Plan) { p.Items[0].Requires = p.Items[0].ID },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := appHome(t)
			marker := filepath.Join(t.TempDir(), "ran")
			p := appPlan(t, e, marker)
			// Items in path order: the two leftovers, the app's command, the app.
			if p.Items[0].Path != homePath(e, leftovers[0]) || p.Items[3].Action != rules.ActionQuarantine {
				t.Fatalf("plan order %v", paths(p.Items))
			}
			p.Items[3], p.Items[2] = p.Items[2], p.Items[3] // 2: the app, 3: the command
			change(p)

			res, err := Apply(t.Context(), e, p, withCommands())

			if err != nil {
				t.Fatalf("Apply: %v", err)
			}
			if len(res.Failed) != 1 || res.Failed[0].Item.Path != homePath(e, leftovers[0]) ||
				!strings.Contains(res.Failed[0].Error, "not an item of this plan to move") {
				t.Fatalf("failed %+v", res.Failed)
			}
			requireExists(t, homePath(e, leftovers[0]))
			for _, it := range res.Moved {
				if it.Path == homePath(e, leftovers[0]) {
					t.Error("the refused item was moved")
				}
			}
		})
	}
}

// Requires survives the plan file.
func TestRequiresInThePlanFile(t *testing.T) {
	t.Parallel()
	e := appHome(t)
	p := appPlan(t, e, filepath.Join(t.TempDir(), "ran"))
	var b strings.Builder
	if err := plan.Write(&b, p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), `"requires": "`) {
		t.Errorf("plan file lacks requires:\n%s", b.String())
	}
	back, err := plan.Read(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Items {
		if back.Items[i].Requires != p.Items[i].Requires {
			t.Errorf("item %s: requires %q, want %q", p.Items[i].ID, back.Items[i].Requires, p.Items[i].Requires)
		}
	}
}

// quarantinedRun moves ~/rel to quarantine in a run that started at, with
// the item claiming bytes, and returns the run id. It never expires
// anything itself.
func quarantinedRun(t *testing.T, e env.Env, rel string, bytes int64, at time.Time) string {
	t.Helper()
	it := item(t, e, rel, rules.RiskSafe)
	it.Bytes = bytes
	opts := DefaultOptions()
	opts.Now = func() time.Time { return at }
	opts.SkipExpire = true
	res, err := Apply(t.Context(), e, makePlan(t, e, it), opts)
	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("quarantine %s: %v, %+v", rel, err, res)
	}
	return res.RunID
}

// With SkipExpire, runs past their age and over the size cap stay in
// quarantine, and Freed counts nothing. Without it they expire.
func TestApplySkipExpireKeepsQuarantine(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"old/a": "a", "big/b": "b", "new/c": "c", "later/d": "d"})
	now := time.Now()
	old := quarantinedRun(t, e, "old", 1000, now.Add(-30*24*time.Hour))
	big := quarantinedRun(t, e, "big", 2*fsop.DefaultMaxBytes, now.Add(-time.Hour))

	opts := DefaultOptions()
	opts.SkipExpire = true
	res, err := Apply(t.Context(), e, makePlan(t, e, item(t, e, "new", rules.RiskSafe)), opts)

	if err != nil || len(res.Moved) != 1 {
		t.Fatalf("Apply: %v, %+v", err, res)
	}
	if res.Freed != 0 {
		t.Errorf("freed %d, want 0", res.Freed)
	}
	sizes, err := fsop.QuarantineSize(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, big, res.RunID} {
		if _, ok := sizes[id]; !ok {
			t.Errorf("run %s left quarantine", id)
		}
	}

	// The same without SkipExpire: both expire.
	res, err = Apply(t.Context(), e, makePlan(t, e, item(t, e, "later", rules.RiskSafe)), DefaultOptions())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Freed == 0 {
		t.Error("nothing expired without SkipExpire")
	}
	if sizes, err = fsop.QuarantineSize(e); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{old, big} {
		if _, ok := sizes[id]; ok {
			t.Errorf("run %s did not expire without SkipExpire", id)
		}
	}
}
