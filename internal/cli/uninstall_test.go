package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

const slackID = "com.tinyspeck.slackmacgap"

// pointAppDirs makes sapu uninstall look for apps only in dirs.
func pointAppDirs(t *testing.T, dirs ...string) {
	t.Helper()
	old := apps.Dirs
	apps.Dirs = dirs
	t.Cleanup(func() { apps.Dirs = old })
}

// fakeAppBundle builds dir/file with a real Info.plist and an executable.
func fakeAppBundle(t *testing.T, dir, file, id, name string) string {
	t.Helper()
	p := filepath.Join(dir, file)
	exe := strings.TrimSuffix(file, ".app")
	writeFile(t, filepath.Join(p, "Contents", "Info.plist"), `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>`+id+`</string>
<key>CFBundleName</key><string>`+name+`</string>
<key>CFBundleExecutable</key><string>`+exe+`</string>
<key>CFBundleShortVersionString</key><string>4.41</string>
</dict></plist>
`)
	writeFile(t, filepath.Join(p, "Contents", "MacOS", exe), "#!/bin/sh\n")
	writeFile(t, filepath.Join(p, "Contents", "Resources", "big.bin"), strings.Repeat("x", 10000))
	return p
}

// slackLeftovers are files in ~/Library named after Slack's bundle id.
var slackLeftovers = []string{
	"Library/Application Support/" + slackID + "/data.json",
	"Library/Caches/" + slackID + "/Cache.db",
	"Library/Preferences/" + slackID + ".plist",
	"Library/Preferences/ByHost/" + slackID + ".0F3A-11AA.plist",
	"Library/Containers/" + slackID + "/Data/x",
	"Library/Group Containers/BQR82RBBHL." + slackID + "/x",
	"Library/Saved Application State/" + slackID + ".savedState/windows.plist",
	"Library/HTTPStorages/" + slackID + ".binarycookies",
	"Library/WebKit/" + slackID + "/x",
	"Library/Logs/" + slackID + "/app.log",
	"Library/Cookies/" + slackID + ".binarycookies",
	"Library/LaunchAgents/" + slackID + ".ShipIt.plist",
	"Library/Application Scripts/" + slackID + "/x",
}

// slackKeeps must survive every uninstall of Slack.
var slackKeeps = []string{
	"Library/Application Support/Slack/data.json", // a name match: never selected by default
	"Library/Caches/" + slackID + "2/x",           // another bundle id
	"Library/Preferences/" + slackID + ".helper.plist",
	"Library/LaunchAgents/" + slackID + "2.plist",
	"Documents/Slack notes.txt",
}

// slackHome builds a home with Slack in ~/Applications, its leftovers and
// files that must stay.
func slackHome(t *testing.T) (env.Env, string) {
	t.Helper()
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	app := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Slack.app", slackID, "Slack")
	for _, rel := range append(slices.Clone(slackLeftovers), slackKeeps...) {
		writeFile(t, filepath.Join(e.Home, rel), "data of "+rel)
	}
	return e, app
}

// topOf is the leftover path that holds rel: its entry in its Library folder.
func topOf(home, rel string) string {
	parts := strings.Split(rel, "/")
	n := 3 // Library/<folder>/<entry>
	if parts[1] == "Preferences" && parts[2] == "ByHost" {
		n = 4
	}
	return filepath.Join(home, filepath.Join(parts[:n]...))
}

func TestUninstallPlanSelectsAppAndBundleIDLeftovers(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	if !strings.Contains(out, "Slack 4.41 · "+slackID) || !strings.Contains(out, "~/Applications/Slack.app") {
		t.Errorf("output does not name the app:\n%s", out)
	}

	p := readPlanFile(t, file)
	want := []string{app}
	for _, rel := range slackLeftovers {
		want = append(want, topOf(e.Home, rel))
	}
	slices.Sort(want)
	if got := selectedPaths(p); !slices.Equal(got, want) {
		t.Errorf("selected:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if p.Kind != plan.KindUninstall {
		t.Errorf("kind %q", p.Kind)
	}
	nameMatch := filepath.Join(e.Home, "Library/Application Support/Slack")
	found := false
	for _, it := range p.Items {
		if it.Source != "uninstall" || it.Explain == "" || it.Bytes <= 0 {
			t.Errorf("item %+v", it)
		}
		if len(it.ProtectIf) != 1 || it.ProtectIf[0].ProcessRunning != "Slack" {
			t.Errorf("%s is not skipped while Slack runs: %+v", it.Path, it.ProtectIf)
		}
		if it.Path == nameMatch {
			found = true
			if it.Selected || it.Risk != rules.RiskRisky {
				t.Errorf("name match: %+v", it)
			}
		} else if it.Risk != rules.RiskModerate {
			t.Errorf("%s has risk %s", it.Path, it.Risk)
		}
	}
	if !found {
		t.Error("the name match is not in the plan")
	}

	// --risk risky selects the name match too.
	if _, _, code := sapu(t, e, "", "uninstall", "Slack", "--plan", file, "--risk", "risky"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := selectedPaths(readPlanFile(t, file)); !slices.Contains(got, nameMatch) {
		t.Errorf("--risk risky does not select the name match: %v", got)
	}
}

func TestUninstallMovesAppAndLeftoversAndUndoRestores(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "uninstall", app, "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu uninstall --yes\n%s", out)
	for _, line := range []string{
		"The Slack app itself", "Cache of Slack.", "Named after its bundle id",
		"~/Library/LaunchAgents/" + slackID + ".ShipIt.plist", // every item is listed
		"To move", "Not selected — use --risk risky to select them", // the name match
		"Moved 14 items", "Undo: `sapu undo",
	} {
		if !strings.Contains(out, line) {
			t.Errorf("output lacks %q", line)
		}
	}
	if exists(app) {
		t.Error("the app was not moved")
	}
	for _, rel := range slackLeftovers {
		if exists(topOf(e.Home, rel)) {
			t.Errorf("%s was not moved", rel)
		}
	}
	for _, rel := range slackKeeps {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s was moved", rel)
		}
	}

	if out, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatalf("undo: exit %d\n%s", code, out)
	}
	sameSnapshot(t, before, snapshot(t, e))
}

func TestUninstallAsksFirst(t *testing.T) {
	e, _ := slackHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "uninstall", "Slack"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestUninstallFindsTheApp(t *testing.T) {
	e, app := slackHome(t)
	for _, query := range []string{"slack", "Slack.app", strings.ToUpper(slackID), app} {
		file := filepath.Join(t.TempDir(), "plan.json")
		if out, errOut, code := sapu(t, e, "", "uninstall", query, "--plan", file); code != 0 {
			t.Errorf("%q: exit %d\n%s\n%s", query, code, out, errOut)
			continue
		}
		if !slices.Contains(selectedPaths(readPlanFile(t, file)), app) {
			t.Errorf("%q: the app is not selected", query)
		}
	}
	_, errOut, code := sapu(t, e, "", "uninstall", "Slac")
	if code != 1 || !strings.Contains(errOut, `no app "Slac"`) {
		t.Errorf("unknown app: exit %d, %s", code, errOut)
	}
	noState(t, e)
}

func TestUninstallSameNameLeavesNameMatchesOut(t *testing.T) {
	e, app := slackHome(t)
	beta := fakeAppBundle(t, filepath.Join(e.Home, "Applications", "Beta"), "Slack.app", "com.example.otherslack", "Slack")

	_, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--yes")
	if code != 2 || !strings.Contains(errOut, "2 apps match") || !strings.Contains(errOut, beta) {
		t.Errorf("two apps called Slack: exit %d, %s", code, errOut)
	}

	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", app, "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat, `Files that only share the name "Slack" are left out`) ||
		!strings.Contains(flat, "~/Applications/Beta/Slack.app") {
		t.Errorf("output does not say why name matches are left out:\n%s", out)
	}
	for _, it := range readPlanFile(t, file).Items {
		if it.Risk == rules.RiskRisky {
			t.Errorf("name match %s is in the plan", it.Path)
		}
	}
	noState(t, e)
}

func TestUninstallSameBundleIDKeepsLeftovers(t *testing.T) {
	e, app := slackHome(t)
	fakeAppBundle(t, filepath.Join(e.Home, "Applications", "Old"), "Slack Old.app", slackID, "Slack Old")
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", app, "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := selectedPaths(readPlanFile(t, file)); !slices.Equal(got, []string{app}) {
		t.Errorf("selected %v, want only the app", got)
	}
	if !strings.Contains(strings.Join(strings.Fields(out), " "), "another copy with the same bundle id is installed") {
		t.Errorf("output does not say why:\n%s", out)
	}
}

func TestUninstallRefusesApple(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Pages.app", "com.apple.iWork.Pages", "Pages")
	_, errOut, code := sapu(t, e, "", "uninstall", "Pages", "--yes")
	if code != 1 || !strings.Contains(errOut, "comes with macOS") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	_, errOut, code = sapu(t, e, "", "uninstall", "/System/Applications/Calculator.app", "--yes")
	if code != 1 || !strings.Contains(errOut, "part of macOS") {
		t.Errorf("/System: exit %d, %s", code, errOut)
	}
	_, errOut, code = sapu(t, e, "", "uninstall", "--leftovers", "com.apple.Safari", "--yes")
	if code != 1 || !strings.Contains(errOut, "belongs to macOS") {
		t.Errorf("--leftovers com.apple: exit %d, %s", code, errOut)
	}
	noState(t, e)
}

func TestUninstallRefusesARunningApp(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)
	// Holding the executable open stands in for the running app: lsof
	// sees this test process using a file inside the bundle.
	f, err := os.Open(filepath.Join(app, "Contents", "MacOS", "Slack"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	_, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--yes")
	if code != 1 || !strings.Contains(errOut, "quit Slack first") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestUninstallAppItCannotMove(t *testing.T) {
	for _, locked := range []string{"bundle", "parent"} {
		t.Run(locked, func(t *testing.T) {
			e, app := slackHome(t)
			dir := app
			if locked == "parent" {
				dir = filepath.Dir(app)
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			before := snapshot(t, e)

			file := filepath.Join(t.TempDir(), "plan.json")
			out, _, code := sapu(t, e, "", "uninstall", "Slack", "--plan", file)
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, out)
			}
			for _, line := range []string{
				"it is owned by the system", "Move it to the Trash in Finder, or remove it with an admin account",
				"sapu uninstall --leftovers " + slackID,
			} {
				if !strings.Contains(strings.Join(strings.Fields(out), " "), line) {
					t.Errorf("output lacks %q:\n%s", line, out)
				}
			}
			p := readPlanFile(t, file)
			if got := selectedPaths(p); len(got) != 0 {
				t.Errorf("selected %v, want nothing while the app stays", got)
			}
			if !slices.ContainsFunc(p.Items, func(it plan.Item) bool { return it.Path == app }) {
				t.Error("the app is not in the plan")
			}

			out, _, code = sapu(t, e, "", "uninstall", "Slack", "--yes")
			if code != 0 || !strings.Contains(out, "Owned by the system, so Sapu cannot move it") || !strings.Contains(out, "Nothing to move.") {
				t.Errorf("exit %d\n%s", code, out)
			}
			sameSnapshot(t, before, snapshot(t, e))
		})
	}
}

func TestUninstallKeepsProtectedLeftovers(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Docker.app", "com.docker.docker", "Docker")
	containers := filepath.Join(e.Home, "Library/Containers/com.docker.docker")
	caches := filepath.Join(e.Home, "Library/Caches/com.docker.docker")
	writeFile(t, filepath.Join(containers, "Data/vms/0/data/Docker.raw"), "all my databases")
	writeFile(t, filepath.Join(caches, "x"), "cache")

	out, errOut, code := sapu(t, e, "", "uninstall", "Docker", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "Kept ~/Library/Containers/com.docker.docker: Docker Desktop disk image (all images, containers and named volumes, including databases) and Docker logins (protected).") {
		t.Errorf("output does not say the container was kept and why:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(containers, "Data/vms/0/data/Docker.raw")); err != nil || string(data) != "all my databases" {
		t.Errorf("the protected container was touched: %q, %v", data, err)
	}
	if exists(caches) {
		t.Error("the cache was not moved")
	}
}

func TestUninstallLeftovers(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	for _, rel := range append(slices.Clone(slackLeftovers), slackKeeps...) {
		writeFile(t, filepath.Join(e.Home, rel), "data of "+rel)
	}
	before := snapshot(t, e)

	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", "--leftovers", slackID, "--plan", file)
	if code != 0 || !strings.Contains(out, "Files left behind by "+slackID) {
		t.Fatalf("exit %d\n%s", code, out)
	}
	var want []string
	for _, rel := range slackLeftovers {
		want = append(want, topOf(e.Home, rel))
	}
	slices.Sort(want)
	if got := selectedPaths(readPlanFile(t, file)); !slices.Equal(got, want) {
		t.Errorf("selected %v\nwant %v", got, want)
	}

	out, _, code = sapu(t, e, "", "uninstall", "--leftovers", slackID, "--yes")
	if code != 0 || !strings.Contains(out, "Moved 13 items") {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, rel := range slackKeeps {
		if !exists(filepath.Join(e.Home, rel)) {
			t.Errorf("%s was moved", rel)
		}
	}
	if _, _, code := sapu(t, e, "", "undo", "--last"); code != 0 {
		t.Fatal("undo failed")
	}
	sameSnapshot(t, before, snapshot(t, e))

	// Refused while an app with that id is installed.
	app := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Slack.app", slackID, "Slack")
	_, errOut, code := sapu(t, e, "", "uninstall", "--leftovers", slackID, "--yes")
	if code != 1 || !strings.Contains(errOut, "still installed at "+app) {
		t.Errorf("installed: exit %d, %s", code, errOut)
	}
}

func TestUninstallUsageErrors(t *testing.T) {
	e, _ := slackHome(t)
	for _, args := range [][]string{
		{"uninstall"},
		{"uninstall", "Slack", "Other"},
		{"uninstall", "Slack", "--leftovers", slackID},
		{"uninstall", "--leftovers", "Slack"}, // a name, not a bundle id
		{"uninstall", "--leftovers", "com.example/../x"},
		{"uninstall", "Slack", "--risk", "wild"},
		{"uninstall", "Slack", "--json"}, // cannot ask
	} {
		if _, _, code := sapu(t, e, "", args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	noState(t, e)
}

func TestUninstallJSON(t *testing.T) {
	e, _ := slackHome(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", "Slack", "--plan", file, "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "uninstall", out)
	var d runData
	decodeData(t, out, &d)
	if d.Summary.SelectedItems != 14 || d.Summary.NotSelectedItems != 1 || d.Result != nil {
		t.Errorf("data %+v", d)
	}

	out, _, code = sapu(t, e, "", "uninstall", "Slack", "--yes", "--json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	validate(t, "uninstall", out)
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Moved) != 14 {
		t.Errorf("data %+v", d)
	}

	out, _, code = sapu(t, e, "", "uninstall", "Nope", "--yes", "--json")
	if code != 1 {
		t.Errorf("exit %d\n%s", code, out)
	}
	validate(t, "uninstall", out)
	if command, errCode, _ := errorOf(t, out); command != "uninstall" || errCode != "error" {
		t.Errorf("command %q, code %q", command, errCode)
	}
}

// A bundle id without a dot is a plain word; files called that are only name
// matches, so nothing but the app is selected.
func TestUninstallBundleIDWithoutADot(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	app := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Weird.app", "Notes", "Weird")
	writeFile(t, filepath.Join(e.Home, "Library/Caches/Notes/x"), "someone else's notes cache")
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", "Weird", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if got := selectedPaths(readPlanFile(t, file)); !slices.Equal(got, []string{app}) {
		t.Errorf("selected %v, want only the app", got)
	}
	if !strings.Contains(out, "Weird has no bundle id") {
		t.Errorf("output:\n%s", out)
	}
}
