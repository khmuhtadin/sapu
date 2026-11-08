package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// isResetData reports whether sapu reset offers the Slack file rel. A login
// item and a script folder are named after the bundle id but are not
// settings or data, so reset leaves them.
func isResetData(rel string) bool {
	return !strings.HasPrefix(rel, "Library/LaunchAgents/") && !strings.HasPrefix(rel, "Library/Application Scripts/")
}

func TestResetPlanNeverOffersTheBundle(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "reset", "Slack", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	if !strings.Contains(out, "Slack 4.41 · "+slackID) {
		t.Errorf("output does not name the app:\n%s", out)
	}

	p := readPlanFile(t, file)
	if p.Kind != plan.KindReset {
		t.Errorf("kind %q", p.Kind)
	}
	var want []string
	for _, rel := range slackLeftovers {
		if isResetData(rel) {
			want = append(want, topOf(e.Home, rel))
		}
	}
	slices.Sort(want)
	if got := selectedPaths(p); !slices.Equal(got, want) {
		t.Errorf("selected:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	nameMatch := filepath.Join(e.Home, "Library/Application Support/Slack")
	found := false
	for _, it := range p.Items {
		if it.Path == app || strings.HasPrefix(it.Path, app+"/") {
			t.Errorf("the bundle is in the plan: %s", it.Path)
		}
		if it.Action != rules.ActionQuarantine || it.Source != "reset" || it.Bytes <= 0 {
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
			continue
		}
		if it.Risk != rules.RiskModerate || !it.Selected {
			t.Errorf("%s: risk %s, selected %v", it.Path, it.Risk, it.Selected)
		}
		wantText := "Settings, sign-ins and data of Slack as it is now; the app starts like the first time. Undo restores them."
		if it.Explain != wantText {
			t.Errorf("explain %q", it.Explain)
		}
	}
	if !found {
		t.Error("the name match is not in the plan")
	}

	// --risk risky selects the name match too.
	if _, _, code := sapu(t, e, "", "reset", "Slack", "--plan", file, "--risk", "risky"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := selectedPaths(readPlanFile(t, file)); !slices.Contains(got, nameMatch) {
		t.Errorf("--risk risky does not select the name match: %v", got)
	}
}

func TestResetMovesDataKeepsTheAppAndUndoRestores(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "reset", "Slack", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	t.Logf("sapu reset --yes\n%s", out)
	if !strings.Contains(out, "Undo: `sapu undo") {
		t.Errorf("no undo hint:\n%s", out)
	}
	if !exists(app) {
		t.Error("the app was moved")
	}
	for _, rel := range slackLeftovers {
		moved := isResetData(rel)
		if exists(topOf(e.Home, rel)) == moved {
			t.Errorf("%s: moved = %v, want %v", rel, !exists(topOf(e.Home, rel)), moved)
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

func TestResetAsksFirst(t *testing.T) {
	e, _ := slackHome(t)
	before := snapshot(t, e)
	if _, _, code := sapu(t, e, "n\n", "reset", "Slack"); code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestResetRefusesARunningApp(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The test process stands in for the running app: it has the app's name.
	name := filepath.Base(exe)
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), name+".app", "com.example.runner", name)
	writeFile(t, filepath.Join(e.Home, "Library/Preferences/com.example.runner.plist"), "prefs")
	before := snapshot(t, e)

	_, errOut, code := sapu(t, e, "", "reset", name, "--yes")
	if code != 1 || !strings.Contains(errOut, "quit "+name+" first") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestResetRefusesApple(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Pages.app", "com.apple.iWork.Pages", "Pages")
	_, errOut, code := sapu(t, e, "", "reset", "Pages", "--yes")
	if code != 1 || !strings.Contains(errOut, "comes with macOS") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	_, errOut, code = sapu(t, e, "", "reset", "/System/Applications/Calculator.app", "--yes")
	if code != 1 || !strings.Contains(errOut, "part of macOS") {
		t.Errorf("/System: exit %d, %s", code, errOut)
	}
	noState(t, e)
}

func TestResetRefusesAnAmbiguousName(t *testing.T) {
	e, _ := slackHome(t)
	beta := fakeAppBundle(t, filepath.Join(e.Home, "Applications", "Beta"), "Slack.app", "com.example.otherslack", "Slack")
	_, errOut, code := sapu(t, e, "", "reset", "Slack", "--yes")
	if code != 2 || !strings.Contains(errOut, "2 apps match") || !strings.Contains(errOut, beta) || !strings.Contains(errOut, "reset") {
		t.Errorf("exit %d, %s", code, errOut)
	}
	noState(t, e)
}

func TestResetKeepsProtectedData(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Docker.app", "com.docker.docker", "Docker")
	containers := filepath.Join(e.Home, "Library/Containers/com.docker.docker")
	caches := filepath.Join(e.Home, "Library/Caches/com.docker.docker")
	writeFile(t, filepath.Join(containers, "Data/vms/0/data/Docker.raw"), "all my databases")
	writeFile(t, filepath.Join(caches, "x"), "cache")

	out, errOut, code := sapu(t, e, "", "reset", "Docker", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "Kept ~/Library/Containers/com.docker.docker: Docker Desktop disk image") || !strings.Contains(flat, "(protected)") {
		t.Errorf("output does not say the container was kept and why:\n%s", out)
	}
	if data, err := os.ReadFile(filepath.Join(containers, "Data/vms/0/data/Docker.raw")); err != nil || string(data) != "all my databases" {
		t.Errorf("the protected container was touched: %q, %v", data, err)
	}
	if exists(caches) {
		t.Error("the cache was not moved")
	}
}

func TestResetSaysWhenThereIsNothing(t *testing.T) {
	e := env.New(t.TempDir())
	pointAppDirs(t, "~/Applications")
	app := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Slack.app", slackID, "Slack")
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "reset", "Slack", "--yes")
	if code != 0 || !strings.Contains(out, "Nothing to reset: Sapu found no settings or data of Slack") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
	if !exists(app) {
		t.Error("the app was moved")
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
}

func TestResetJSON(t *testing.T) {
	e, _ := slackHome(t)
	out, errOut, code := sapu(t, e, "", "--json", "reset", "Slack", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "reset", out)

	// Nothing left to reset: still one valid document.
	out, errOut, code = sapu(t, e, "", "--json", "reset", "Slack", "--yes")
	if code != 0 {
		t.Fatalf("second run: exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "reset", out)
}
