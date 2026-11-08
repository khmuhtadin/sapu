package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// fakeTccutil puts a tccutil on PATH that writes its arguments to the file it
// returns. The real tccutil is never run.
func fakeTccutil(t *testing.T) (record string) {
	t.Helper()
	record = filepath.Join(t.TempDir(), "tccutil.log")
	tool := fakeTool(t, "tccutil", `echo "$@" >> "`+record+`"`)
	t.Setenv("PATH", filepath.Dir(tool)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func commandItems(p *plan.Plan) []plan.Item {
	var out []plan.Item
	for _, it := range p.Items {
		if it.Action == rules.ActionOfficialCommand {
			out = append(out, it)
		}
	}
	return out
}

func TestUninstallResetPermissionsPlansOneCommand(t *testing.T) {
	e, app := slackHome(t)
	before := snapshot(t, e)
	record := fakeTccutil(t)
	file := filepath.Join(t.TempDir(), "plan.json")

	out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--reset-permissions", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)
	if exists(record) {
		t.Error("--plan ran tccutil")
	}

	p := readPlanFile(t, file)
	cmds := commandItems(p)
	if len(cmds) != 1 {
		t.Fatalf("%d command items, want 1", len(cmds))
	}
	it := cmds[0]
	if strings.Join(it.Command, " ") != "tccutil reset All "+slackID {
		t.Errorf("command %v", it.Command)
	}
	if it.Source != "uninstall" || it.Risk != rules.RiskModerate || !it.Selected || it.Path != app {
		t.Errorf("item %+v", it)
	}
	if len(it.ProtectIf) != 1 || it.ProtectIf[0].ProcessRunning != "Slack" {
		t.Errorf("protect_if %+v", it.ProtectIf)
	}
	if !strings.Contains(it.Explain, "Removes the privacy permissions you gave the app") || !strings.Contains(it.Explain, "Not undoable") {
		t.Errorf("explain %q", it.Explain)
	}
	// The app and the files are still planned as before.
	if got := selectedPaths(p); len(got) != 1+len(slackLeftovers) {
		t.Errorf("%d items selected, want %d", len(got), 1+len(slackLeftovers))
	}
}

func TestUninstallWithoutTheFlagPlansNoCommand(t *testing.T) {
	e, _ := slackHome(t)
	fakeTccutil(t)
	file := filepath.Join(t.TempDir(), "plan.json")
	out, _, code := sapu(t, e, "", "uninstall", "Slack", "--plan", file)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if cmds := commandItems(readPlanFile(t, file)); len(cmds) != 0 {
		t.Errorf("command items without the flag: %+v", cmds)
	}
	if strings.Contains(out, "tccutil") {
		t.Errorf("output mentions tccutil:\n%s", out)
	}

	// Applying without the flag moves files and never runs tccutil.
	record := fakeTccutil(t)
	if out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--yes"); code != 0 || exists(record) || strings.Contains(out, "tccutil") {
		t.Errorf("exit %d, ran %v\n%s\n%s", code, exists(record), out, errOut)
	}
}

func TestUninstallResetPermissionsRunsAfterConfirmation(t *testing.T) {
	e, app := slackHome(t)
	record := fakeTccutil(t)

	// Answering no: nothing moves, nothing runs.
	before := snapshot(t, e)
	_, errOut, code := sapu(t, e, "n\n", "uninstall", "Slack", "--reset-permissions")
	if code != 2 {
		t.Errorf("answer n: exit %d, want 2", code)
	}
	for _, want := range []string{"tccutil reset All " + slackID, "not undoable"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("confirmation lacks %q:\n%s", want, errOut)
		}
	}
	if exists(record) {
		t.Error("tccutil ran without a yes")
	}
	sameSnapshot(t, before, snapshot(t, e))
	noState(t, e)

	out, errOut, code := sapu(t, e, "", "uninstall", "Slack", "--reset-permissions", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	if exists(app) {
		t.Error("the app was not moved")
	}
	got, err := os.ReadFile(record)
	if err != nil || strings.TrimSpace(string(got)) != "reset All "+slackID {
		t.Errorf("tccutil was called with %q, %v", got, err)
	}
	if !strings.Contains(out, "Ran 1 command (not undoable)") {
		t.Errorf("result does not say the command ran:\n%s", out)
	}
}

func TestUninstallResetPermissionsJSON(t *testing.T) {
	e, _ := slackHome(t)
	fakeTccutil(t)
	out, errOut, code := sapu(t, e, "", "--json", "uninstall", "Slack", "--reset-permissions", "--yes")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errOut)
	}
	validate(t, "uninstall", out)
	if !strings.Contains(out, `"commands"`) {
		t.Errorf("no commands in the result:\n%s", out)
	}
}

func TestUninstallResetPermissionsRefusals(t *testing.T) {
	e, _ := slackHome(t)
	_, errOut, code := sapu(t, e, "", "uninstall", "--leftovers", slackID, "--reset-permissions", "--yes")
	if code != 2 || !strings.Contains(errOut, "--reset-permissions needs an installed APP") {
		t.Errorf("with --leftovers: exit %d, %s", code, errOut)
	}

	// An app without a bundle id has nothing tccutil could reset.
	noID := fakeAppBundle(t, filepath.Join(e.Home, "Applications"), "Plain.app", "Plain", "Plain")
	_, errOut, code = sapu(t, e, "", "uninstall", noID, "--reset-permissions", "--yes")
	if code != 1 || !strings.Contains(errOut, "has no bundle id") {
		t.Errorf("no bundle id: exit %d, %s", code, errOut)
	}
	if !exists(noID) {
		t.Error("the app was moved")
	}
	noState(t, e)
}
