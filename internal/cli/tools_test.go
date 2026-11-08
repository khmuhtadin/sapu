package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// runTool runs the plan of tool id in e, checks that it changed nothing,
// and that the review screen and engine.Apply can act on every item: each
// one is moved to quarantine, or is an official command the screen only
// shows.
func planTool(t *testing.T, e env.Env, id string, dirs []string, app string) *plan.Plan {
	t.Helper()
	tl, ok := toolByID(id)
	if !ok {
		t.Fatalf("no tool %q", id)
	}
	before := tidySnapshot(t, e)
	p, err := tl.Plan(e, dirs, app)(context.Background(), nil)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	sameSnapshot(t, before, tidySnapshot(t, e))
	noState(t, e)
	for _, it := range p.Items {
		if it.Action != rules.ActionQuarantine && it.Action != rules.ActionOfficialCommand {
			t.Errorf("%s: %s has action %q, which the review screen cannot move", id, it.Path, it.Action)
		}
	}
	return p
}

// typedPlan runs `sapu ARGS --plan FILE` and returns the plan it wrote.
func typedPlan(t *testing.T, e env.Env, args ...string) *plan.Plan {
	t.Helper()
	file := filepath.Join(t.TempDir(), "plan.json")
	out, errOut, code := sapu(t, e, "", append(args, "--plan", file)...)
	if code != 0 {
		t.Fatalf("%v --plan: exit %d\n%s\n%s", args, code, out, errOut)
	}
	return readPlanFile(t, file)
}

// planShape is what a person sees of a plan: each item with its selection
// and action, and the paths left out, sorted.
func planShape(p *plan.Plan) []string {
	var out []string
	for _, it := range p.Items {
		out = append(out, fmt.Sprintf("%s selected=%v %s requires=%v", it.Path, it.Selected, it.Action, it.Requires != ""))
	}
	for _, s := range p.Skipped {
		out = append(out, "skip "+s.Path+" "+s.Reason)
	}
	slices.Sort(out)
	return out
}

func samePlan(t *testing.T, name string, tool, typed *plan.Plan) {
	t.Helper()
	if tool.Kind != typed.Kind {
		t.Errorf("%s: kind %q, the command's is %q", name, tool.Kind, typed.Kind)
	}
	if got, want := planShape(tool), planShape(typed); !slices.Equal(got, want) {
		t.Errorf("%s: the tool plans\n  %s\nthe command plans\n  %s", name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func itemPaths(p *plan.Plan) []string {
	var paths []string
	for _, it := range p.Items {
		paths = append(paths, it.Path)
	}
	slices.Sort(paths)
	return paths
}

func TestToolsHaveAPlanAndTheRightFolders(t *testing.T) {
	ids := []string{"uninstall", "purge", "large", "dupes", "similar", "installer", "tidy", "trash", "backups", "login-items"}
	var got []string
	for _, tl := range tools {
		got = append(got, tl.ID)
		if tl.Plan == nil {
			t.Errorf("%s has no Plan", tl.ID)
		}
		if tl.Folders != (tl.DefaultDirs != nil) {
			t.Errorf("%s: Folders is %v but DefaultDirs is set: %v", tl.ID, tl.Folders, tl.DefaultDirs != nil)
		}
	}
	if !slices.Equal(got, ids) {
		t.Errorf("tools %v, want %v", got, ids)
	}

	e := env.New(t.TempDir())
	for _, dir := range []string{"Downloads", "Desktop", "Documents", "Pictures", "Movies", "Music"} {
		if err := os.MkdirAll(filepath.Join(e.Home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	h := func(rels ...string) []string {
		var out []string
		for _, rel := range rels {
			out = append(out, filepath.Join(e.Home, rel))
		}
		return out
	}
	for id, want := range map[string][]string{
		"purge":     {e.Home}, // no project folder and no sapu.toml
		"large":     {e.Home},
		"dupes":     h("Downloads", "Desktop", "Documents", "Pictures", "Movies", "Music"),
		"similar":   h("Pictures", "Downloads", "Desktop"),
		"installer": h("Downloads", "Desktop"),
		"tidy":      h("Downloads", "Desktop", "Documents"),
	} {
		tl, _ := toolByID(id)
		dirs, err := tl.DefaultDirs(e)
		if err != nil || !slices.Equal(dirs, want) {
			t.Errorf("%s: DefaultDirs %v, %v; want %v", id, dirs, err, want)
		}
	}
}

func TestPurgeToolDefaultDirs(t *testing.T) {
	e := env.New(t.TempDir())
	for _, dir := range []string{"Projects", "code", "repos", "Music"} {
		if err := os.MkdirAll(filepath.Join(e.Home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(e.Home, "work"), "a file, not a folder")
	dirs, err := purgeDefaultDirs(e)
	want := []string{filepath.Join(e.Home, "Projects"), filepath.Join(e.Home, "code"), filepath.Join(e.Home, "repos")}
	if err != nil || !slices.Equal(dirs, want) {
		t.Errorf("project folders: %v, %v; want %v", dirs, err, want)
	}

	// [purge] roots of sapu.toml win.
	writeSettings(t, e, "[purge]\nroots = [\"~/Music\"]\n")
	dirs, err = purgeDefaultDirs(e)
	if err != nil || !slices.Equal(dirs, []string{filepath.Join(e.Home, "Music")}) {
		t.Errorf("sapu.toml roots: %v, %v", dirs, err)
	}
}

func TestPurgeToolPlansLikePurge(t *testing.T) {
	e, code := newProjects(t)
	p := planTool(t, e, "purge", nil, "") // the default folders: ~/code
	if len(p.Items) == 0 {
		t.Fatal("the purge tool found nothing in ~/code")
	}
	samePlan(t, "purge", p, typedPlan(t, e, "purge", code))
	samePlan(t, "purge in dirs", planTool(t, e, "purge", []string{filepath.Join(code, "web")}, ""), typedPlan(t, e, "purge", filepath.Join(code, "web")))
}

func TestInstallerToolPlansLikeInstaller(t *testing.T) {
	e, _ := installerHome(t)
	p := planTool(t, e, "installer", nil, "")
	if len(selectedPaths(p)) != 4 {
		t.Errorf("selected %v, want the 4 old installers", selectedPaths(p))
	}
	samePlan(t, "installer", p, typedPlan(t, e, "installer"))
}

func TestTrashToolPlansLikeTrash(t *testing.T) {
	e := trashHome(t)
	p := planTool(t, e, "trash", nil, "")
	if len(selectedPaths(p)) != 4 {
		t.Errorf("selected %v, want everything in the Trash", selectedPaths(p))
	}
	samePlan(t, "trash", p, typedPlan(t, e, "trash"))
}

func TestUninstallToolPlansLikeUninstall(t *testing.T) {
	e, app := slackHome(t)
	for _, query := range []string{"Slack", app, slackID} {
		p := planTool(t, e, "uninstall", nil, query)
		if !slices.Contains(selectedPaths(p), app) {
			t.Errorf("%s: the app is not selected", query)
		}
		samePlan(t, "uninstall "+query, p, typedPlan(t, e, "uninstall", "Slack"))
	}

	tl, _ := toolByID("uninstall")
	if _, err := tl.Plan(e, nil, "")(context.Background(), nil); err == nil {
		t.Error("uninstall without an app should fail")
	}
	if _, err := tl.Plan(e, nil, "NoSuchApp")(context.Background(), nil); err == nil {
		t.Error("an app that is not installed should fail")
	}
}

// login-items lists every login item of the user; only the orphan starts
// selected, as with disable --orphans. Items of other scopes are not listed.
func TestLoginItemsToolSelectsOrphans(t *testing.T) {
	e, user, _ := loginHome(t)
	p := planTool(t, e, "login-items", nil, "")
	want := []string{
		filepath.Join(user, "com.example.active.plist"),
		filepath.Join(user, "com.example.gone.plist"),
		filepath.Join(user, "com.example.idle.plist"),
	}
	if got := itemPaths(p); !slices.Equal(got, want) {
		t.Errorf("items %v, want %v", got, want)
	}
	orphans := typedPlan(t, e, "login-items", "disable", "--orphans")
	if got, want := selectedPaths(p), selectedPaths(orphans); !slices.Equal(got, want) || len(got) != 1 {
		t.Errorf("selected %v, disable --orphans selects %v", got, want)
	}
	if p.Kind != plan.KindLoginItems {
		t.Errorf("kind %q", p.Kind)
	}
}

// Tools where a person must choose start with nothing selected.

func TestBackupsToolSelectsNothing(t *testing.T) {
	e := backupsHome(t)
	p := planTool(t, e, "backups", nil, "")
	if len(p.Items) != 3 || len(selectedPaths(p)) != 0 {
		t.Errorf("items %v, selected %v; want 3 backups, none selected", itemPaths(p), selectedPaths(p))
	}
}

func TestLargeToolSelectsNothing(t *testing.T) {
	e := largeHome(t)
	// The defaults (500MB, 90 days) leave out these small files.
	if p := planTool(t, e, "large", nil, ""); len(p.Items) != 0 {
		t.Errorf("the large tool with its defaults found %v", itemPaths(p))
	}
	// What the tool shares with sapu large, with a smaller size.
	out, errOut, code := sapu(t, e, "", "large", "--min-size", "100KB", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	var listed foundList[largeJSON]
	decodeData(t, out, &listed)
	opts := largeTestOptions(t)
	p, files, err := planLarge(context.Background(), e, []string{e.Home}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || len(listed.Items) != 3 || len(selectedPaths(p)) != 0 {
		t.Errorf("files %d, listed %d, selected %v; want 3, 3, none", len(files), len(listed.Items), selectedPaths(p))
	}
	for i, f := range files {
		if f.Item.Path != listed.Items[i].Path {
			t.Errorf("file %d is %s, sapu large lists %s", i+1, f.Item.Path, listed.Items[i].Path)
		}
	}
}

// largeTestOptions are the large tool's options with a 100 KB minimum.
func largeTestOptions(t *testing.T) finders.LargeOptions {
	t.Helper()
	opts := finders.LargeOptions{MinSize: 100_000, Type: "any", Now: time.Now()}
	var err error
	if opts.OlderThan, err = finders.ParseAge(largeOlderThan); err != nil {
		t.Fatal(err)
	}
	return opts
}

func TestTidyToolPlansLikeTidy(t *testing.T) {
	e := tidyHome(t)
	p := planTool(t, e, "tidy", nil, "")
	if got := itemPaths(p); !slices.Equal(got, tidyWant(e)) || len(selectedPaths(p)) != 0 {
		t.Errorf("items %v selected %v; want %v, none selected", got, selectedPaths(p), tidyWant(e))
	}
	samePlan(t, "tidy", p, typedPlan(t, e, "tidy"))
}

func TestSimilarToolPlansLikeSimilar(t *testing.T) {
	fakeSips(t)
	e := similarHome(t)
	p := planTool(t, e, "similar", nil, "")
	if len(p.Items) != 2 || len(selectedPaths(p)) != 0 {
		t.Errorf("items %v selected %v; want 2 look-alikes, none selected", itemPaths(p), selectedPaths(p))
	}
	samePlan(t, "similar", p, typedPlan(t, e, "similar"))
}

// dupesToolHome has three copies of a 1.2 MB file (the dupes tool skips
// files under 1 MB), the oldest in Downloads, and a small pair.
func dupesToolHome(t *testing.T) (e env.Env, keep string, extras []string) {
	t.Helper()
	e = env.New(t.TempDir())
	big := bytes.Repeat([]byte("d"), 1_200_000)
	keep = dupesFile(t, e, "Downloads/movie.mov", big)
	extras = []string{
		dupesFile(t, e, "Desktop/movie copy.mov", big),
		dupesFile(t, e, "Documents/movie.mov", big),
	}
	dupesFile(t, e, "Documents/a.txt", []byte("small"))
	dupesFile(t, e, "Pictures/a.txt", []byte("small"))
	slices.Sort(extras)
	return e, keep, extras
}

// The dupes tool keeps one copy of each group, even when the user selects
// every item: the copy that stays is never an item.
func TestDupesToolKeepsOneCopy(t *testing.T) {
	e, keep, extras := dupesToolHome(t)
	p := planTool(t, e, "dupes", nil, "")
	if got := itemPaths(p); !slices.Equal(got, extras) {
		t.Fatalf("items %v, want the extras %v", got, extras)
	}
	if got := selectedPaths(p); !slices.Equal(got, extras) {
		t.Errorf("selected %v, want every extra", got)
	}
	for _, it := range p.Items {
		if it.KeepCopy == nil || it.KeepCopy.Path != keep {
			t.Errorf("%s keeps %v, want %s", it.Path, it.KeepCopy, keep)
		}
	}
	samePlan(t, "dupes", p, typedPlan(t, e, "dupes"))

	// Select everything, as a user may on the review screen, and apply.
	for i := range p.Items {
		p.Items[i].Selected = true
	}
	res, err := engine.Apply(context.Background(), e, p, engine.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 2 || len(res.Failed) != 0 {
		t.Fatalf("moved %d, failed %v", len(res.Moved), res.Failed)
	}
	data, err := os.ReadFile(keep)
	if err != nil || len(data) != 1_200_000 {
		t.Errorf("the copy that stays is gone: %v", err)
	}
	for _, x := range extras {
		if exists(x) {
			t.Errorf("%s was not moved", x)
		}
	}
}

func TestToolsList(t *testing.T) {
	e := env.New(t.TempDir())
	if err := os.MkdirAll(filepath.Join(e.Home, "Projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := tidySnapshot(t, e)
	out, errOut, code := sapu(t, e, "", "tools")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu tools\n%s", out)
	for _, want := range []string{"uninstall", "an app you pick", "purge", "~/Projects", "login-items", "Stop things from starting at login"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	out, errOut, code = sapu(t, e, "", "tools", "--json")
	if code != 0 {
		t.Fatalf("--json: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "tools", out)
	var data toolsData
	decodeData(t, out, &data)
	if len(data.Tools) != len(tools) {
		t.Fatalf("%d tools, want %d", len(data.Tools), len(tools))
	}
	for i, got := range data.Tools {
		tl := tools[i]
		if got.ID != tl.ID || got.Title != tl.Title || got.Folders != tl.Folders || got.App != tl.App {
			t.Errorf("tool %d: %+v", i, got)
		}
		if !tl.Folders && len(got.DefaultDirs) != 0 {
			t.Errorf("%s has no folders, but default_dirs %v", got.ID, got.DefaultDirs)
		}
	}
	if purge := data.Tools[1]; !slices.Equal(purge.DefaultDirs, []string{filepath.Join(e.Home, "Projects")}) {
		t.Errorf("purge default_dirs %v", purge.DefaultDirs)
	}
	if !strings.Contains(out, `"default_dirs": []`) {
		t.Errorf("a tool without folders must have an empty list, not null:\n%s", out)
	}
	sameSnapshot(t, before, tidySnapshot(t, e))
	noState(t, e)
}

func TestToolsApps(t *testing.T) {
	e, app := slackHome(t)
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Notes.app", "com.apple.Notes", "Notes")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications", "Tools"), "Arc.app", "company.thebrowser.Browser", "Arc")
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "tools", "apps")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !strings.Contains(out, "Slack") || !strings.Contains(out, "Arc") || strings.Contains(out, "Notes") {
		t.Errorf("want Slack and Arc, not Apple's Notes:\n%s", out)
	}

	out, errOut, code = sapu(t, e, "", "tools", "apps", "--json")
	if code != 0 {
		t.Fatalf("--json: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "tools-apps", out)
	var data toolsAppsData
	decodeData(t, out, &data)
	want := []toolApp{
		{Name: "Arc", BundleID: "company.thebrowser.Browser", Path: filepath.Join(e.Home, "Applications", "Tools", "Arc.app")},
		{Name: "Slack", BundleID: slackID, Path: app},
	}
	if !slices.Equal(data.Apps, want) {
		t.Errorf("apps %+v, want %+v", data.Apps, want)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	// No app at all is an empty list, not null.
	pointAppDirs(t, filepath.Join(t.TempDir(), "none"))
	out, _, code = sapu(t, e, "", "tools", "apps", "--json")
	if code != 0 || !strings.Contains(out, `"apps": []`) {
		t.Errorf("exit %d\n%s", code, out)
	}
	validate(t, "tools-apps", out)
}

// tools plan prints the same plan document as clean --plan -, changes
// nothing, and sapu apply - can apply it.
func TestToolsPlanPrintsThePlan(t *testing.T) {
	e, _ := installerHome(t)
	before := snapshot(t, e)
	out, errOut, code := sapu(t, e, "", "tools", "plan", "installer")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	check(t, "plan", doc)
	p, err := plan.Read(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	samePlan(t, "tools plan installer", p, typedPlan(t, e, "installer"))

	out, errOut, code = sapu(t, e, out, "apply", "-", "--yes")
	if code != 0 || !strings.Contains(out, "Moved 4 items") {
		t.Fatalf("apply: exit %d\n%s\n%s", code, out, errOut)
	}
	for _, path := range selectedPaths(p) {
		if exists(path) {
			t.Errorf("%s was not moved", path)
		}
	}
}

func TestToolsPlanFoldersAppAndProgress(t *testing.T) {
	e, keep, extras := dupesToolHome(t)
	before := snapshot(t, e)
	out, errOut, code := sapu(t, e, "", "tools", "plan", "dupes", filepath.Join(e.Home, "Desktop"), filepath.Join(e.Home, "Downloads"), "--progress", "ndjson")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	p, err := plan.Read(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if got := itemPaths(p); !slices.Equal(got, extras[:1]) || p.Items[0].KeepCopy.Path != keep {
		t.Errorf("in Desktop and Downloads: items %v, want %v", got, extras[:1])
	}
	lines := strings.Split(strings.TrimSpace(errOut), "\n")
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil || ev["type"] != "progress" {
			t.Errorf("stderr line is not a progress event: %q", line)
		}
	}
	if errOut == "" {
		t.Error("--progress ndjson wrote no event")
	}

	e, app := slackHome(t)
	out, errOut, code = sapu(t, e, "", "tools", "plan", "uninstall", "--app", app)
	if code != 0 {
		t.Fatalf("uninstall: exit %d\n%s\n%s", code, out, errOut)
	}
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	check(t, "plan", doc)
	p, err = plan.Read(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	samePlan(t, "tools plan uninstall", p, typedPlan(t, e, "uninstall", "Slack"))
}

func TestToolsPlanUsageErrors(t *testing.T) {
	e := env.New(t.TempDir())
	for _, args := range [][]string{
		{"tools", "plan"},
		{"tools", "plan", "nope"},
		{"tools", "plan", "trash", e.Home},           // trash takes no folders
		{"tools", "plan", "uninstall"},               // needs --app
		{"tools", "plan", "purge", "--app", "Slack"}, // --app is for uninstall
		{"tools", "plan", "large", filepath.Join(e.Home, "missing")},
		{"tools", "plan", "trash", "--json"},
	} {
		out, errOut, code := sapu(t, e, "", args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2\n%s\n%s", args, code, out, errOut)
		}
	}
	noState(t, e)
}

// With a terminal, the flags that need the text flow still get it.
func TestToolCommandsKeepTheTextFlowOnATerminal(t *testing.T) {
	fakeTerminal(t)

	e, _ := installerHome(t)
	out, errOut, code := sapu(t, e, "", "installer", "--plan", filepath.Join(t.TempDir(), "p.json"))
	if code != 0 || !strings.Contains(out, "Plan written to") {
		t.Fatalf("installer --plan: exit %d\n%s\n%s", code, out, errOut)
	}

	e = largeHome(t)
	out, errOut, code = sapu(t, e, "", "large", "--min-size", "100KB", "--pick", "1", "--plan", filepath.Join(t.TempDir(), "p.json"))
	if code != 0 || !strings.Contains(out, "Plan written to") {
		t.Fatalf("large --pick --plan: exit %d\n%s\n%s", code, out, errOut)
	}
	out, errOut, code = sapu(t, e, "", "large", "--min-size", "100KB", "--json")
	if code != 0 {
		t.Fatalf("large --json: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "large", out)

	// --replace-with-clone asks its own question, so it keeps the text flow.
	e, _, _ = dupesToolHome(t)
	before := snapshot(t, e)
	out, errOut, code = sapu(t, e, "n\n", "dupes", "--replace-with-clone")
	if code != 2 || !strings.Contains(errOut, "Replace 2 extra copies") {
		t.Fatalf("dupes --replace-with-clone: exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
}
