package engine

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// newHome creates a fake home holding files (path relative to the home ->
// contents). A path ending in "/" is an empty folder.
func newHome(t *testing.T, files map[string]string) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	for rel, data := range files {
		p := filepath.Join(e.Home, filepath.FromSlash(rel))
		if strings.HasSuffix(rel, "/") {
			mkdirAll(t, p)
			continue
		}
		mkdirAll(t, filepath.Dir(p))
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func homePath(e env.Env, rel string) string {
	return filepath.Join(e.Home, filepath.FromSlash(rel))
}

// item is what scan would produce for ~/rel.
func item(t *testing.T, e env.Env, rel string, risk rules.Risk) plan.Item {
	t.Helper()
	p := homePath(e, rel)
	id, err := plan.IdentityOf(p)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Item{
		Source:      "test.rule",
		Path:        p,
		Bytes:       1000,
		Risk:        risk,
		Regenerates: true,
		Explain:     "Test item.",
		Action:      rules.ActionQuarantine,
		Identity:    id,
	}
}

func makePlan(t *testing.T, e env.Env, items ...plan.Item) *plan.Plan {
	t.Helper()
	p, err := Plan(t.Context(), e, plan.KindClean, items)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

func requireExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("want %s to exist: %v", p, err)
	}
}

func requireMissing(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("want %s gone (lstat err: %v)", p, err)
	}
}

func paths(items []plan.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Path)
	}
	return out
}

func ids(items []plan.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func runStatus(t *testing.T, e env.Env, runID string) string {
	t.Helper()
	runs, err := runlog.List(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.ID == runID {
			return r.Status
		}
	}
	t.Fatalf("run %s not listed", runID)
	return ""
}

func TestPlanNumbersInPathOrderAndDropsDuplicates(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/a/inner/f": "f",
		"Library/Caches/a-x/f":     "f",
		"Library/Caches/b/f":       "f",
		"Library/Caches/c.db":      "c",
	})
	// "a-x" sorts between "a" and "a/inner", so a duplicate is not always
	// next to the item that covers it.
	given := []plan.Item{
		item(t, e, "Library/Caches/c.db", rules.RiskSafe),
		item(t, e, "Library/Caches/a/inner", rules.RiskSafe),
		item(t, e, "Library/Caches/b", rules.RiskSafe),
		item(t, e, "Library/Caches/a", rules.RiskSafe),
		item(t, e, "Library/Caches/a-x", rules.RiskSafe),
		item(t, e, "Library/Caches/a", rules.RiskModerate),
	}
	original := slices.Clone(given)

	p := makePlan(t, e, given...)

	wantPaths := []string{
		homePath(e, "Library/Caches/a"),
		homePath(e, "Library/Caches/a-x"),
		homePath(e, "Library/Caches/b"),
		homePath(e, "Library/Caches/c.db"),
	}
	if got := paths(p.Items); !slices.Equal(got, wantPaths) {
		t.Errorf("plan items %v, want %v", got, wantPaths)
	}
	if got, want := ids(p.Items), []string{"0001", "0002", "0003", "0004"}; !slices.Equal(got, want) {
		t.Errorf("ids %v, want %v", got, want)
	}
	if p.Items[0].Risk != rules.RiskSafe {
		t.Errorf("of two items with the same path, the first given must stay; got risk %s", p.Items[0].Risk)
	}
	covered := fmt.Sprintf("covered by item 0001 (%s)", homePath(e, "Library/Caches/a"))
	wantSkipped := []plan.Skip{
		{Path: homePath(e, "Library/Caches/a"), Source: "test.rule", Reason: plan.ReasonDuplicate, Detail: covered},
		{Path: homePath(e, "Library/Caches/a/inner"), Source: "test.rule", Reason: plan.ReasonDuplicate, Detail: covered},
	}
	if !slices.Equal(p.Skipped, wantSkipped) {
		t.Errorf("skipped %+v, want %+v", p.Skipped, wantSkipped)
	}
	if !slices.EqualFunc(given, original, func(a, b plan.Item) bool { return a.Path == b.Path && a.ID == b.ID }) {
		t.Error("Plan changed the caller's slice")
	}
	if p.SchemaVersion != plan.SchemaVersion || p.Kind != plan.KindClean || p.Home != e.Home || p.CreatedAt.IsZero() {
		t.Errorf("plan header: version %d, kind %q, home %q, created %v", p.SchemaVersion, p.Kind, p.Home, p.CreatedAt)
	}
}

func TestPlanSelectsSafeAndModerate(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"safe": "s", "moderate": "m", "risky": "r"})
	given := []plan.Item{
		item(t, e, "safe", rules.RiskSafe),
		item(t, e, "moderate", rules.RiskModerate),
		item(t, e, "risky", rules.RiskRisky),
	}
	given[0].Selected = false // whatever the producer set is replaced
	given[2].Selected = true

	p := makePlan(t, e, given...)

	want := map[rules.Risk]bool{rules.RiskSafe: true, rules.RiskModerate: true, rules.RiskRisky: false}
	if len(p.Items) != 3 {
		t.Fatalf("plan has %d items, want 3", len(p.Items))
	}
	for _, it := range p.Items {
		if it.Selected != want[it.Risk] {
			t.Errorf("%s item: selected=%v, want %v", it.Risk, it.Selected, want[it.Risk])
		}
	}
}

func TestPlanSkipDetails(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		".claude/settings.json":      "{}",
		"Documents/keep.txt":         "keep",
		"Library/Caches/open/a.db":   "open",
		"Library/Caches/recent/a.db": "recent",
		"Library/Caches/junk/a.db":   "junk",
	})
	if err := protect.AddWhitelist(e, "~/Documents/keep.txt"); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(homePath(e, "Library/Caches/open/a.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	recent := item(t, e, "Library/Caches/recent", rules.RiskSafe)
	recent.ProtectIf = []rules.Condition{{ModifiedWithin: "1d"}}

	p := makePlan(t, e,
		item(t, e, ".claude/settings.json", rules.RiskSafe),
		item(t, e, "Documents/keep.txt", rules.RiskSafe),
		item(t, e, "Library/Caches/open", rules.RiskSafe),
		recent,
		item(t, e, "Library/Caches/junk", rules.RiskSafe),
	)

	if got, want := paths(p.Items), []string{homePath(e, "Library/Caches/junk")}; !slices.Equal(got, want) {
		t.Errorf("plan items %v, want only %v", got, want)
	}
	want := map[string]struct{ reason, detail string }{
		".claude/settings.json": {plan.ReasonProtected, "protected:claude"},
		"Documents/keep.txt":    {plan.ReasonWhitelisted, "whitelisted:~/Documents/keep.txt"},
		"Library/Caches/open":   {plan.ReasonInUse, fmt.Sprintf("(pid %d)", os.Getpid())},
		"Library/Caches/recent": {plan.ReasonInUse, "changed within 1d"},
	}
	if len(p.Skipped) != len(want) {
		t.Errorf("skipped %+v, want %d entries", p.Skipped, len(want))
	}
	for rel, w := range want {
		n := 0
		for _, s := range p.Skipped {
			if s.Path != homePath(e, rel) {
				continue
			}
			n++
			if s.Reason != w.reason || !strings.Contains(s.Detail, w.detail) {
				t.Errorf("~/%s skipped as %q (%q), want %q with %q in the detail", rel, s.Reason, s.Detail, w.reason, w.detail)
			}
		}
		if n != 1 {
			t.Errorf("~/%s is skipped %d times, want once", rel, n)
		}
	}
}

func TestPlanCancelled(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/junk/a.db": "junk"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p, err := Plan(ctx, e, plan.KindClean, []plan.Item{item(t, e, "Library/Caches/junk", rules.RiskSafe)})

	if !errors.Is(err, context.Canceled) || p != nil {
		t.Errorf("Plan on a cancelled context: plan %v, err %v; want nil and context.Canceled", p, err)
	}
}

func TestApplyMovesInOrderAndReportsProgress(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/a/f": "a",
		"Library/Caches/b/f": "b",
		"Library/Caches/c/f": "c",
		"Library/Caches/d/f": "d",
	})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/a", rules.RiskSafe),
		item(t, e, "Library/Caches/b", rules.RiskRisky),
		item(t, e, "Library/Caches/c", rules.RiskSafe),
		item(t, e, "Library/Caches/d", rules.RiskModerate),
	)
	var calls []string
	opts := DefaultOptions()
	opts.Progress = func(done, total int, it plan.Item) {
		calls = append(calls, fmt.Sprintf("%d/%d %s", done, total, it.ID))
	}

	res, err := Apply(t.Context(), e, p, opts)

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"1/3 0001", "2/3 0003", "3/3 0004"}; !slices.Equal(calls, want) {
		t.Errorf("progress calls %v, want %v", calls, want)
	}
	if got, want := ids(res.Moved), []string{"0001", "0003", "0004"}; !slices.Equal(got, want) {
		t.Errorf("moved %v, want %v", got, want)
	}
	if res.Bytes != 3000 || res.Freed != 0 {
		t.Errorf("bytes %d, freed %d; want 3000 and 0", res.Bytes, res.Freed)
	}
	if len(res.Skipped)+len(res.Failed)+len(res.Suggested) != 0 {
		t.Errorf("unexpected skips %v, failures %v or suggestions %v", res.Skipped, res.Failed, res.Suggested)
	}
	requireExists(t, homePath(e, "Library/Caches/b/f"))
	requireMissing(t, homePath(e, "Library/Caches/a"))
	if s := runStatus(t, e, res.RunID); s != runlog.StatusDone {
		t.Errorf("run status %q, want %q", s, runlog.StatusDone)
	}
}

// Cancelling stops between items, ends the run cleanly, keeps what moved in
// quarantine (even with Permanent) and returns the partial result.
func TestApplyCancelBetweenItems(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/a/f": "a",
		"Library/Caches/b/f": "b",
		"Library/Caches/c/f": "c",
	})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/a", rules.RiskSafe),
		item(t, e, "Library/Caches/b", rules.RiskSafe),
		item(t, e, "Library/Caches/c", rules.RiskSafe),
	)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	opts := DefaultOptions()
	opts.Permanent = true
	opts.Progress = func(done, total int, it plan.Item) {
		if done == 1 {
			cancel()
		}
	}

	res, err := Apply(ctx, e, p, opts)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply err %v, want context.Canceled", err)
	}
	if res == nil {
		t.Fatal("Apply returned no result on cancel")
	}
	if got, want := ids(res.Moved), []string{"0001"}; !slices.Equal(got, want) {
		t.Errorf("moved %v, want %v", got, want)
	}
	if res.Freed != 0 {
		t.Errorf("freed %d after a cancel, want 0: nothing is deleted after the user stops", res.Freed)
	}
	requireExists(t, filepath.Join(e.QuarantineDir(), res.RunID, "0001-a", "f"))
	requireExists(t, homePath(e, "Library/Caches/b/f"))
	requireExists(t, homePath(e, "Library/Caches/c/f"))
	if s := runStatus(t, e, res.RunID); s != runlog.StatusDone {
		t.Errorf("run status %q, want %q (closed with its end event)", s, runlog.StatusDone)
	}
}

// Freed counts expired runs and this run's quarantine when Permanent is set.
func TestApplyPermanentFreed(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/old/f": "old",
		"Library/Caches/new/f": "new",
	})
	old := makePlan(t, e, item(t, e, "Library/Caches/old", rules.RiskSafe))
	past := DefaultOptions()
	past.Now = func() time.Time { return time.Now().Add(-10 * 24 * time.Hour) }
	first, err := Apply(t.Context(), e, old, past)
	if err != nil || len(first.Moved) != 1 {
		t.Fatalf("first Apply: moved %v, err %v", first, err)
	}

	newItem := item(t, e, "Library/Caches/new", rules.RiskSafe)
	newItem.Bytes = 2000
	p := makePlan(t, e, newItem)
	opts := DefaultOptions()
	opts.Permanent = true

	res, err := Apply(t.Context(), e, p, opts)

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Bytes != 2000 || res.Freed != 3000 {
		t.Errorf("bytes %d, freed %d; want 2000 moved and 3000 freed (1000 expired + 2000 permanent)", res.Bytes, res.Freed)
	}
	requireMissing(t, filepath.Join(e.QuarantineDir(), first.RunID))
	requireMissing(t, filepath.Join(e.QuarantineDir(), res.RunID))
	for _, id := range []string{first.RunID, res.RunID} {
		if s := runStatus(t, e, id); s != runlog.StatusEmptied {
			t.Errorf("run %s status %q, want %q", id, s, runlog.StatusEmptied)
		}
	}
}

// A plan Apply must not accept is refused before anything is written.
func TestApplyRefusesBeforeWriting(t *testing.T) {
	t.Parallel()
	cases := map[string]func(p *plan.Plan, opts *Options){
		"schema version": func(p *plan.Plan, _ *Options) { p.SchemaVersion = 2 },
		"other home":     func(p *plan.Plan, _ *Options) { p.Home += "-other" },
		"zero options":   func(_ *plan.Plan, opts *Options) { *opts = Options{} },
		"no max age":     func(_ *plan.Plan, opts *Options) { opts.MaxAge = 0 },
		"no max bytes":   func(_ *plan.Plan, opts *Options) { opts.MaxBytes = 0 },
		// Found by the second independent review (Nov 2026): a plan dated in
		// the future slipped past the changed-contents check.
		"future plan": func(p *plan.Plan, _ *Options) { p.CreatedAt = time.Now().Add(24 * time.Hour) },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := newHome(t, map[string]string{"Library/Caches/junk/a.db": "junk"})
			p := makePlan(t, e, item(t, e, "Library/Caches/junk", rules.RiskSafe))
			opts := DefaultOptions()
			edit(p, &opts)

			res, err := Apply(t.Context(), e, p, opts)

			if err == nil || res != nil {
				t.Errorf("Apply accepted the plan: result %+v, err %v", res, err)
			}
			requireMissing(t, e.StateDir)
			requireExists(t, homePath(e, "Library/Caches/junk/a.db"))
		})
	}
}

// With nothing to move, Apply runs expiry only: no run is created.
func TestApplyNothingToMove(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/risky/f": "r",
		"Library/Containers/vm/": "",
	})
	official := item(t, e, "Library/Containers/vm", rules.RiskSafe)
	official.Action = rules.ActionOfficialCommand
	official.Command = []string{"/usr/bin/true"}
	p := makePlan(t, e, item(t, e, "Library/Caches/risky", rules.RiskRisky), official)

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.RunID != "" {
		t.Errorf("run %s created with nothing to move", res.RunID)
	}
	if got, want := paths(res.Suggested), []string{official.Path}; !slices.Equal(got, want) {
		t.Errorf("suggested %v, want %v", got, want)
	}
	if len(res.Moved) != 0 {
		t.Errorf("moved %v, want nothing", paths(res.Moved))
	}
	requireMissing(t, e.StateDir)
}

// A plan file edited by hand may hold an action Apply does not know. It is
// never moved and is reported as a failure.
func TestApplyUnknownActionFails(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{"Library/Caches/junk/a.db": "junk"})
	p := makePlan(t, e, item(t, e, "Library/Caches/junk", rules.RiskSafe))
	p.Items[0].Action = "trash"

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Error, `unknown action "trash"`) {
		t.Errorf("failed %+v, want the item with an unknown action", res.Failed)
	}
	requireExists(t, homePath(e, "Library/Caches/junk/a.db"))
}

// Every skip during Apply is in the Result with a detail and in the journal.
func TestApplySkipsAreJournaled(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/gone/f":  "g",
		"Library/Caches/kept/f":  "k",
		"Library/Caches/moved/f": "m",
	})
	p := makePlan(t, e,
		item(t, e, "Library/Caches/gone", rules.RiskSafe),
		item(t, e, "Library/Caches/kept", rules.RiskSafe),
		item(t, e, "Library/Caches/moved", rules.RiskSafe),
	)
	if err := os.RemoveAll(homePath(e, "Library/Caches/gone")); err != nil {
		t.Fatal(err)
	}
	if err := protect.AddWhitelist(e, "~/Library/Caches/kept"); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(t.Context(), e, p, DefaultOptions())

	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []plan.Skip{
		{Path: homePath(e, "Library/Caches/gone"), Source: "test.rule", Reason: plan.ReasonGone},
		{Path: homePath(e, "Library/Caches/kept"), Source: "test.rule", Reason: plan.ReasonWhitelisted, Detail: "whitelisted:~/Library/Caches/kept"},
	}
	if len(res.Skipped) != len(want) {
		t.Fatalf("skipped %+v, want %+v", res.Skipped, want)
	}
	for i, w := range want {
		s := res.Skipped[i]
		if s.Path != w.Path || s.Source != w.Source || s.Reason != w.Reason || s.Detail == "" || (w.Detail != "" && s.Detail != w.Detail) {
			t.Errorf("skip %d: %+v, want %+v", i, s, w)
		}
	}
	if got, want := ids(res.Moved), []string{"0003"}; !slices.Equal(got, want) {
		t.Errorf("moved %v, want %v", got, want)
	}

	evs, err := runlog.Events(e, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var skips []string
	for _, ev := range evs {
		if ev.Type == runlog.EventSkip {
			skips = append(skips, ev.Item+" "+ev.Reason)
		}
	}
	wantSkips := []string{"0001 gone: " + res.Skipped[0].Detail, "0002 whitelisted: whitelisted:~/Library/Caches/kept"}
	if !slices.Equal(skips, wantSkips) {
		t.Errorf("journal skips %q, want %q", skips, wantSkips)
	}
}

func TestPlanSuggestionDoesNotHideItemsInside(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"code/wt/.git":                "gitdir: /elsewhere/.git/worktrees/wt",
		"code/wt/node_modules/x/f.js": "x",
	})
	suggestion := item(t, e, "code/wt", rules.RiskModerate)
	suggestion.Source = "purge:git-worktree"
	suggestion.Action = rules.ActionOfficialCommand
	suggestion.Command = []string{"git", "worktree", "remove", suggestion.Path}

	p := makePlan(t, e, suggestion, item(t, e, "code/wt/node_modules", rules.RiskSafe))

	want := []string{homePath(e, "code/wt"), homePath(e, "code/wt/node_modules")}
	if got := paths(p.Items); !slices.Equal(got, want) {
		t.Fatalf("plan items %v, want %v (skipped %+v)", got, want, p.Skipped)
	}
}

func TestPlanSkipsSapuStateFolder(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		".sapu/runs/x/journal.jsonl": "{}",
		".sapu/whitelist.txt":        "",
		"Library/Caches/a/f":         "f",
	})
	given := []plan.Item{
		item(t, e, ".sapu", rules.RiskSafe),
		item(t, e, ".sapu/runs", rules.RiskSafe),
		item(t, e, "Library/Caches/a", rules.RiskSafe),
	}
	upper := item(t, e, ".sapu/whitelist.txt", rules.RiskSafe)
	upper.Path = filepath.Join(e.Home, ".SAPU", "whitelist.txt") // APFS ignores case
	given = append(given, upper)

	p := makePlan(t, e, given...)

	if got, want := paths(p.Items), []string{homePath(e, "Library/Caches/a")}; !slices.Equal(got, want) {
		t.Fatalf("plan items %v, want %v", got, want)
	}
	if len(p.Skipped) != 3 {
		t.Fatalf("skipped %+v, want the 3 state folder items", p.Skipped)
	}
	for _, s := range p.Skipped {
		if s.Reason != plan.ReasonProtected || s.Detail != "protected:sapu-state" {
			t.Errorf("skip %+v, want protected:sapu-state", s)
		}
	}
}

func TestApplySkipsDuplicateWhoseKeptCopyChanged(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Documents/a.pdf":      "same",
		"Documents/copy-a.pdf": "same",
		"Documents/b.pdf":      "same2",
		"Documents/copy-b.pdf": "same2",
	})
	keep := func(rel string) *plan.KeepCopy {
		id, err := plan.IdentityOf(homePath(e, rel))
		if err != nil {
			t.Fatal(err)
		}
		return &plan.KeepCopy{Path: homePath(e, rel), Identity: id}
	}
	extraA := item(t, e, "Documents/copy-a.pdf", rules.RiskSafe)
	extraA.KeepCopy = keep("Documents/a.pdf")
	extraB := item(t, e, "Documents/copy-b.pdf", rules.RiskSafe)
	extraB.KeepCopy = keep("Documents/b.pdf")
	p := makePlan(t, e, extraA, extraB)

	// The kept copy of a is edited after planning; b's is untouched.
	if err := os.WriteFile(homePath(e, "Documents/a.pdf"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(t.Context(), e, p, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	requireExists(t, homePath(e, "Documents/copy-a.pdf"))
	requireMissing(t, homePath(e, "Documents/copy-b.pdf"))
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonChanged || res.Skipped[0].Path != homePath(e, "Documents/copy-a.pdf") {
		t.Fatalf("skipped %+v, want copy-a as changed", res.Skipped)
	}
}
