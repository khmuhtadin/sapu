package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// dupesFile writes a file and waits a little, so files written one after the
// other get birth times in that order.
func dupesFile(t *testing.T, e env.Env, rel string, data []byte) string {
	t.Helper()
	path := filepath.Join(e.Home, rel)
	writeFile(t, path, string(data))
	time.Sleep(5 * time.Millisecond)
	return path
}

// newDupesHome has three copies of one file (the oldest in Downloads), a
// second pair in Documents, files that only look alike, and a unique file.
func newDupesHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	a := bytes.Repeat([]byte("a"), 5000)
	dupesFile(t, e, "Downloads/report.pdf", a)
	dupesFile(t, e, "Documents/work/report-final.pdf", a)
	dupesFile(t, e, "Desktop/report copy.pdf", a)

	b := bytes.Repeat([]byte("b"), 3000)
	dupesFile(t, e, "Documents/b1.bin", b)
	dupesFile(t, e, "Pictures/b2.bin", b)

	dupesFile(t, e, "Documents/same-size.bin", bytes.Repeat([]byte("c"), 5000)) // same size as a, other content
	dupesFile(t, e, "Documents/unique.bin", bytes.Repeat([]byte("u"), 4000))
	return e
}

func dupesPlan(t *testing.T, e env.Env, args ...string) (string, *plan.Plan) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", append([]string{"dupes", "--min-size", "1KB", "--plan", file}, args...)...)
	if code != 0 {
		t.Fatalf("dupes --plan: exit %d\n%s\n%s", code, out, errOut)
	}
	return file, readPlanFile(t, file)
}

func dupesPaths(e env.Env, rels ...string) []string {
	out := make([]string, len(rels))
	for i, rel := range rels {
		out[i] = filepath.Join(e.Home, rel)
	}
	slices.Sort(out)
	return out
}

func TestDupesPlanChangesNothing(t *testing.T) {
	e := newDupesHome(t)
	before := snapshot(t, e)

	_, p := dupesPlan(t, e)
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	if p.Kind != plan.KindDupes {
		t.Errorf("kind %q", p.Kind)
	}
	want := dupesPaths(e, "Documents/work/report-final.pdf", "Desktop/report copy.pdf", "Pictures/b2.bin")
	if got := selectedPaths(p); !slices.Equal(got, want) {
		t.Errorf("selected %q, want %q", got, want)
	}
	for _, it := range p.Items {
		if it.Source != "dupes" || it.Risk != "risky" || it.KeepCopy == nil {
			t.Errorf("item %+v", it)
		}
	}
}

func TestDupesTableShowsGroups(t *testing.T) {
	e := newDupesHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	t.Logf("sapu dupes --plan\n%s", out)
	for _, line := range []string{
		"Same content · 3 copies", "Same content · 2 copies",
		"keep  ~/Downloads/report.pdf",
		"move  ~/Documents/work/report-final.pdf", "move  ~/Desktop/report copy.pdf",
		"Total · 3 extra copies in 2 groups",
		"APFS clones share storage", "Nothing was changed",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, name := range []string{"unique.bin", "same-size.bin"} {
		if strings.Contains(out, name) {
			t.Errorf("%s is listed", name)
		}
	}
	table, _, _ := strings.Cut(out, "Plan written")
	checkHuman(t, table)
}

func TestDupesDefaultRootsAndMinSize(t *testing.T) {
	e := env.New(t.TempDir())
	big := bytes.Repeat([]byte("x"), 1_200_000)
	dupesFile(t, e, "Downloads/big.bin", big)
	dupesFile(t, e, "Desktop/big.bin", big)
	small := bytes.Repeat([]byte("s"), 5000)
	dupesFile(t, e, "Downloads/small.bin", small)
	dupesFile(t, e, "Desktop/small.bin", small)
	dupesFile(t, e, "code/big.bin", big) // not a default folder
	dupesFile(t, e, "Library/Caches/big.bin", big)

	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, code := sapu(t, e, "", "dupes", "--plan", file); code != 0 {
		t.Fatal("dupes failed")
	}
	if got, want := selectedPaths(readPlanFile(t, file)), dupesPaths(e, "Desktop/big.bin"); !slices.Equal(got, want) {
		t.Errorf("default size 1MB: selected %q, want %q", got, want)
	}

	if _, _, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--plan", file); code != 0 {
		t.Fatal("dupes failed")
	}
	if got, want := selectedPaths(readPlanFile(t, file)), dupesPaths(e, "Desktop/big.bin", "Desktop/small.bin"); !slices.Equal(got, want) {
		t.Errorf("--min-size 1KB: selected %q, want %q", got, want)
	}

	// Naming a folder searches that folder only.
	if _, _, code := sapu(t, e, "", "dupes", filepath.Join(e.Home, "code"), "--plan", file); code != 0 {
		t.Fatal("dupes failed")
	}
	if got := selectedPaths(readPlanFile(t, file)); len(got) != 0 {
		t.Errorf("one copy in code/ has no duplicate there: %q", got)
	}
}

func TestDupesKeepFlag(t *testing.T) {
	e := newDupesHome(t)
	for keep, want := range map[string]string{
		"oldest":        "Downloads/report.pdf",
		"newest":        "Desktop/report copy.pdf",
		"shortest-path": "Downloads/report.pdf", // the shortest path under the home
	} {
		_, p := dupesPlan(t, e, "--keep", keep)
		got := ""
		for _, it := range p.Items {
			if strings.HasPrefix(it.KeepCopy.Path, e.Home) && strings.Contains(it.KeepCopy.Path, "report") {
				got = strings.TrimPrefix(it.KeepCopy.Path, e.Home+"/")
			}
		}
		if got != want {
			t.Errorf("--keep %s keeps %q, want %q", keep, got, want)
		}
	}
}

func TestDupesMovesOnlyExtrasAndUndoRestores(t *testing.T) {
	e := newDupesHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for _, line := range []string{"Moved 3 items", "The space is not free yet"} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, rel := range []string{"Documents/work/report-final.pdf", "Desktop/report copy.pdf", "Pictures/b2.bin"} {
		if exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s was not moved", rel)
		}
	}
	for _, rel := range []string{"Downloads/report.pdf", "Documents/b1.bin", "Documents/same-size.bin", "Documents/unique.bin"} {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s is gone", rel)
		}
	}

	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestDupesAsksFirst(t *testing.T) {
	e := newDupesHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "dupes", "--min-size", "1KB"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "dupes", "--min-size", "1KB"); code != 2 {
		t.Errorf("no answer: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	out, _, code := sapu(t, e, "y\n", "dupes", "--min-size", "1KB")
	if code != 0 || !strings.Contains(out, "Moved 3 items") {
		t.Errorf("answer y: exit %d\n%s", code, out)
	}
}

func TestDupesNothingFound(t *testing.T) {
	e := env.New(t.TempDir())
	dupesFile(t, e, "Documents/only.bin", bytes.Repeat([]byte("o"), 5000))
	out, _, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--yes")
	if code != 0 || !strings.Contains(out, "No duplicate files found.") {
		t.Errorf("exit %d\n%s", code, out)
	}
	noState(t, e)
}

// The safety case: the copy that stays is edited after the plan was made.
// Moving the extra would leave the only copy of the old content in quarantine.
func TestDupesKeptCopyEditedAfterPlanningLeavesExtraInPlace(t *testing.T) {
	e := env.New(t.TempDir())
	data := bytes.Repeat([]byte("a"), 5000)
	keeper := dupesFile(t, e, "Downloads/a.bin", data)
	extra := dupesFile(t, e, "Documents/a-copy.bin", data)

	file, p := dupesPlan(t, e)
	if got, want := selectedPaths(p), []string{extra}; !slices.Equal(got, want) {
		t.Fatalf("selected %q, want %q", got, want)
	}

	// The user edits the file that was going to stay.
	if err := os.WriteFile(keeper, []byte("new text, written after the plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(keeper, later, later); err != nil {
		t.Fatal(err)
	}

	out, errOut, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, out, errOut)
	}
	for _, line := range []string{"Nothing was moved.", "Skipped while applying", "1 changed", "Documents/a-copy.bin", "the copy that stays"} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	got, err := os.ReadFile(extra)
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("the extra changed or is gone: %v", err)
	}
	if got, _ := os.ReadFile(keeper); string(got) != "new text, written after the plan" {
		t.Errorf("the kept copy was touched: %q", got)
	}

	// The same when the kept copy is deleted.
	if err := os.Remove(keeper); err != nil {
		t.Fatal(err)
	}
	_, _, code = sapu(t, e, "", "apply", file, "--yes")
	if code != 3 || !exists(extra) {
		t.Errorf("kept copy deleted: exit %d, extra exists %v", code, exists(extra))
	}
}

func TestDupesExtraEditedAfterPlanningStays(t *testing.T) {
	e := env.New(t.TempDir())
	data := bytes.Repeat([]byte("a"), 5000)
	dupesFile(t, e, "Downloads/a.bin", data)
	extra := dupesFile(t, e, "Documents/a-copy.bin", data)

	file, _ := dupesPlan(t, e)
	if err := os.WriteFile(extra, []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 3 {
		t.Errorf("exit %d, want 3", code)
	}
	if got, _ := os.ReadFile(extra); string(got) != "edited" {
		t.Errorf("the edited extra was moved: %q", got)
	}
}

func TestDupesJSON(t *testing.T) {
	e := newDupesHome(t)

	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", "dupes", "--min-size", "1KB", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "dupes", out)
	var d dupesData
	decodeData(t, out, &d)
	if d.PlanFile != file {
		t.Errorf("plan_file %q", d.PlanFile)
	}
	if len(d.Groups) != 2 || d.Summary.SelectedItems != 3 {
		t.Fatalf("groups %+v, summary %+v", d.Groups, d.Summary)
	}
	first := d.Groups[0]
	if first.Keep.Path != filepath.Join(e.Home, "Downloads/report.pdf") || len(first.Extras) != 2 || first.Bytes <= 0 {
		t.Errorf("first group %+v", first)
	}
	noState(t, e)

	out, errOut, code = sapu(t, e, "", "dupes", "--min-size", "1KB", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "dupes", out)
	d = dupesData{}
	decodeData(t, out, &d)
	if len(d.Groups) != 2 || d.Result == nil || len(d.Result.Moved) != 3 || d.ExpiresAfter == nil {
		t.Errorf("data %+v", d)
	}

	// Nothing to find: groups is an empty list, not null.
	empty := env.New(t.TempDir())
	out, _, code = sapu(t, empty, "", "dupes", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "dupes", out)
	if !strings.Contains(out, `"groups": []`) && !strings.Contains(out, `"groups":[]`) {
		t.Errorf("groups is not an empty list:\n%s", out)
	}
}

func TestDupesUsageErrors(t *testing.T) {
	e := newDupesHome(t)
	for _, args := range [][]string{
		{"dupes", "--keep", "random"},
		{"dupes", "--min-size", "big"},
		{"dupes", "--min-size", "0"},
		{"dupes", "--min-size", "-5MB"},
		{"dupes", "--min-size", "inf"},
		{"dupes", "--json"}, // cannot ask
		{"dupes", "--plan", "-", "--json"},
	} {
		if _, _, code := sapu(t, e, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	// A folder that Sapu never searches, and one that is not there.
	for _, dir := range []string{filepath.Join(e.Home, "Library"), filepath.Join(e.Home, "missing")} {
		if _, _, code := sapu(t, e, "", "dupes", dir, "--yes"); code != 1 {
			t.Errorf("%s: exit %d, want 1", dir, code)
		}
	}
	noState(t, e)
}

func TestParseDupesMinSize(t *testing.T) {
	for in, want := range map[string]int64{
		"1MB": 1_000_000, "1mb": 1_000_000, "500KB": 500_000, "10M": 10_000_000, "1.5GB": 1_500_000_000,
		"2G": 2_000_000_000, "1TB": 1_000_000_000_000, "4096": 4096, "1b": 1, " 3 KB ": 3000,
	} {
		got, err := parseDupesMinSize(in)
		if err != nil || got != want {
			t.Errorf("%q: %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MB", "0", "0.0001KB", "-1", "nan", "inf", "1XB", "1e30GB"} {
		if got, err := parseDupesMinSize(in); err == nil {
			t.Errorf("%q: accepted as %d", in, got)
		}
	}
}
