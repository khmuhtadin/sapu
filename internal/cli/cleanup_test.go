package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// The junk in a fake home. Each of the first four lines is one plan item that
// clean selects: logs.homebrew (one item per formula folder), node-gyp.cache,
// typescript.cache (all safe) and npm.cache (moderate). go.module-cache is an
// official command: shown, never run. The rest are decoys no rule targets.
var junk = map[string]string{
	"Library/Logs/Homebrew/x/build.log":    "homebrew log",
	"Library/Caches/node-gyp/headers.tar":  "node headers",
	"Library/Caches/typescript/types.json": "types",
	".npm/_cacache/content-v2/blob":        "npm blob",
	"go/pkg/mod/example.com/m@v1/m.go":     "package m",
}

var decoys = map[string]string{
	"Library/Caches/JetBrains/idea/index.db": "datagrip data sources",
	".claude/projects/memory.md":             "claude memory",
	"Documents/important.txt":                "do not lose me",
}

var junkItems = []string{
	"Library/Logs/Homebrew/x",
	"Library/Caches/node-gyp",
	"Library/Caches/typescript",
	".npm/_cacache",
}

func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	for _, files := range []map[string]string{junk, decoys} {
		for rel, data := range files {
			writeFile(t, filepath.Join(e.Home, rel), data)
		}
	}
	return e
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// snapshot records every file (mode and content) and folder under the home,
// except Sapu's own state folder.
func snapshot(t *testing.T, e env.Env) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(e.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == e.StateDir {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(e.Home, p)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			snap[rel] = "dir " + info.Mode().String()
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		snap[rel] = info.Mode().String() + " " + string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func sameSnapshot(t *testing.T, want, got map[string]string) {
	t.Helper()
	for p, w := range want {
		if g, ok := got[p]; !ok {
			t.Errorf("missing after the run: %s", p)
		} else if g != w {
			t.Errorf("%s changed: %q -> %q", p, w, g)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			t.Errorf("new after the run: %s", p)
		}
	}
}

func noState(t *testing.T, e env.Env) {
	t.Helper()
	if exists(e.StateDir) {
		t.Errorf("%s exists: the command wrote Sapu state, but it must not have changed anything", e.StateDir)
	}
}

func assertDecoys(t *testing.T, e env.Env) {
	t.Helper()
	for rel, data := range decoys {
		got, err := os.ReadFile(filepath.Join(e.Home, rel))
		if err != nil || string(got) != data {
			t.Errorf("decoy %s was touched: %q, %v", rel, got, err)
		}
	}
}

func decodeData(t *testing.T, out string, into any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// planFileOf writes a plan with the given clean flags and returns its path.
func planFileOf(t *testing.T, e env.Env, args ...string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", append([]string{"clean", "--plan", file}, args...)...)
	if code != 0 {
		t.Fatalf("clean --plan: exit %d\n%s\n%s", code, out, errOut)
	}
	return file
}

func readPlanFile(t *testing.T, file string) *plan.Plan {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p, err := plan.Read(f)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// summaryOf runs clean --plan --json with args and returns the summary.
func summaryOf(t *testing.T, e env.Env, args ...string) planSummary {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", append([]string{"clean", "--plan", file, "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "clean", out)
	var d runData
	decodeData(t, out, &d)
	return d.Summary
}

func selectedPaths(p *plan.Plan) []string {
	var paths []string
	for _, it := range p.Items {
		if it.Selected && it.Action == "quarantine" {
			paths = append(paths, it.Path)
		}
	}
	slices.Sort(paths)
	return paths
}

func runIDOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`run (\d{8}-\d{6}-[0-9a-f]{4})`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no run id in output:\n%s", out)
	}
	return m[1]
}

func TestCleanPlanChangesNothing(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "clean", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Nothing was changed") {
		t.Errorf("output does not say nothing changed:\n%s", out)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	// The plan file holds exactly the selected junk, and the official command.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	check(t, "plan", doc)
	p := readPlanFile(t, file)
	var want []string
	for _, rel := range junkItems {
		want = append(want, filepath.Join(e.Home, rel))
	}
	slices.Sort(want)
	if got := selectedPaths(p); !slices.Equal(got, want) {
		t.Errorf("selected items = %v, want %v", got, want)
	}
	if p.Home != e.Home || p.Kind != plan.KindClean {
		t.Errorf("plan home %q kind %q", p.Home, p.Kind)
	}
}

func TestCleanPlanToStdout(t *testing.T) {
	e := newHome(t)
	out, _, code := sapu(t, e, "", "clean", "--plan", "-")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	check(t, "plan", doc)
	if _, ok := doc.(map[string]any)["command"]; ok {
		t.Error("--plan - must print the raw plan, not an envelope")
	}
	noState(t, e)

	if _, _, code := sapu(t, e, "", "clean", "--plan", "-", "--json"); code != 2 {
		t.Errorf("--plan - with --json: exit %d, want 2", code)
	}
}

func TestCleanAsksThenMovesAndUndoRestores(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)
	want := selectedPaths(readPlanFile(t, planFileOf(t, e)))

	out, errOut, code := sapu(t, e, "y\n", "clean")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu clean, answering y\n--- stdout\n%s--- stderr\n%s", out, errOut)

	for _, path := range want {
		if exists(path) {
			t.Errorf("%s was not moved", path)
		}
	}
	if !exists(filepath.Join(e.Home, "go/pkg/mod/example.com/m@v1/m.go")) {
		t.Error("the official command item was touched; Sapu must not run it")
	}
	assertDecoys(t, e)

	id := runIDOf(t, out)
	freed := time.Now().Add(fsop.DefaultMaxAge).Format("Mon 2 Jan")
	for _, line := range []string{
		"Moved 4 items",
		"to quarantine, run " + id,
		"The space is not free yet: these items stay in quarantine until at least " + freed + ".",
		"To free the space now: `sapu quarantine empty " + id + "`",
		"Undo: `sapu undo " + id + "`",
		"Move 4 items", // the question, on stderr
	} {
		if !strings.Contains(out+errOut, line) {
			t.Errorf("output lacks %q", line)
		}
	}

	hist, _, code := sapu(t, e, "", "history")
	if code != 0 || !strings.Contains(hist, id) || !strings.Contains(hist, "clean") || !strings.Contains(hist, "done") {
		t.Errorf("history (exit %d) does not list the run:\n%s", code, hist)
	}
	t.Logf("sapu history\n%s", hist)

	undoOut, _, code := sapu(t, e, "", "undo", "--last")
	if code != 0 || !strings.Contains(undoOut, "Restored 4 items from run "+id) {
		t.Fatalf("undo: exit %d\n%s", code, undoOut)
	}
	sameSnapshot(t, before, snapshot(t, e))

	hist, _, _ = sapu(t, e, "", "history")
	if !strings.Contains(hist, "undone") {
		t.Errorf("history does not show the run as undone:\n%s", hist)
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 1 {
		t.Errorf("a second undo --last: exit %d, want 1 (nothing left to undo)", code)
	}
}

func TestCleanConfirmation(t *testing.T) {
	for _, tc := range []struct{ name, stdin, hint string }{
		{"no", "n\n", "not confirmed"},
		{"anything else", "maybe\n", "not confirmed"},
		{"empty stdin", "", "--yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newHome(t)
			before := snapshot(t, e)
			out, errOut, code := sapu(t, e, tc.stdin, "clean")
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			if !strings.Contains(errOut, tc.hint) || !strings.Contains(errOut, "nothing was changed") {
				t.Errorf("stderr lacks %q:\n%s", tc.hint, errOut)
			}
			if !strings.Contains(out, "Total selected") {
				t.Errorf("the plan table was not shown:\n%s", out)
			}
			sameSnapshot(t, before, snapshot(t, e))
			noState(t, e)
		})
	}
}

func TestCleanYesAnswers(t *testing.T) {
	e := newHome(t)
	if _, _, code := sapu(t, e, "YES\n", "clean"); code != 0 {
		t.Errorf("YES: exit %d", code)
	}
}

func TestCleanNothingToDo(t *testing.T) {
	e := env.New(t.TempDir())
	out, _, code := sapu(t, e, "", "clean")
	if code != 0 || !strings.Contains(out, "Nothing to move") {
		t.Errorf("exit %d\n%s", code, out)
	}
	noState(t, e)
}

func TestPermanent(t *testing.T) {
	t.Run("typing delete", func(t *testing.T) {
		e := newHome(t)
		out, errOut, code := sapu(t, e, "delete\n", "clean", "--permanent")
		if code != 0 {
			t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
		}
		if !strings.Contains(errOut, "WARNING") || !strings.Contains(errOut, "for good") {
			t.Errorf("no clear warning on stderr:\n%s", errOut)
		}
		if !strings.Contains(out, "Deleted 4 items") || strings.Contains(out, "quarantine, run") {
			t.Errorf("result does not say the items are deleted:\n%s", out)
		}
		for _, rel := range junkItems {
			if exists(filepath.Join(e.Home, rel)) {
				t.Errorf("%s is still there", rel)
			}
		}
		assertDecoys(t, e)
		if _, _, code := sapu(t, e, "", "undo", "--last"); code == 0 {
			t.Error("undo worked after --permanent")
		}
		held, _, _ := sapu(t, e, "", "quarantine", "list")
		if !strings.Contains(held, "empty") {
			t.Errorf("quarantine is not empty after --permanent:\n%s", held)
		}
	})

	for _, stdin := range []string{"yes\n", "y\n", "DELETE\n", ""} {
		t.Run("not "+strings.TrimSpace(stdin), func(t *testing.T) {
			e := newHome(t)
			before := snapshot(t, e)
			_, _, code := sapu(t, e, stdin, "clean", "--permanent")
			if code != 2 {
				t.Errorf("exit %d, want 2", code)
			}
			sameSnapshot(t, before, snapshot(t, e))
			noState(t, e)
		})
	}

	t.Run("with --yes", func(t *testing.T) {
		e := newHome(t)
		out, errOut, code := sapu(t, e, "", "clean", "--permanent", "--yes")
		if code != 0 || !strings.Contains(out, "Deleted 4 items") || !strings.Contains(errOut, "WARNING") {
			t.Errorf("exit %d\n%s\n%s", code, out, errOut)
		}
	})

	t.Run("not with --plan", func(t *testing.T) {
		e := newHome(t)
		file := filepath.Join(t.TempDir(), "plan.json")
		if _, _, code := sapu(t, e, "", "clean", "--plan", file, "--permanent"); code != 2 {
			t.Errorf("exit %d, want 2", code)
		}
	})
}

func TestSelectionFlags(t *testing.T) {
	e := newHome(t)
	for _, tc := range []struct {
		name string
		args []string
		want int // selected items
	}{
		{"default is moderate", nil, 4},
		{"risk safe", []string{"--risk", "safe"}, 3},
		{"risk moderate", []string{"--risk", "moderate"}, 4},
		{"risk risky", []string{"--risk", "risky"}, 4},
		{"only a rule id", []string{"--only", "node-gyp.cache"}, 1},
		{"only a category", []string{"--only", "dev"}, 3},
		{"only twice", []string{"--only", "node-gyp.cache", "--only", "logs.homebrew"}, 2},
		{"exclude a rule id", []string{"--exclude", "npm.cache"}, 3},
		{"exclude a category", []string{"--exclude", "dev"}, 1},
		{"only and exclude", []string{"--only", "dev", "--exclude", "typescript.cache"}, 2},
		{"only with risk", []string{"--only", "dev", "--risk", "safe"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := summaryOf(t, e, tc.args...); got.SelectedItems != tc.want {
				t.Errorf("selected %d items, want %d", got.SelectedItems, tc.want)
			}
		})
	}

	sum := summaryOf(t, e, "--risk", "safe")
	if sum.NotSelectedItems == 0 || sum.Commands != 0 {
		t.Errorf("--risk safe: %+v: the moderate items should be listed but not selected", sum)
	}
	if sum := summaryOf(t, e); sum.Commands != 1 {
		t.Errorf("commands = %d, want the go.module-cache suggestion", sum.Commands)
	}

	for _, args := range [][]string{
		{"--risk", "extreme"},
		{"--only", "no-such-rule"},
		{"--exclude", "no-such-rule"},
	} {
		if _, _, code := sapu(t, e, "", append([]string{"clean", "--yes"}, args...)...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	noState(t, e)

	// --risk safe moves only the safe items.
	if _, _, code := sapu(t, e, "", "clean", "--risk", "safe", "--yes"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !exists(filepath.Join(e.Home, ".npm/_cacache")) {
		t.Error("--risk safe moved the moderate npm cache")
	}
	if exists(filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("--risk safe left a safe item")
	}
}

func TestWhitelistedItemIsNotTouched(t *testing.T) {
	e := newHome(t)
	if out, _, code := sapu(t, e, "", "whitelist", "add", "~/Library/Caches/typescript"); code != 0 {
		t.Fatalf("whitelist add: exit %d\n%s", code, out)
	}
	out, _, code := sapu(t, e, "n\n", "clean")
	if code != 2 {
		t.Fatalf("exit %d", code)
	}
	for _, line := range []string{"Not touched", "1 whitelisted", "~/Library/Caches/typescript"} {
		if !strings.Contains(out, line) {
			t.Errorf("table lacks %q:\n%s", line, out)
		}
	}

	if _, _, code := sapu(t, e, "", "clean", "--yes"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !exists(filepath.Join(e.Home, "Library/Caches/typescript/types.json")) {
		t.Error("the whitelisted folder was moved")
	}
	if exists(filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("the other items were not moved")
	}
}

func TestCleanJSON(t *testing.T) {
	e := newHome(t)

	out, _, code := sapu(t, e, "", "clean", "--json")
	if code != 2 {
		t.Errorf("--json without --yes: exit %d, want 2", code)
	}
	validate(t, "clean", out)
	if !strings.Contains(out, `"code": "usage"`) {
		t.Errorf("not a usage error envelope:\n%s", out)
	}
	noState(t, e)

	out, errOut, code := sapu(t, e, "", "clean", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "clean", out)
	var d runData
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Moved) != 4 || d.Result.RunID == "" || d.ExpiresAfter == nil {
		t.Errorf("unexpected data: %+v", d)
	}
	if d.Summary.SelectedItems != 4 || d.Summary.Commands != 1 {
		t.Errorf("summary %+v", d.Summary)
	}

	// Nothing selected still gives an envelope.
	out, _, code = sapu(t, e, "", "clean", "--yes", "--json")
	if code != 0 {
		t.Fatalf("second run: exit %d\n%s", code, out)
	}
	validate(t, "clean", out)
}

func TestApplyPlanFile(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)
	file := planFileOf(t, e)
	want := selectedPaths(readPlanFile(t, file))

	out, errOut, code := sapu(t, e, "y\n", "apply", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for _, path := range want {
		if exists(path) {
			t.Errorf("%s was not moved", path)
		}
	}
	if !strings.Contains(out, "Moved 4 items") || !strings.Contains(out, "The space is not free yet") {
		t.Errorf("result:\n%s", out)
	}
	assertDecoys(t, e)
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestApplyFromStdin(t *testing.T) {
	e := newHome(t)
	planJSON, _, code := sapu(t, e, "", "clean", "--plan", "-")
	if code != 0 {
		t.Fatal("clean --plan -")
	}
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, planJSON, "apply", "-"); code != 2 {
		t.Errorf("apply - without --yes: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))

	out, errOut, code := sapu(t, e, planJSON, "apply", "-", "--yes")
	if code != 0 || !strings.Contains(out, "Moved 4 items") {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
}

func TestApplyAnswersAndFlags(t *testing.T) {
	e := newHome(t)
	file := planFileOf(t, e)
	before := snapshot(t, e)

	if _, _, code := sapu(t, e, "n\n", "apply", file); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "apply", file); code != 2 {
		t.Errorf("empty stdin: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "apply", file, "--permanent"); code != 2 {
		t.Errorf("--permanent without typing delete: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "apply"); code != 2 {
		t.Errorf("no plan: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "apply", file, "--json"); code != 2 {
		t.Errorf("--json without --yes: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "apply", filepath.Join(t.TempDir(), "missing.json"), "--yes"); code != 1 {
		t.Errorf("missing file: exit %d, want 1", code)
	}
	garbage := filepath.Join(t.TempDir(), "garbage.json")
	writeFile(t, garbage, "not a plan")
	if _, _, code := sapu(t, e, "", "apply", garbage, "--yes"); code != 1 {
		t.Errorf("garbage file: exit %d, want 1", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	out, _, code := sapu(t, e, "delete\n", "apply", file, "--permanent")
	if code != 0 || !strings.Contains(out, "Deleted 4 items") {
		t.Errorf("--permanent with delete: exit %d\n%s", code, out)
	}
}

func TestApplyRefusesAPlanFromAnotherHome(t *testing.T) {
	mine := newHome(t)
	other := newHome(t)
	file := planFileOf(t, other)
	before := snapshot(t, mine)
	beforeOther := snapshot(t, other)

	out, errOut, code := sapu(t, mine, "", "apply", file, "--yes")
	if code != 1 {
		t.Errorf("exit %d, want 1\n%s", code, out)
	}
	if !strings.Contains(errOut, "made for the home folder "+other.Home) {
		t.Errorf("stderr does not explain:\n%s", errOut)
	}
	sameSnapshot(t, before, snapshot(t, mine))
	sameSnapshot(t, beforeOther, snapshot(t, other))
	noState(t, mine)
	noState(t, other)
}

func TestApplySkipsChangedItemsAndExits3(t *testing.T) {
	e := newHome(t)
	file := planFileOf(t, e)
	// A new file changes the folder's mtime, so its identity no longer matches the plan.
	writeFile(t, filepath.Join(e.Home, "Library/Caches/node-gyp/new-file"), "fresh")

	out, errOut, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, out, errOut)
	}
	for _, line := range []string{"Moved 3 items", "Skipped while applying", "1 changed", "~/Library/Caches/node-gyp"} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q:\n%s", line, out)
		}
	}
	if !strings.Contains(errOut, "skipped or failed") {
		t.Errorf("stderr: %q", errOut)
	}
	if !exists(filepath.Join(e.Home, "Library/Caches/node-gyp/new-file")) {
		t.Error("the changed item was moved")
	}
	if exists(filepath.Join(e.Home, "Library/Caches/typescript")) {
		t.Error("the unchanged items were not moved")
	}
}

func TestApplyJSON(t *testing.T) {
	e := newHome(t)
	file := planFileOf(t, e)
	writeFile(t, filepath.Join(e.Home, "Library/Caches/node-gyp/new-file"), "fresh")

	out, _, code := sapu(t, e, "", "apply", file, "--yes", "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	validate(t, "apply", out) // one envelope even though the exit code is 3
	var d runData
	decodeData(t, out, &d)
	if len(d.Result.Moved) != 3 || len(d.Result.Skipped) != 1 || d.Result.Skipped[0].Reason != plan.ReasonChanged {
		t.Errorf("result %+v", d.Result)
	}

	out, _, code = sapu(t, e, "", "apply", file, "--json")
	if code != 2 {
		t.Errorf("exit %d, want 2", code)
	}
	validate(t, "apply", out)
}

// sapuWith is sapu() with a context and a stderr the test chooses.
func sapuWith(t *testing.T, ctx context.Context, e env.Env, stderr *bytes.Buffer, writer func([]byte), args ...string) (string, int) {
	t.Helper()
	old := loadEnv
	loadEnv = func() (env.Env, error) { return e, nil }
	t.Cleanup(func() { loadEnv = old })
	var out bytes.Buffer
	code := runArgs(ctx, args, strings.NewReader(""), &out, hook{stderr, writer})
	return out.String(), code
}

// hook sees every write to stderr.
type hook struct {
	buf    *bytes.Buffer
	onLine func([]byte)
}

func (h hook) Write(b []byte) (int, error) {
	h.onLine(b)
	return h.buf.Write(b)
}

func TestCancelDuringScan(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var errOut bytes.Buffer
	_, code := sapuWith(t, ctx, e, &errOut, func([]byte) {}, "clean", "--yes")
	if code != 130 {
		t.Errorf("exit %d, want 130", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestCancelDuringApplyPrintsPartialResult(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		e := newHome(t)
		ctx, cancel := context.WithCancel(context.Background())
		var errOut bytes.Buffer
		onWrite := func(b []byte) {
			if bytes.Contains(b, []byte(`"phase":"apply"`)) {
				cancel() // after the first item is moved
			}
		}
		args := []string{"clean", "--yes", "--progress", "ndjson"}
		if jsonMode {
			args = append(args, "--json")
		}
		out, code := sapuWith(t, ctx, e, &errOut, onWrite, args...)
		cancel()
		if code != 130 {
			t.Errorf("json=%v: exit %d, want 130\n%s", jsonMode, code, out)
		}
		if jsonMode {
			validate(t, "clean", out)
			var d runData
			decodeData(t, out, &d)
			if !d.Cancelled || len(d.Result.Moved) != 1 {
				t.Errorf("cancelled=%v moved=%d, want a partial result of 1", d.Cancelled, len(d.Result.Moved))
			}
		} else if !strings.Contains(out, "Moved 1 item ") {
			t.Errorf("no partial result:\n%s", out)
		}
		// What was moved can be put back.
		if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
			t.Errorf("json=%v: undo --last: exit %d", jsonMode, code)
		}
		for rel := range junk {
			if !exists(filepath.Join(e.Home, rel)) {
				t.Errorf("json=%v: %s is missing after undo", jsonMode, rel)
			}
		}
	}
}

func TestUndo(t *testing.T) {
	e := newHome(t)
	out, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, out)

	if _, _, code := sapu(t, e, "", "undo"); code != 2 {
		t.Errorf("no run id and no --last: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "undo", id, "--last"); code != 2 {
		t.Errorf("run id with --last: exit %d, want 2", code)
	}
	if _, _, code := sapu(t, e, "", "undo", "19990101-000000-abcd"); code == 0 {
		t.Error("an unknown run was undone")
	}

	// Something is at the original place now: the item comes back beside it.
	writeFile(t, filepath.Join(e.Home, "Library/Caches/typescript/other.json"), "new")
	jsonText, _, code := sapu(t, e, "", "undo", id, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, jsonText)
	}
	validate(t, "undo", jsonText)
	var d undoData
	decodeData(t, jsonText, &d)
	if d.RunID != id || len(d.Restored) != 4 || d.Conflicts != 1 {
		t.Errorf("undo data %+v", d)
	}
	if got, _ := os.ReadFile(filepath.Join(e.Home, "Library/Caches/typescript/other.json")); string(got) != "new" {
		t.Error("the new folder was overwritten")
	}

	// The human text for a conflict.
	e2 := newHome(t)
	out, _, _ = sapu(t, e2, "", "clean", "--yes")
	id2 := runIDOf(t, out)
	writeFile(t, filepath.Join(e2.Home, "Library/Caches/typescript/other.json"), "new")
	text, _, code := sapu(t, e2, "", "undo", id2)
	if code != 0 || !strings.Contains(text, "restored next to the original as ") {
		t.Errorf("exit %d\n%s", code, text)
	}
}

func TestHistory(t *testing.T) {
	e := newHome(t)
	out, _, code := sapu(t, e, "", "history")
	if code != 0 || !strings.Contains(out, "No runs yet") {
		t.Errorf("exit %d\n%s", code, out)
	}
	out, _, code = sapu(t, e, "", "history", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	validate(t, "history", out)

	run, _, _ := sapu(t, e, "", "clean", "--yes")
	id := runIDOf(t, run)
	out, _, _ = sapu(t, e, "", "history")
	for _, col := range []string{"Run", "Date", "Kind", "Items", "Size", "Status"} {
		if !strings.Contains(out, col) {
			t.Errorf("no %q column:\n%s", col, out)
		}
	}
	if !regexp.MustCompile(id + `\s+\d{4}-\d{2}-\d{2} \d{2}:\d{2}\s+clean\s+4\s+\S+ \S+\s+done`).MatchString(out) {
		t.Errorf("row of run %s is not as expected:\n%s", id, out)
	}

	out, _, _ = sapu(t, e, "", "history", "--json")
	validate(t, "history", out)
	var d historyData
	decodeData(t, out, &d)
	if len(d.Runs) != 1 || d.Runs[0].ID != id || d.Runs[0].Items != 4 {
		t.Errorf("history data %+v", d)
	}
}

// ageRun makes a run look old by moving its start event back in time.
func ageRun(t *testing.T, e env.Env, id string, by time.Duration) {
	t.Helper()
	path := filepath.Join(e.RunsDir(), id, "journal.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	first, rest, _ := bytes.Cut(data, []byte("\n"))
	var ev runlog.Event
	if err := json.Unmarshal(first, &ev); err != nil || ev.Type != runlog.EventStart {
		t.Fatalf("first journal line is not the start event: %s, %v", first, err)
	}
	ev.Time = ev.Time.Add(-by)
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(line, '\n'), rest...), 0o600); err != nil {
		t.Fatal(err)
	}
}
