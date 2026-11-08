package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Every leftover and the tccutil command require the app's own item, so
// none of them goes unless the app was moved in the same run.
func TestUninstallPlanRequiresTheApp(t *testing.T) {
	e, app := slackHome(t)
	fakeTccutil(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--reset-permissions", "--risk", "risky", "--plan", file); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	p := readPlanFile(t, file)
	bundle := ""
	for _, it := range p.Items {
		if it.Path == app && it.Action == rules.ActionQuarantine {
			bundle = it.ID
		}
	}
	if bundle == "" {
		t.Fatalf("no item for the app in %+v", p.Items)
	}
	for _, it := range p.Items {
		want := bundle
		if it.ID == bundle {
			want = ""
		}
		if it.Requires != want {
			t.Errorf("%s %s: requires %q, want %q", it.ID, it.Path, it.Requires, want)
		}
	}
	if len(p.Items) != 2+len(slackLeftovers)+1 { // the app, tccutil, the leftovers, the name match
		t.Errorf("%d items", len(p.Items))
	}
}

// The app changed after the plan was made, so apply leaves it alone, and
// with it every file of the app and its permissions.
func TestUninstallAppChangedAfterPlanningKeepsEverything(t *testing.T) {
	e, app := slackHome(t)
	record := fakeTccutil(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--reset-permissions", "--plan", file); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	writeFile(t, filepath.Join(app, "Contents", "Resources", "update.bin"), "a new version")
	before := snapshot(t, e)

	out, errOut, code := sapu(t, e, "", "apply", file, "--run-commands", "--yes", "--json")

	if code != 3 {
		t.Fatalf("exit %d, want 3 (partial)\n%s\n%s", code, out, errOut)
	}
	validate(t, "apply", out)
	var d runData
	decodeData(t, out, &d)
	if d.Result == nil || len(d.Result.Moved) != 0 || len(d.Result.Commands) != 0 || len(d.Result.Failed) != 0 {
		t.Fatalf("result %+v", d.Result)
	}
	requires := 0
	for _, s := range d.Result.Skipped {
		switch {
		case s.Path == app && s.Reason == plan.ReasonChanged:
		case s.Reason == plan.ReasonRequires && strings.Contains(s.Detail, app+" was not moved"):
			requires++
		default:
			t.Errorf("skip %+v", s)
		}
	}
	if requires != len(slackLeftovers)+1 { // every selected leftover and tccutil
		t.Errorf("%d skipped as requires, want %d", requires, len(slackLeftovers)+1)
	}
	if exists(record) {
		t.Error("tccutil ran")
	}
	sameSnapshot(t, before, snapshot(t, e))
}

// The same plan applied while the app is unchanged moves the app, then its
// files, and runs tccutil.
func TestUninstallPlanAppliedMovesAppFilesAndPermissions(t *testing.T) {
	e, app := slackHome(t)
	record := fakeTccutil(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	if out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--reset-permissions", "--plan", file); code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}

	out, errOut, code := sapu(t, e, "", "apply", file, "--run-commands", "--yes")

	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if exists(app) {
		t.Error("the app was not moved")
	}
	for _, rel := range slackLeftovers {
		if exists(topOf(e.Home, rel)) {
			t.Errorf("%s was not moved", rel)
		}
	}
	if b, err := os.ReadFile(record); err != nil || strings.TrimSpace(string(b)) != "reset All "+slackID {
		t.Errorf("tccutil ran with %q, %v", b, err)
	}
}
