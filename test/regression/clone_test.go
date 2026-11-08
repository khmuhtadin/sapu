package regression

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/dupes"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// cloneExtras are the copies dupes finds in dupesHome; the shorter path of
// each pair stays.
var cloneExtras = []string{"Desktop/report copy.pdf", "Pictures/Trip/photo copy.jpg"}

// dupesHome has two pairs of duplicates next to files that must not change.
// The extras have their own mode and mtime. They have no xattrs: a clone
// gets the xattrs of the kept copy, not those of the file it replaces.
func dupesHome(t *testing.T) env.Env {
	t.Helper()
	report := strings.Repeat("%PDF report ", 90_000) // about 1 MB
	photo := strings.Repeat("JPEG", 20_000)
	e := newHome(t, map[string]string{
		"Documents/report.pdf":         report,
		"Desktop/report copy.pdf":      report,
		"Pictures/photo.jpg":           photo,
		"Pictures/Trip/photo copy.jpg": photo,
		"Documents/notes.txt":          "notes",
		"Library/Caches/app/cache.db":  report, // never searched
	})
	setXattr(t, homePath(e, "Documents/report.pdf"), "com.example.kept", "kept copy")
	setXattr(t, homePath(e, "Documents/notes.txt"), "com.example.where-from", "https://example.com")
	fixTimes(t, e.Home)
	setXattr(t, homePath(e, "Desktop/report copy.pdf"), "com.example.kept", "kept copy") // same as its kept copy
	chmod(t, homePath(e, "Desktop/report copy.pdf"), 0o600)
	chmod(t, homePath(e, "Pictures/Trip/photo copy.jpg"), 0o444)
	mt := fixtureTime.Add(36*time.Hour + 123456789)
	if err := os.Chtimes(homePath(e, "Desktop/report copy.pdf"), mt, mt); err != nil {
		t.Fatal(err)
	}
	return e
}

// clonePlan is what sapu dupes --replace-with-clone plans.
func clonePlan(t *testing.T, e env.Env) *plan.Plan {
	t.Helper()
	groups, err := dupes.Find(t.Context(), e, dupes.DefaultRoots(e), dupes.Options{MinSize: 1, Keep: dupes.KeepShortestPath})
	if err != nil {
		t.Fatal(err)
	}
	p, err := engine.Plan(t.Context(), e, plan.KindDupes, dupes.Items(groups))
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Items {
		p.Items[i].Selected = true
		p.Items[i].CloneFrom = p.Items[i].KeepCopy.Path
	}
	return p
}

// Replacing duplicates by clones changes no file's name, content, mode or
// mtime, and undo puts back exactly the files that were there.
func TestReplaceWithCloneThenUndoRestoresIdentically(t *testing.T) {
	t.Parallel()
	e := dupesHome(t)
	before := snapHome(t, e)
	p := clonePlan(t, e)
	if len(p.Items) != len(cloneExtras) {
		t.Fatalf("planned %d items, want %d", len(p.Items), len(cloneExtras))
	}

	res := applyPlan(t, e, p, engine.DefaultOptions())

	if len(res.Cloned) != len(cloneExtras) || len(res.Failed) != 0 {
		t.Fatalf("cloned %d, failed %+v; want every extra cloned", len(res.Cloned), res.Failed)
	}
	for _, rel := range cloneExtras {
		if !inItems(res.Cloned, homePath(e, rel)) {
			t.Errorf("~/%s was not cloned", rel)
		}
	}
	parents := parentDirs(t, e, res.Moved)
	want := before
	want.clearMtime(parents...)
	got := snapHome(t, e)
	got.clearMtime(parents...)
	requireSameTree(t, "home with clones", want, got)

	restored, err := fsop.Restore(e, res.RunID)
	if err != nil {
		t.Fatalf("fsop.Restore: %v", err)
	}
	if len(restored) != len(cloneExtras) {
		t.Errorf("restored %d items, want %d", len(restored), len(cloneExtras))
	}
	for _, r := range restored {
		if r.Conflict {
			t.Errorf("unexpected conflict restoring %s", display(e, r.To))
		}
	}
	after := snapHome(t, e)
	after.clearMtime(parents...)
	requireSameTree(t, "home after undo", want, after)
	requireRunQuarantineEmpty(t, e, res.RunID)
}

// --permanent deletes the quarantined originals; the clones and the kept
// copies stay exactly as they were.
func TestReplaceWithClonePermanentKeepsEveryFile(t *testing.T) {
	t.Parallel()
	e := dupesHome(t)
	before := snapHome(t, e)
	opts := engine.DefaultOptions()
	opts.Permanent = true

	res := applyPlan(t, e, clonePlan(t, e), opts)

	if len(res.Cloned) != len(cloneExtras) || res.Freed <= 0 {
		t.Fatalf("cloned %d, freed %d", len(res.Cloned), res.Freed)
	}
	requireRunQuarantineEmpty(t, e, res.RunID)
	parents := parentDirs(t, e, res.Moved)
	want := before
	want.clearMtime(parents...)
	got := snapHome(t, e)
	got.clearMtime(parents...)
	requireSameTree(t, "home after --permanent", want, got)
}
