package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

var dupesExtras = []string{"Documents/work/report-final.pdf", "Desktop/report copy.pdf", "Pictures/b2.bin"}

func identities(t *testing.T, e env.Env, rels []string) map[string]plan.Identity {
	t.Helper()
	out := map[string]plan.Identity{}
	for _, rel := range rels {
		id, err := plan.IdentityOf(filepath.Join(e.Home, rel))
		if err != nil {
			t.Fatal(err)
		}
		out[rel] = id
	}
	return out
}

// oneLine joins the words of out, so a sentence wrapped over lines can be found.
func oneLine(out string) string {
	return strings.Join(strings.Fields(out), " ")
}

func TestDupesReplaceWithCloneKeepsFilesAndUndoRestores(t *testing.T) {
	e := newDupesHome(t)
	before := snapshot(t, e)
	origs := identities(t, e, dupesExtras)

	out, errOut, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--replace-with-clone", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu dupes --replace-with-clone --yes\n%s", out)
	for _, text := range []string{
		"clone ~/Documents/work/report-final.pdf", "clone ~/Desktop/report copy.pdf",
		dupesReplaceNote,
		"Moved 3 items", "Clones placed (3)", "(clone of ~/Downloads/report.pdf)",
		"The quarantined originals still use space until the quarantine is emptied.",
	} {
		if !strings.Contains(oneLine(out), text) {
			t.Errorf("output lacks %q:\n%s", text, out)
		}
	}
	if strings.Contains(out, "Clones not placed") {
		t.Errorf("a clone was not placed:\n%s", out)
	}
	// The table and the clone lists fit 80 columns (the result lines between
	// them are writeResult's, shared with every command).
	table, rest, _ := strings.Cut(out, "Moved 3 items")
	_, clones, _ := strings.Cut(rest, "Clones placed")
	checkHuman(t, table)
	checkHuman(t, clones)

	// Every file is where it was, with the same mode and content; the
	// extras are new files (the clones).
	sameSnapshot(t, before, snapshot(t, e))
	for rel, id := range identities(t, e, dupesExtras) {
		if id.Ino == origs[rel].Ino {
			t.Errorf("%s was not replaced", rel)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
	for rel, id := range identities(t, e, dupesExtras) {
		if id != origs[rel] {
			t.Errorf("%s after undo is %+v, want the original %+v", rel, id, origs[rel])
		}
	}
}

func TestDupesReplaceWithCloneAsksFirst(t *testing.T) {
	e := newDupesHome(t)
	before := snapshot(t, e)
	out, errOut, code := sapu(t, e, "n\n", "dupes", "--min-size", "1KB", "--replace-with-clone")
	if code != 2 {
		t.Fatalf("answer n: exit %d, want 2", code)
	}
	if !strings.Contains(oneLine(out), dupesReplaceNote) {
		t.Errorf("the explanation is not shown before the question:\n%s", out)
	}
	if !strings.Contains(errOut, "Replace 3 extra copies") || !strings.Contains(errOut, "by clones of the kept files? The originals go to quarantine.") {
		t.Errorf("question: %q", errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestDupesReplaceWithClonePlanAndJSON(t *testing.T) {
	e := newDupesHome(t)
	file, p := dupesPlan(t, e, "--replace-with-clone")
	noState(t, e)
	check(t, "plan", p)
	if len(p.Items) != 3 {
		t.Fatalf("items %+v", p.Items)
	}
	for _, it := range p.Items {
		if it.KeepCopy == nil || it.CloneFrom != it.KeepCopy.Path {
			t.Errorf("item %s: clone_from %q, keep_copy %+v", it.Path, it.CloneFrom, it.KeepCopy)
		}
	}

	// sapu apply runs the clones of a saved plan too.
	out, errOut, code := sapu(t, e, "", "apply", file, "--yes", "--json")
	if code != 0 {
		t.Fatalf("apply: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "apply", out)
	var a runData
	decodeData(t, out, &a)
	if a.Result == nil || len(a.Result.Cloned) != 3 || len(a.Result.Moved) != 3 {
		t.Fatalf("apply result %+v", a.Result)
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}

	out, errOut, code = sapu(t, e, "", "dupes", "--min-size", "1KB", "--replace-with-clone", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "dupes", out)
	var d dupesData
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Cloned) != 3 || len(d.Result.Failed) != 0 {
		t.Fatalf("result %+v", d.Result)
	}
	for _, it := range d.Result.Cloned {
		if it.CloneFrom != filepath.Join(e.Home, "Downloads/report.pdf") && it.CloneFrom != filepath.Join(e.Home, "Documents/b1.bin") {
			t.Errorf("cloned %s from %s", it.Path, it.CloneFrom)
		}
	}
}

// A clone that could not be placed (a volume that is not APFS, a file that
// appeared at the path) is listed with the way back.
func TestWriteClonesListsClonesNotPlaced(t *testing.T) {
	home := "/home/x"
	a := plan.Item{ID: "0001", Path: home + "/Documents/a.pdf", CloneFrom: home + "/Downloads/a.pdf"}
	b := plan.Item{ID: "0002", Path: home + "/Documents/b.pdf", CloneFrom: home + "/Downloads/b.pdf"}
	res := &engine.Result{RunID: "20261108-111500-a3f9", Moved: []plan.Item{a, b}, Cloned: []plan.Item{a}}
	var out bytes.Buffer
	writeClones(&out, home, res, false)
	t.Logf("\n%s", out.String())
	for _, text := range []string{
		"Clones placed (1)", "~/Documents/a.pdf (clone of ~/Downloads/a.pdf)",
		"Clones not placed (1)", "~/Documents/b.pdf They are in quarantine with nothing in their place. To put them back: `sapu undo 20261108-111500-a3f9`",
		"The quarantined originals still use space until the quarantine is emptied.",
	} {
		if !strings.Contains(oneLine(out.String()), text) {
			t.Errorf("output lacks %q", text)
		}
	}
	checkHuman(t, out.String())
}

// sapu apply of a plan made with dupes --replace-with-clone lists the clones
// like dupes does; a plan without clones prints no such list.
func TestApplyShowsClones(t *testing.T) {
	e := newDupesHome(t)
	file, _ := dupesPlan(t, e, "--replace-with-clone")
	out, errOut, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 0 {
		t.Fatalf("apply: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, text := range []string{
		"Moved 3 items", "Clones placed (3)", "(clone of ~/Downloads/report.pdf)",
		"The quarantined originals still use space until the quarantine is emptied.",
	} {
		if !strings.Contains(oneLine(out), text) {
			t.Errorf("apply output lacks %q:\n%s", text, out)
		}
	}
	if strings.Contains(out, "Clones not placed") {
		t.Errorf("a clone was not placed:\n%s", out)
	}

	plain := newHome(t)
	out, _, _ = sapu(t, plain, "", "clean", "--yes")
	if strings.Contains(out, "Clones") {
		t.Errorf("clean mentions clones:\n%s", out)
	}
}

// A clone that could not be placed is in Moved (the original is in
// quarantine) and in Failed; the list says so and how to undo.
func TestWriteResultListsClonesNotPlaced(t *testing.T) {
	placed := plan.Item{ID: "a", Path: "/h/a.bin", CloneFrom: "/h/keep.bin"}
	lost := plan.Item{ID: "b", Path: "/h/b.bin", CloneFrom: "/h/keep.bin"}
	res := &engine.Result{
		RunID:  "r1",
		Moved:  []plan.Item{placed, lost},
		Cloned: []plan.Item{placed},
		Failed: []engine.Failure{{Item: lost, Error: "clone failed"}},
		Bytes:  20,
	}
	var buf strings.Builder
	writeResult(&buf, "/h", res, config.Defaults(), time.Now(), false, false) // "/h" does not exist, so the free space line says so
	out := buf.String()
	for _, text := range []string{
		"Clones placed (1)", "~/a.bin (clone of ~/keep.bin)",
		"Clones not placed (1)", "~/b.bin", "To put them back: `sapu undo r1`",
		"Failed (1)",
	} {
		if !strings.Contains(out, text) {
			t.Errorf("result lacks %q:\n%s", text, out)
		}
	}
}
