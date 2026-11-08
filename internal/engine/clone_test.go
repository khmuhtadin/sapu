package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// cloneHome has a file and a copy of it, and another file of the same length.
func cloneHome(t *testing.T) env.Env {
	t.Helper()
	return newHome(t, map[string]string{
		"Downloads/a.pdf":      "same content",
		"Documents/copy-a.pdf": "same content",
		"Documents/other.pdf":  "other conten",
	})
}

// cloneOf is the item sapu dupes --replace-with-clone plans for ~/extra,
// whose kept copy is ~/keep.
func cloneOf(t *testing.T, e env.Env, extra, keep string) plan.Item {
	t.Helper()
	it := item(t, e, extra, rules.RiskSafe)
	id, err := plan.IdentityOf(homePath(e, keep))
	if err != nil {
		t.Fatal(err)
	}
	it.KeepCopy = &plan.KeepCopy{Path: homePath(e, keep), Identity: id}
	it.CloneFrom = homePath(e, keep)
	return it
}

func TestApplyReplacesDuplicateWithClone(t *testing.T) {
	t.Parallel()
	e := cloneHome(t)
	extra := homePath(e, "Documents/copy-a.pdf")
	orig, err := plan.IdentityOf(extra)
	if err != nil {
		t.Fatal(err)
	}
	p := makePlan(t, e, cloneOf(t, e, "Documents/copy-a.pdf", "Downloads/a.pdf"))

	res, err := Apply(t.Context(), e, p, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || len(res.Cloned) != 1 || len(res.Failed) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("result %+v, want one item moved and cloned", res)
	}
	clone, err := plan.IdentityOf(extra)
	if err != nil {
		t.Fatal(err)
	}
	if clone.Ino == orig.Ino || clone.MtimeNs != orig.MtimeNs || clone.Mode != orig.Mode || readFile(t, extra) != "same content" {
		t.Fatalf("clone %+v, original %+v: want a new file that looks the same", clone, orig)
	}
	if res.Bytes != 1000 {
		t.Errorf("bytes = %d, want the moved item's 1000", res.Bytes)
	}

	if _, err := fsop.Restore(e, res.RunID); err != nil {
		t.Fatal(err)
	}
	back, err := plan.IdentityOf(extra)
	if err != nil {
		t.Fatal(err)
	}
	if back != orig {
		t.Fatalf("after undo %+v, want the original %+v", back, orig)
	}
}

// A plan edited by hand could point a clone's source anywhere. Apply refuses
// any CloneFrom that is not exactly the item's KeepCopy.Path.
func TestApplyRefusesCloneFromWithoutMatchingKeepCopy(t *testing.T) {
	t.Parallel()
	cases := map[string]func(e env.Env, it *plan.Item){
		"no kept copy": func(_ env.Env, it *plan.Item) { it.KeepCopy = nil },
		"source is another file": func(e env.Env, it *plan.Item) {
			it.CloneFrom = homePath(e, "Documents/other.pdf")
		},
		"kept copy is another file": func(e env.Env, it *plan.Item) {
			id, _ := plan.IdentityOf(homePath(e, "Documents/other.pdf"))
			it.KeepCopy = &plan.KeepCopy{Path: homePath(e, "Documents/other.pdf"), Identity: id}
		},
		"not a quarantine item": func(_ env.Env, it *plan.Item) {
			it.Action = rules.ActionOfficialCommand
			it.Command = []string{"true"}
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := cloneHome(t)
			p := makePlan(t, e, cloneOf(t, e, "Documents/copy-a.pdf", "Downloads/a.pdf"))
			tamper(e, &p.Items[0])
			before := map[string]string{}
			for _, rel := range []string{"Downloads/a.pdf", "Documents/copy-a.pdf", "Documents/other.pdf"} {
				before[rel] = readFile(t, homePath(e, rel))
			}
			id, err := plan.IdentityOf(homePath(e, "Documents/copy-a.pdf"))
			if err != nil {
				t.Fatal(err)
			}

			opts := DefaultOptions()
			opts.RunCommands = true
			res, err := Apply(t.Context(), e, p, opts)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Failed) != 1 || !strings.Contains(res.Failed[0].Error, "clone_from") {
				t.Fatalf("failed %+v, want the item refused", res.Failed)
			}
			if len(res.Moved) != 0 || len(res.Cloned) != 0 || len(res.Commands) != 0 || res.RunID != "" {
				t.Fatalf("result %+v, want nothing done", res)
			}
			for rel, data := range before {
				if got := readFile(t, homePath(e, rel)); got != data {
					t.Errorf("%s changed to %q", rel, got)
				}
			}
			if now, err := plan.IdentityOf(homePath(e, "Documents/copy-a.pdf")); err != nil || now != id {
				t.Errorf("the item was touched: %+v, %v", now, err)
			}
		})
	}
}

// The kept copy changed after planning: the extra stays where it is.
func TestApplyCloneSkippedWhenKeptCopyChanged(t *testing.T) {
	t.Parallel()
	e := cloneHome(t)
	p := makePlan(t, e, cloneOf(t, e, "Documents/copy-a.pdf", "Downloads/a.pdf"))
	if err := os.WriteFile(homePath(e, "Downloads/a.pdf"), []byte("edited after planning"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(t.Context(), e, p, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != plan.ReasonChanged || len(res.Moved) != 0 || len(res.Cloned) != 0 {
		t.Fatalf("result %+v, want the item skipped as changed", res)
	}
	if readFile(t, homePath(e, "Documents/copy-a.pdf")) != "same content" {
		t.Fatal("the extra was touched")
	}
}

// The clone cannot be made (here: the kept copy cannot be read, so
// clonefile refuses). The item stays in quarantine, is reported as moved and
// failed, and --permanent keeps the run so that undo still works.
func TestApplyCloneNotPlacedIsMovedFailedAndKept(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("root can read any file")
	}
	e := cloneHome(t)
	keep := homePath(e, "Downloads/a.pdf")
	if err := os.Chmod(keep, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(keep, 0o644) })
	extra := homePath(e, "Documents/copy-a.pdf")
	orig, err := plan.IdentityOf(extra)
	if err != nil {
		t.Fatal(err)
	}
	p := makePlan(t, e, cloneOf(t, e, "Documents/copy-a.pdf", "Downloads/a.pdf"))
	opts := DefaultOptions()
	opts.Permanent = true

	res, err := Apply(t.Context(), e, p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || len(res.Cloned) != 0 || len(res.Failed) != 1 {
		t.Fatalf("result %+v, want the item moved and failed, not cloned", res)
	}
	if !strings.HasPrefix(res.Failed[0].Error, fsop.ErrNotCloned.Error()) {
		t.Errorf("error %q, want it to start with %q", res.Failed[0].Error, fsop.ErrNotCloned)
	}
	if res.Freed != 0 {
		t.Errorf("freed = %d: the quarantine was emptied although the clone was not placed", res.Freed)
	}
	requireMissing(t, extra)
	q := filepath.Join(e.QuarantineDir(), res.RunID, p.Items[0].ID+"-copy-a.pdf")
	if id, err := plan.IdentityOf(q); err != nil || id != orig {
		t.Fatalf("quarantined item %+v, %v; want the original", id, err)
	}

	if _, err := fsop.Restore(e, res.RunID); err != nil {
		t.Fatal(err)
	}
	if id, err := plan.IdentityOf(extra); err != nil || id != orig {
		t.Fatalf("after undo %+v, %v; want the original back", id, err)
	}
}

// --permanent empties the quarantine as usual; the clones stay.
func TestApplyPermanentKeepsClones(t *testing.T) {
	t.Parallel()
	e := cloneHome(t)
	p := makePlan(t, e, cloneOf(t, e, "Documents/copy-a.pdf", "Downloads/a.pdf"))
	opts := DefaultOptions()
	opts.Permanent = true

	res, err := Apply(t.Context(), e, p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cloned) != 1 || res.Freed != 1000 {
		t.Fatalf("result %+v, want one clone and 1000 bytes freed", res)
	}
	requireMissing(t, filepath.Join(e.QuarantineDir(), res.RunID))
	if readFile(t, homePath(e, "Documents/copy-a.pdf")) != "same content" || readFile(t, homePath(e, "Downloads/a.pdf")) != "same content" {
		t.Fatal("the clone or the kept copy changed")
	}
	if got := runStatus(t, e, res.RunID); got != "emptied" {
		t.Fatalf("status %q, want emptied", got)
	}
}
