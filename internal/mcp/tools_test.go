package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Junk that rules target: node-gyp and typescript caches are safe, the npm
// cache is moderate, and the Go module cache is an official command.
// The rest are decoys no rule may select.
var fixture = map[string]string{
	"Library/Caches/node-gyp/headers.tar":  "node headers",
	"Library/Caches/typescript/types.json": "types",
	".npm/_cacache/content-v2/blob":        "npm blob",
	"go/pkg/mod/example.com/m@v1/m.go":     "package m",

	"Library/Caches/JetBrains/idea/index.db": "datagrip data sources",
	".claude/projects/memory.md":             "claude memory",
	"Documents/important.txt":                "do not lose me",

	"code/web/package.json":                   `{"name":"web"}`,
	"code/web/src/index.js":                   "source",
	"code/web/node_modules/left-pad/index.js": "module.exports = 1",
	"code/api/Cargo.toml":                     "[package]",
	"code/api/src/main.rs":                    "fn main() {}",
	"code/api/target/debug/api":               "binary",
}

func newHome(t *testing.T) env.Env {
	t.Helper()
	e := env.New(t.TempDir())
	for rel, data := range fixture {
		path := filepath.Join(e.Home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// snapshot records every file (mode and content) and folder under the home,
// state folder included.
func snapshot(t *testing.T, e env.Env) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(e.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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

// call runs one tool call and returns the tool result, or fails on a JSON-RPC error.
func call(t *testing.T, e env.Env, name, args string) map[string]any {
	t.Helper()
	req := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, name, args)
	replies := serve(t, e, lines(req))
	if len(replies) != 1 || replies[0].Error != nil {
		t.Fatalf("%s %s: replies %+v", name, args, replies)
	}
	return replies[0].Result
}

func textOf(t *testing.T, res map[string]any) string {
	t.Helper()
	content := res["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["type"] != "text" {
		t.Fatalf("content %v", content)
	}
	return content[0].(map[string]any)["text"].(string)
}

// ok checks a successful result and returns its text and structured data.
func ok(t *testing.T, res map[string]any) (string, map[string]any) {
	t.Helper()
	if res["isError"] != false {
		t.Fatalf("isError %v: %v", res["isError"], res)
	}
	data, isMap := res["structuredContent"].(map[string]any)
	if !isMap {
		t.Fatalf("structuredContent %v", res["structuredContent"])
	}
	return textOf(t, res), data
}

func num(v any) int64 { return int64(v.(float64)) }

func TestSummary(t *testing.T) {
	e := newHome(t)
	text, data := ok(t, call(t, e, "summary", `{}`))

	rec := data["reclaimable"].(map[string]any)
	if num(rec["safe_bytes"]) == 0 || num(rec["moderate_bytes"]) == 0 {
		t.Errorf("reclaimable %v: the fixture has safe and moderate junk", rec)
	}
	if num(rec["selected_bytes"]) != num(rec["safe_bytes"])+num(rec["moderate_bytes"]) {
		t.Errorf("selected_bytes is not safe plus moderate: %v", rec)
	}
	if num(data["free_bytes"]) <= 0 || num(data["total_bytes"]) < num(data["free_bytes"]) {
		t.Errorf("free %v of %v", data["free_bytes"], data["total_bytes"])
	}
	for _, key := range []string{"protected_items", "in_use_items", "full_disk_access"} {
		if _, ok := data[key]; !ok {
			t.Errorf("data lacks %s", key)
		}
	}
	for _, want := range []string{"Free space:", "Can be cleaned:", "Full Disk Access:", "plan_clean"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestExplain(t *testing.T) {
	text, data := ok(t, call(t, newHome(t), "explain", `{}`))
	rows := data["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	if !strings.Contains(text, "What fills") || !strings.Contains(text, "Sapu never runs these commands.") {
		t.Errorf("text:\n%s", text)
	}
}

func TestAnalyze(t *testing.T) {
	e := newHome(t)

	// The defaults: the home folder, top 20, depth 2.
	text, data := ok(t, call(t, e, "analyze", `{}`))
	if data["top"] != float64(20) || data["depth"] != float64(2) {
		t.Errorf("defaults: top %v depth %v", data["top"], data["depth"])
	}
	root := data["root"].(map[string]any)
	if root["path"] != e.Home || num(root["bytes"]) == 0 {
		t.Errorf("root %v", root)
	}
	for _, want := range []string{"Library/", "code/"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	// A path, with ~ and a smaller top.
	_, data = ok(t, call(t, e, "analyze", `{"path":"~/code","top":1,"depth":1}`))
	root = data["root"].(map[string]any)
	if root["path"] != filepath.Join(e.Home, "code") || len(root["children"].([]any)) != 1 {
		t.Errorf("~/code top 1: %v", root)
	}
}

func TestAnalyzeErrors(t *testing.T) {
	e := newHome(t)
	for _, args := range []string{
		`{"top":0}`, `{"top":101}`, `{"depth":0}`, `{"depth":5}`,
		`{"path":"relative/dir"}`, `{"path":"/does/not/exist/anywhere"}`,
	} {
		res := call(t, e, "analyze", args)
		if res["isError"] != true {
			t.Errorf("analyze %s: want isError, got %v", args, res)
		}
		if textOf(t, res) == "" {
			t.Errorf("analyze %s: empty message", args)
		}
	}
}

func TestHistoryAndQuarantineWhenEmpty(t *testing.T) {
	e := newHome(t)
	text, data := ok(t, call(t, e, "history", `{}`))
	if text != "No runs yet." || len(data["runs"].([]any)) != 0 {
		t.Errorf("history: %q %v", text, data)
	}
	text, data = ok(t, call(t, e, "quarantine", `{}`))
	if text != "The quarantine is empty." || len(data["runs"].([]any)) != 0 || num(data["total_bytes"]) != 0 {
		t.Errorf("quarantine: %q %v", text, data)
	}
}

func TestHistoryAndQuarantineWithARun(t *testing.T) {
	e := newHome(t)
	// A run made the normal way, in the temp home, so there is something
	// in the history and in quarantine.
	p, err := cleanPlan(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	res, err := engine.Apply(context.Background(), e, p, engine.DefaultOptions())
	if err != nil || len(res.Moved) == 0 {
		t.Fatalf("apply: %v, moved %d", err, len(res.Moved))
	}

	text, data := ok(t, call(t, e, "history", `{}`))
	runs := data["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("history %v", data)
	}
	run := runs[0].(map[string]any)
	if run["id"] != res.RunID || run["kind"] != "clean" || num(run["items"]) != int64(len(res.Moved)) || !strings.Contains(text, res.RunID) {
		t.Errorf("history run %v, text %q", run, text)
	}

	text, data = ok(t, call(t, e, "quarantine", `{}`))
	held := data["runs"].([]any)
	if len(held) != 1 || held[0].(map[string]any)["id"] != res.RunID {
		t.Fatalf("quarantine %v", data)
	}
	if num(data["total_bytes"]) == 0 || held[0].(map[string]any)["expires_after"] == "" {
		t.Errorf("quarantine %v", data)
	}
	if !strings.Contains(text, res.RunID) || !strings.Contains(text, "expires after") {
		t.Errorf("text %q", text)
	}
}

func readPlan(t *testing.T, file string) *plan.Plan {
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

func selectedPaths(p *plan.Plan) []string {
	var out []string
	for _, it := range p.Items {
		if it.Selected {
			out = append(out, it.Path)
		}
	}
	slices.Sort(out)
	return out
}

// onlyPlanFileAdded checks that after the call the home holds what it did
// before, plus the state folder with plans/<one file>.
func onlyPlanFileAdded(t *testing.T, e env.Env, before map[string]string, file string) {
	t.Helper()
	after := snapshot(t, e)
	rel, _ := filepath.Rel(e.Home, file)
	added := map[string]bool{".sapu": true, ".sapu/plans": true, rel: true}
	for p, want := range before {
		if got, ok := after[p]; !ok || got != want {
			t.Errorf("%s changed: %q -> %q", p, want, got)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok && !added[p] {
			t.Errorf("new after the call: %s", p)
		}
	}
	for p := range added {
		if _, ok := after[p]; !ok {
			t.Errorf("%s was not written", p)
		}
	}
	if got := after[".sapu/plans"]; got != "dir drwx------" {
		t.Errorf("plans folder is %q, want 0700", got)
	}
	if !strings.HasPrefix(after[rel], "-rw------- ") {
		t.Errorf("plan file mode: %q", after[rel][:11])
	}
}

func TestPlanCleanWritesOnlyAPlanFile(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)

	text, data := ok(t, call(t, e, "plan_clean", `{}`))
	file := data["plan_file"].(string)
	onlyPlanFileAdded(t, e, before, file)

	if filepath.Dir(file) != filepath.Join(e.StateDir, "plans") || filepath.Ext(file) != ".json" {
		t.Errorf("plan file %s", file)
	}
	p := readPlan(t, file)
	if p.Kind != plan.KindClean || p.Home != e.Home {
		t.Errorf("plan kind %q home %q", p.Kind, p.Home)
	}
	want := []string{filepath.Join(e.Home, "Library/Caches/node-gyp"), filepath.Join(e.Home, "Library/Caches/typescript")}
	if got := selectedPaths(p); !slices.Equal(got, want) {
		t.Errorf("selected %v, want only the safe items %v", got, want)
	}
	for _, it := range p.Items {
		if it.Selected && it.Risk != rules.RiskSafe {
			t.Errorf("selected a %s item: %s", it.Risk, it.Path)
		}
	}

	if data["max_risk"] != "safe" || num(data["selected_items"]) != 2 || num(data["selected_bytes"]) == 0 {
		t.Errorf("data %v", data)
	}
	groups := data["groups"].([]any)
	if len(groups) != 2 {
		t.Errorf("groups %v", groups)
	}
	nt := data["not_touched"].(map[string]any)
	if num(nt["not_selected_items"]) == 0 {
		t.Errorf("the moderate npm cache is not selected at max_risk safe: %v", nt)
	}
	if num(nt["commands"]) != 1 {
		t.Errorf("the Go module cache is an official command, left out: %v", nt)
	}
	if want := "sapu apply " + file; data["apply_command"] != want {
		t.Errorf("apply_command %v, want %q", data["apply_command"], want)
	}
	for _, want := range []string{"Nothing has been moved", "node-gyp", file, "sapu apply " + file, "Do not run it for them"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
}

func TestPlanCleanModerate(t *testing.T) {
	e := newHome(t)
	_, data := ok(t, call(t, e, "plan_clean", `{"max_risk":"moderate"}`))
	p := readPlan(t, data["plan_file"].(string))
	if !slices.Contains(selectedPaths(p), filepath.Join(e.Home, ".npm/_cacache")) {
		t.Errorf("moderate plan selects %v, want the npm cache too", selectedPaths(p))
	}
	for _, it := range p.Items {
		if it.Selected && (it.Risk == rules.RiskRisky || it.Action != rules.ActionQuarantine) {
			t.Errorf("selected %s %s item: %s", it.Risk, it.Action, it.Path)
		}
	}
}

func TestPlanCleanRefusesRisky(t *testing.T) {
	e := newHome(t)
	before := snapshot(t, e)
	for _, args := range []string{`{"max_risk":"risky"}`, `{"max_risk":"all"}`} {
		res := call(t, e, "plan_clean", args)
		if res["isError"] != true || !strings.Contains(textOf(t, res), "max_risk") {
			t.Errorf("plan_clean %s: %v", args, res)
		}
	}
	if after := snapshot(t, e); len(after) != len(before) {
		t.Errorf("a refused plan wrote something: %v", after)
	}
}

func TestPlanCleanWithNothingToDo(t *testing.T) {
	e := env.New(t.TempDir())
	text, data := ok(t, call(t, e, "plan_clean", `{}`))
	if data["plan_file"] != "" || data["apply_command"] != "" || num(data["selected_items"]) != 0 {
		t.Errorf("data %v", data)
	}
	if !strings.Contains(text, "Nothing to plan") {
		t.Errorf("text %q", text)
	}
	if _, err := os.Lstat(e.StateDir); err == nil {
		t.Error("an empty plan wrote the state folder")
	}
}

func TestPlanPurge(t *testing.T) {
	e := newHome(t)
	code := filepath.Join(e.Home, "code")
	before := snapshot(t, e)

	text, data := ok(t, call(t, e, "plan_purge", fmt.Sprintf(`{"paths":[%q]}`, code)))
	file := data["plan_file"].(string)
	onlyPlanFileAdded(t, e, before, file)

	p := readPlan(t, file)
	want := []string{filepath.Join(code, "api/target"), filepath.Join(code, "web/node_modules")}
	if got := selectedPaths(p); !slices.Equal(got, want) || p.Kind != plan.KindPurge {
		t.Errorf("plan kind %q selects %v, want %v", p.Kind, got, want)
	}
	if data["kind"] != "purge" || num(data["selected_items"]) != 2 {
		t.Errorf("data %v", data)
	}
	for _, want := range []string{"purge:", "sapu apply " + file} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}

	// ~ works, and a project touched just now is not older than 30 days.
	_, data = ok(t, call(t, e, "plan_purge", `{"paths":["~/code"],"older_than":"30d"}`))
	if data["plan_file"] != "" || num(data["selected_items"]) != 0 {
		t.Errorf("older_than 30d: %v", data)
	}
}

func TestPlanPurgeErrors(t *testing.T) {
	e := newHome(t)
	for _, args := range []string{
		`{"paths":[]}`,
		`{"paths":["code"]}`,
		`{"paths":["~/code"],"older_than":"soon"}`,
		`{"paths":["~/code"],"max_risk":"risky"}`,
		`{"paths":["~/nowhere"]}`,
	} {
		res := call(t, e, "plan_purge", args)
		if res["isError"] != true || textOf(t, res) == "" {
			t.Errorf("plan_purge %s: want isError with a message, got %v", args, res)
		}
	}
	// Without paths the arguments do not fit the schema.
	replies := serve(t, e, lines(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"plan_purge","arguments":{"paths":"~/code"}}}`))
	if replies[0].Error == nil || replies[0].Error.Code != codeInvalidParams {
		t.Errorf("paths as a string: %+v", replies[0])
	}
}

func TestPlanFileNeverReplacesAFile(t *testing.T) {
	e := newHome(t)
	_, data := ok(t, call(t, e, "plan_clean", `{}`))
	file := data["plan_file"].(string)
	p := readPlan(t, file)

	if _, err := writePlanFile(e, p); err == nil {
		t.Error("writePlanFile replaced an existing plan")
	}
	// A symlink where the plans folder should be is refused.
	link := env.New(t.TempDir())
	if err := os.MkdirAll(link.StateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(link.StateDir, "plans")); err != nil {
		t.Fatal(err)
	}
	if _, err := writePlanFile(link, p); err == nil {
		t.Error("writePlanFile wrote through a symlinked plans folder")
	}
}

func TestSchemaIsJSON(t *testing.T) {
	for _, tl := range tools {
		var v map[string]any
		if err := json.Unmarshal([]byte(tl.schema), &v); err != nil {
			t.Errorf("%s: %v", tl.name, err)
		}
	}
}
