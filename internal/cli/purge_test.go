package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// newProjects builds two projects under ~/code and returns the code folder.
func newProjects(t *testing.T) (env.Env, string) {
	t.Helper()
	e := env.New(t.TempDir())
	code := filepath.Join(e.Home, "code")
	files := map[string]string{
		"web/package.json":                   `{"name":"web"}`,
		"web/src/index.js":                   "console.log('source')",
		"web/node_modules/left-pad/index.js": "module.exports = 1",
		"api/Cargo.toml":                     "[package]",
		"api/src/main.rs":                    "fn main() {}",
		"api/target/debug/api":               "binary",
	}
	for rel, data := range files {
		writeFile(t, filepath.Join(code, rel), data)
	}
	return e, code
}

func TestPurgePlanChangesNothing(t *testing.T) {
	e, code := newProjects(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, exit := sapu(t, e, "", "purge", code, "--plan", file)
	if exit != 0 {
		t.Fatalf("exit %d\n%s\n%s", exit, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	p := readPlanFile(t, file)
	want := []string{filepath.Join(code, "api/target"), filepath.Join(code, "web/node_modules")}
	if got := selectedPaths(p); strings.Join(got, ",") != strings.Join(want, ",") || p.Kind != plan.KindPurge {
		t.Errorf("plan kind %q selects %v, want %v", p.Kind, got, want)
	}
}

func TestPurgeMovesArtifactsAndKeepsSource(t *testing.T) {
	e, code := newProjects(t)
	before := snapshot(t, e)

	out, errOut, exit := sapu(t, e, "y\n", "purge", code)
	if exit != 0 {
		t.Fatalf("exit %d\n%s\n%s", exit, out, errOut)
	}
	t.Logf("sapu purge, answering y\n%s", out)
	for _, line := range []string{
		"purge:node · 1 item", "purge:cargo · 1 item", // grouped by type
		"Project: ~/code/web", // each artifact names its project
		"~/code/web/node_modules", "~/code/api/target",
		"Moved 2 items", "The space is not free yet",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	for _, gone := range []string{"web/node_modules", "api/target"} {
		if exists(filepath.Join(code, gone)) {
			t.Errorf("%s was not moved", gone)
		}
	}
	for _, kept := range []string{"web/src/index.js", "web/package.json", "api/src/main.rs", "api/Cargo.toml"} {
		if !exists(filepath.Join(code, kept)) {
			t.Errorf("source file %s is gone", kept)
		}
	}

	if _, _, exit := sapu(t, e, "", "undo", "--last"); exit != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestPurgeAsksFirst(t *testing.T) {
	e, code := newProjects(t)
	before := snapshot(t, e)
	if _, _, exit := sapu(t, e, "n\n", "purge", code); exit != 2 {
		t.Errorf("answer n: exit %d, want 2", exit)
	}
	if _, _, exit := sapu(t, e, "", "purge", code); exit != 2 {
		t.Errorf("empty stdin: exit %d, want 2", exit)
	}
	if _, _, exit := sapu(t, e, "yes\n", "purge", code, "--permanent"); exit != 2 {
		t.Errorf("--permanent answered yes: exit %d, want 2", exit)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	out, _, exit := sapu(t, e, "delete\n", "purge", code, "--permanent")
	if exit != 0 || !strings.Contains(out, "Deleted 2 items") {
		t.Errorf("exit %d\n%s", exit, out)
	}
}

func TestPurgeOlderThan(t *testing.T) {
	e, code := newProjects(t)
	// Both projects were just touched, so nothing is old enough.
	out, _, exit := sapu(t, e, "", "purge", code, "--older-than", "30d", "--yes")
	if exit != 0 || !strings.Contains(out, "Nothing") {
		t.Errorf("exit %d\n%s", exit, out)
	}
	noState(t, e)

	// Age the web project; only its artifact is selected now.
	old := time.Now().Add(-60 * 24 * time.Hour)
	web := filepath.Join(code, "web")
	err := filepath.WalkDir(web, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !strings.Contains(p, "node_modules") {
			return os.Chtimes(p, old, old)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, _, exit := sapu(t, e, "", "purge", code, "--older-than", "30d", "--plan", file); exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	if got := selectedPaths(readPlanFile(t, file)); len(got) != 1 || got[0] != filepath.Join(code, "web/node_modules") {
		t.Errorf("selected %v", got)
	}

	if _, _, exit := sapu(t, e, "", "purge", code, "--older-than", "soon"); exit != 2 {
		t.Errorf("bad --older-than: exit %d, want 2", exit)
	}
}

func TestPurgeFlagsAndErrors(t *testing.T) {
	e, code := newProjects(t)
	if _, _, exit := sapu(t, e, "", "purge", code, "--risk", "wild"); exit != 2 {
		t.Errorf("bad --risk: exit %d, want 2", exit)
	}
	if _, _, exit := sapu(t, e, "", "purge", filepath.Join(code, "missing"), "--yes"); exit != 1 {
		t.Errorf("missing folder: exit %d, want 1", exit)
	}
	if _, _, exit := sapu(t, e, "", "purge", code, "--json"); exit != 2 {
		t.Errorf("--json without --yes: exit %d, want 2", exit)
	}
	noState(t, e)
}

func TestPurgeJSON(t *testing.T) {
	e, code := newProjects(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, exit := sapu(t, e, "", "purge", code, "--plan", file, "--json")
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	validate(t, "purge", out)
	var d runData
	decodeData(t, out, &d)
	if d.PlanFile != file && !strings.HasSuffix(d.PlanFile, "plan.json") {
		t.Errorf("plan_file %q", d.PlanFile)
	}
	if d.Summary.SelectedItems != 2 || d.Result != nil {
		t.Errorf("data %+v", d)
	}

	out, _, exit = sapu(t, e, "", "purge", code, "--yes", "--json")
	if exit != 0 {
		t.Fatalf("exit %d\n%s", exit, out)
	}
	validate(t, "purge", out)
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Moved) != 2 {
		t.Errorf("data %+v", d)
	}

	out, _, exit = sapu(t, e, "", "purge", filepath.Join(code, "missing"), "--yes", "--json")
	if exit != 1 || !strings.Contains(out, `"ok": false`) {
		t.Errorf("exit %d\n%s", exit, out)
	}
	validate(t, "purge", out)
}
