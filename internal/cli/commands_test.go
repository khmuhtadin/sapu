package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/pkgs"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Every command these tests let Sapu run is a script in a temp folder that
// writes a marker. No real tool is ever run.

// fakeTool writes an executable script and returns its path.
func fakeTool(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	writeFile(t, p, "#!/bin/sh\n"+body+"\n")
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// commandPlanFile writes a plan holding one cache item and one official
// command of sapu packages that runs args, and returns the file. Apply runs
// only commands Sapu makes itself, so args must be one of pkgs.Commands,
// with the program where pkgs.LookPath finds it (see fakePackages).
func commandPlanFile(t *testing.T, e env.Env, args ...string) string {
	t.Helper()
	writeFile(t, filepath.Join(e.Home, "Library/Caches/node-gyp/headers.tar"), "headers")
	writeFile(t, filepath.Join(e.Home, "Library/Caches/tool/blob"), "tool cache")
	var items []plan.Item
	for _, rel := range []string{"Library/Caches/node-gyp", "Library/Caches/tool"} {
		path := filepath.Join(e.Home, rel)
		id, err := plan.IdentityOf(path)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, plan.Item{
			Source: "test.rule", Path: path, Bytes: 4096, Risk: rules.RiskSafe, Regenerates: true,
			Explain: "Test item.", Action: rules.ActionQuarantine, Identity: id,
		})
	}
	items[1].Source = pkgs.Source
	items[1].Action = rules.ActionOfficialCommand
	items[1].Command = args
	p, err := engine.Plan(context.Background(), e, plan.KindClean, items)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "plan.json")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if err := plan.Write(f, p); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestRunCommandsFlag(t *testing.T) {
	e := env.New(t.TempDir())
	marker := filepath.Join(t.TempDir(), "ran")
	tool := fakeTool(t, "npm", `echo "cleaned the cache"; touch "`+marker+`"`)
	fakePackages(t, map[string]string{"npm": tool}, nil)
	file := commandPlanFile(t, e, tool, "cache", "clean", "--force")

	// Without the flag the command is only shown.
	out, errOut, code := sapu(t, e, "y\n", "apply", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if exists(marker) || !strings.Contains(out, "Commands you can run") || strings.Contains(errOut, "not undoable") {
		t.Errorf("without --run-commands the command must only be shown:\n%s\n%s", out, errOut)
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}

	// The confirmation lists the command and says it is not undoable.
	out, errOut, code = sapu(t, e, "n\n", "apply", file, "--run-commands")
	if code != 2 || exists(marker) {
		t.Fatalf("declined: exit %d, marker %v\n%s\n%s", code, exists(marker), out, errOut)
	}
	for _, want := range []string{
		"Sapu will run these commands after moving the items:",
		"  " + tool + " cache clean --force",
		"These commands are not undoable.",
		"to quarantine and run 1 command?",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, errOut)
		}
	}
	if !strings.Contains(out, "Commands Sapu will run") {
		t.Errorf("table:\n%s", out)
	}

	// Confirmed: the command runs after the move, and the result says so.
	out, errOut, code = sapu(t, e, "y\n", "apply", file, "--run-commands")
	if code != 0 || !exists(marker) {
		t.Fatalf("exit %d, marker %v\n%s\n%s", code, exists(marker), out, errOut)
	}
	for _, want := range []string{"Moved 1 item", "Ran 1 command (not undoable)", tool + " cache clean --force", "done (exit code 0)"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "cleaned the cache") {
		t.Errorf("the output of a command that worked is shown:\n%s", out)
	}

	if _, _, code := sapu(t, e, "", "clean", "--plan", filepath.Join(t.TempDir(), "p.json"), "--run-commands"); code != 2 {
		t.Errorf("--plan with --run-commands: exit %d, want 2", code)
	}
}

func TestRunCommandsJSONAndFailure(t *testing.T) {
	e := env.New(t.TempDir())
	tool := fakeTool(t, "npm", "echo first line; echo 'npm ERR! could not clean' >&2; exit 3")
	fakePackages(t, map[string]string{"npm": tool}, nil)
	file := commandPlanFile(t, e, tool, "cache", "clean", "--force")

	out, errOut, code := sapu(t, e, "", "apply", file, "--run-commands", "--yes")
	if code != 3 {
		t.Fatalf("exit %d, want 3\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"Ran 1 command", "failed (exit code 3)", "npm ERR! could not clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Failed (") {
		t.Errorf("the failed command is listed twice:\n%s", out)
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}

	out, _, code = sapu(t, e, "", "apply", file, "--run-commands", "--yes", "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	validate(t, "apply", out)
	var d runData
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Commands) != 1 || d.Result.Commands[0].ExitCode != 3 ||
		!strings.Contains(d.Result.Commands[0].Output, "could not clean") || len(d.Result.Failed) != 1 {
		t.Errorf("result %+v", d.Result)
	}
}

// fakePackages makes pkgs see exactly the given programs and answers.
func fakePackages(t *testing.T, programs, outputs map[string]string) {
	t.Helper()
	oldLook, oldOut := pkgs.LookPath, pkgs.Output
	t.Cleanup(func() { pkgs.LookPath, pkgs.Output = oldLook, oldOut })
	pkgs.LookPath = func(name string) (string, error) {
		if p, ok := programs[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
	pkgs.Output = func(_ context.Context, _, program string, args ...string) (string, error) {
		if out, ok := outputs[strings.Join(append([]string{program}, args...), " ")]; ok {
			return out, nil
		}
		return "", errors.New("exit status 1")
	}
}

// packagesHome has an npm cache, a brew cache and three nvm versions, with
// v20 as the default. npm, brew and docker are fake scripts.
func packagesHome(t *testing.T) (e env.Env, markers map[string]string) {
	t.Helper()
	e = env.New(t.TempDir())
	h := func(rel string) string { return filepath.Join(e.Home, rel) }
	writeFile(t, h(".npm/_cacache/content-v2/blob"), "npm blob")
	writeFile(t, h("Library/Caches/Homebrew/downloads/x.tar.gz"), "bottle")
	for _, v := range []string{"v18.20.1", "v20.11.1", "v22.1.0"} {
		writeFile(t, h(".nvm/versions/node/"+v+"/bin/node"), "node "+v)
	}
	writeFile(t, h(".nvm/alias/default"), "20\n")
	dir := t.TempDir()
	markers = map[string]string{}
	programs := map[string]string{}
	for _, name := range []string{"npm", "brew", "docker"} {
		markers[name] = filepath.Join(dir, name+"-ran")
		programs[name] = fakeTool(t, name, `touch "`+markers[name]+`"`)
	}
	fakePackages(t, programs, map[string]string{programs["brew"] + " --cache": h("Library/Caches/Homebrew")})
	return e, markers
}

func TestPackagesPlan(t *testing.T) {
	e, markers := packagesHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "packages", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "packages", out)
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	for name, m := range markers {
		if exists(m) {
			t.Errorf("planning ran %s", name)
		}
	}

	p := readPlanFile(t, file)
	if p.Kind != plan.KindPackages {
		t.Errorf("kind %q", p.Kind)
	}
	selected := map[string]bool{} // base name of the command or path -> selected
	for _, it := range p.Items {
		if it.Source != "packages" {
			t.Errorf("source %q", it.Source)
		}
		key := filepath.Base(it.Path)
		if it.Action == rules.ActionOfficialCommand {
			key = filepath.Base(it.Command[0]) + " " + strings.Join(it.Command[1:], " ")
		}
		selected[key] = it.Selected
	}
	want := map[string]bool{
		"npm cache clean --force":     true,
		"brew cleanup --prune=all -s": true,
		"docker system prune":         false,
		"v18.20.1":                    false,
		"v22.1.0":                     false,
	}
	if len(selected) != len(want) {
		t.Errorf("items %v, want %v", selected, want)
	}
	for k, sel := range want {
		if got, ok := selected[k]; !ok || got != sel {
			t.Errorf("%s: selected %v (present %v), want %v", k, got, ok, sel)
		}
	}
	if _, ok := selected["v20.11.1"]; ok {
		t.Error("the default nvm version is offered")
	}

	out, _, code = sapu(t, e, "", "packages", "--plan", file)
	if code != 0 || !strings.Contains(out, "2 commands selected") || !strings.Contains(out, "sapu apply "+file+" --run-commands") {
		t.Errorf("exit %d\n%s", code, out)
	}
}

func TestPackagesRunsToolsAfterConfirming(t *testing.T) {
	e, markers := packagesHome(t)

	out, errOut, code := sapu(t, e, "n\n", "packages")
	if code != 2 || exists(markers["npm"]) {
		t.Fatalf("declined: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{"Sapu will run these commands:", "These commands are not undoable.", "is what sapu packages is for", "Run 2 commands?"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, errOut)
		}
	}
	for _, want := range []string{"Commands Sapu will run", "Commands not selected", "docker system prune", "Not selected — use --risk risky to select them", "v18.20.1", ".nvmrc"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}

	out, errOut, code = sapu(t, e, "y\n", "packages")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu packages, answering y\n--- stdout\n%s--- stderr\n%s", out, errOut)
	if !exists(markers["npm"]) || !exists(markers["brew"]) || exists(markers["docker"]) {
		t.Errorf("ran npm %v, brew %v, docker %v; want npm and brew only", exists(markers["npm"]), exists(markers["brew"]), exists(markers["docker"]))
	}
	for _, v := range []string{"v18.20.1", "v20.11.1", "v22.1.0"} {
		if !exists(filepath.Join(e.Home, ".nvm/versions/node", v)) {
			t.Errorf("%s was moved without --risk risky", v)
		}
	}
	if !strings.Contains(out, "Ran 2 commands") {
		t.Errorf("result:\n%s", out)
	}

	// --risk risky moves the old versions, never the default, and still not docker.
	if err := os.Remove(markers["docker"]); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out, errOut, code = sapu(t, e, "", "packages", "--risk", "risky", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for v, kept := range map[string]bool{"v18.20.1": false, "v20.11.1": true, "v22.1.0": false} {
		if got := exists(filepath.Join(e.Home, ".nvm/versions/node", v)); got != kept {
			t.Errorf("%s exists %v, want %v", v, got, kept)
		}
	}
	if exists(markers["docker"]) {
		t.Error("docker system prune ran")
	}
}

// fakeOptimize makes optimize find the given programs.
func fakeOptimize(t *testing.T, programs map[string]string) {
	t.Helper()
	old := optimize.LookPath
	t.Cleanup(func() { optimize.LookPath = old })
	optimize.LookPath = func(name string) (string, error) {
		if p, ok := programs[name]; ok {
			return p, nil
		}
		return "", errors.New("not found")
	}
}

func TestOptimize(t *testing.T) {
	e := env.New(t.TempDir())
	dir := t.TempDir()
	ql := fakeTool(t, "qlmanage", `echo "$*" > "`+filepath.Join(dir, "qlmanage")+`"`)
	ats := fakeTool(t, "atsutil", `echo "$*" > "`+filepath.Join(dir, "atsutil")+`"`)
	fakeOptimize(t, map[string]string{"qlmanage": ql, "atsutil": ats})

	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "optimize", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "optimize", out)
	var d runData
	decodeData(t, out, &d)
	if len(d.Admin) != 2 || d.Admin[0].Command != "sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder" || d.Admin[1].Command != "sudo mdutil -E /" {
		t.Errorf("admin %+v", d.Admin)
	}
	p := readPlanFile(t, file)
	var cmds []string
	for _, it := range p.Items {
		if it.Action != rules.ActionOfficialCommand || !it.Selected || it.Risk != rules.RiskSafe || it.Source != "optimize" {
			t.Errorf("item %+v", it)
		}
		cmds = append(cmds, strings.Join(it.Command, " "))
	}
	if want := []string{ql + " -r cache", ats + " databases -removeUser"}; !slices.Equal(cmds, want) {
		t.Errorf("commands %q, want %q", cmds, want)
	}
	noState(t, e)

	out, errOut, code := sapu(t, e, "y\n", "optimize")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"makes your Mac faster in general",
		"Nothing to roll back: these only clear caches macOS rebuilds.",
		"Commands Sapu will run", "QuickLook", "log out",
		"Needs admin — Sapu never runs sudo", "sudo mdutil -E /", "Flush the DNS cache",
		"Ran 2 commands",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, "These commands are not undoable.") || !strings.Contains(errOut, "Run 2 commands?") {
		t.Errorf("confirmation:\n%s", errOut)
	}
	if strings.Contains(out, "Nothing was moved") || strings.Contains(out, "Undo:") {
		t.Errorf("optimize talks about moves:\n%s", out)
	}
	for name, args := range map[string]string{"qlmanage": "-r cache\n", "atsutil": "databases -removeUser\n"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != args {
			t.Errorf("%s ran with %q (%v), want %q", name, got, err, args)
		}
	}
}

func TestPlanTableWithRunCommands(t *testing.T) {
	run := item("packages", ".npm/_cacache", 2_000_000, rules.RiskModerate, true)
	run.Action = rules.ActionOfficialCommand
	run.Command = []string{"/bin/npm", "cache", "clean", "--force"}
	docker := item("packages", "bin/docker", 0, rules.RiskRisky, false)
	docker.Action = rules.ActionOfficialCommand
	docker.Command = []string{"/bin/docker", "system", "prune"}
	p := &plan.Plan{Home: testHome, Items: []plan.Item{run, docker}}

	var buf bytes.Buffer
	writePlanTable(&buf, p, nil, false, true)
	out := buf.String()
	for _, want := range []string{"Commands Sapu will run", "not undoable", "/bin/npm cache clean --force", "2.0 MB", "Commands not selected", "/bin/docker system prune"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Nothing found") || strings.Contains(out, "0 B") {
		t.Errorf("table:\n%s", out)
	}

	// Without running, unselected commands stay hidden as before.
	buf.Reset()
	writePlanTable(&buf, p, nil, false, false)
	if out := buf.String(); strings.Contains(out, "docker") || !strings.Contains(out, "Commands you can run") {
		t.Errorf("table:\n%s", out)
	}
}

func TestWriteResultWithCommands(t *testing.T) {
	ok := item("packages", "a", 1, rules.RiskSafe, true)
	ok.ID, ok.Action, ok.Command = "0001", rules.ActionOfficialCommand, []string{"/bin/brew", "cleanup"}
	bad := item("packages", "b", 1, rules.RiskSafe, true)
	bad.ID, bad.Action, bad.Command = "0002", rules.ActionOfficialCommand, []string{"/bin/npm", "cache", "clean", "--force"}
	refused := item("packages", "c", 1, rules.RiskSafe, true)
	refused.ID, refused.Action, refused.Command = "0003", rules.ActionOfficialCommand, []string{"sudo", "rm", "-rf", "/"}
	res := &engine.Result{
		RunID: "20261108-120000-abcd",
		Commands: []engine.CommandResult{
			{Item: ok, Output: "Removing: lots\n", Duration: 3 * time.Second},
			{Item: bad, ExitCode: 1, Output: strings.Repeat("noise\n", 20) + "npm ERR! code EPERM\nnpm ERR! path /x\n", Duration: time.Second},
		},
		Failed: []engine.Failure{{Item: bad, Error: "exit code 1"}, {Item: refused, Error: "refused: Sapu never runs sudo"}},
	}
	var buf bytes.Buffer
	writeResult(&buf, testHome, res, config.Defaults(), time.Now(), false, false)
	out := buf.String()
	for _, want := range []string{
		"Ran 2 commands (not undoable)",
		"/bin/brew cleanup", "done (exit code 0)", "3s",
		"/bin/npm cache clean --force", "failed (exit code 1)", "npm ERR! code EPERM", "npm ERR! path /x",
		"Failed (1)", "sudo rm -rf / (refused: Sapu never runs sudo)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Removing: lots") || strings.Count(out, "noise") > maxOutputLines || strings.Contains(out, "Nothing was moved") {
		t.Errorf("result:\n%s", out)
	}
}
