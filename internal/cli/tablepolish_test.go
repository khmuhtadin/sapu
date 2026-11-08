package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// otherItem is an item that does not regenerate: an app, a picked file, a Trash item.
func otherItem(source, path string, bytes int64, risk rules.Risk, selected bool) plan.Item {
	it := item(source, path, bytes, risk, selected)
	it.Regenerates = false
	return it
}

func TestPlanTableUsesNeutralHeadingsForItemsThatDoNotRegenerate(t *testing.T) {
	p := &plan.Plan{Home: testHome, Kind: plan.KindUninstall, Items: []plan.Item{
		otherItem("uninstall", "Applications/Slack.app", 9_000_000, rules.RiskModerate, true),
		otherItem("uninstall", "Library/Application Support/Slack", 1_000, rules.RiskRisky, false),
	}}
	out := table(p, false)

	for _, want := range []string{
		"To move", "uninstall · 1 item", "Explanation of uninstall.", "~/Applications/Slack.app",
		"Not selected — use --risk risky to select them", "~/Library/Application Support/Slack",
		"Total selected · 1 item ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"rebuilt", "Safe —", "Moderate —", "Risky —"} {
		if strings.Contains(out, bad) {
			t.Errorf("table says %q for items that are not rebuilt:\n%s", bad, out)
		}
	}
	if strings.Index(out, "To move") > strings.Index(out, "Not selected") {
		t.Errorf("sections out of order:\n%s", out)
	}
	if strings.Contains(out, "· not selected") {
		t.Errorf("the group repeats what its heading says:\n%s", out)
	}
}

func TestPlanTableNotSelectedHintFitsTheCommand(t *testing.T) {
	for kind, want := range map[string]string{
		plan.KindUninstall:  "use --risk risky",
		plan.KindPackages:   "use --risk risky",
		plan.KindClean:      "--only or --exclude",
		plan.KindLarge:      "leaves them where they are",
		plan.KindLoginItems: "leaves them where they are",
	} {
		p := &plan.Plan{Home: testHome, Kind: kind, Items: []plan.Item{otherItem("x", "a", 1_000, rules.RiskRisky, false)}}
		out := table(p, false)
		if !strings.Contains(out, "Not selected — ") || !strings.Contains(out, want) {
			t.Errorf("%s: heading lacks %q:\n%s", kind, want, out)
		}
		if kind != plan.KindUninstall && kind != plan.KindPackages && strings.Contains(out, "--risk risky") {
			t.Errorf("%s has no --risk flag but the table suggests it:\n%s", kind, out)
		}
	}
}

func TestPlanTableKeepsRiskHeadingsForCachesNextToOtherItems(t *testing.T) {
	p := &plan.Plan{Home: testHome, Kind: plan.KindClean, Items: []plan.Item{
		item("npm.cache", ".npm/_cacache", 2_000, rules.RiskModerate, true),
		otherItem("logs.diagnostic-reports", "Library/Logs/DiagnosticReports/a", 1_000, rules.RiskSafe, true),
	}}
	out := table(p, false)
	order := []string{"Moderate — rebuilt, but may take time or a download", "npm.cache", "To move", "logs.diagnostic-reports", "Total selected · 2 items"}
	at := -1
	for _, want := range order {
		i := strings.Index(out, want)
		if i < 0 || i < at {
			t.Fatalf("%q missing or out of order:\n%s", want, out)
		}
		at = i
	}
}

func TestUninstallAndCleanTablesEndToEnd(t *testing.T) {
	e, app := slackHome(t)
	out, _, code := sapu(t, e, "", "uninstall", app, "--yes")
	if code != 0 {
		t.Fatalf("uninstall: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "To move") || !strings.Contains(out, "Not selected — use --risk risky to select them") || strings.Contains(out, "rebuilt") {
		t.Errorf("uninstall table:\n%s", out)
	}

	out, _, code = sapu(t, newHome(t), "", "clean", "--yes")
	if code != 0 {
		t.Fatalf("clean: exit %d\n%s", code, out)
	}
	for _, want := range []string{"Safe — rebuilt automatically", "Moderate — rebuilt, but may take time or a download"} {
		if !strings.Contains(out, want) {
			t.Errorf("clean table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "To move") {
		t.Errorf("a cache table has a neutral heading:\n%s", out)
	}
}

func TestRunCommandsFlagOnlyOnCleanPurgeAndApply(t *testing.T) {
	e := env.New(t.TempDir())
	with := [][]string{{"clean"}, {"purge"}, {"apply"}}
	without := [][]string{
		{"login-items", "disable"}, {"uninstall"}, {"large"}, {"backups"}, {"trash"},
		{"installer"}, {"dupes"}, {"packages"}, {"optimize"},
	}
	for _, args := range with {
		out, _, code := sapu(t, e, "", append(args, "--help")...)
		if code != 0 || !strings.Contains(out, "--run-commands") {
			t.Errorf("sapu %s --help (exit %d) should show --run-commands:\n%s", strings.Join(args, " "), code, out)
		}
	}
	for _, args := range without {
		out, _, code := sapu(t, e, "", append(args, "--help")...)
		if code != 0 || strings.Contains(out, "run-commands") {
			t.Errorf("sapu %s --help (exit %d) must not show --run-commands:\n%s", strings.Join(args, " "), code, out)
		}
	}
	if _, _, code := sapu(t, e, "", "login-items", "disable", "--orphans", "--run-commands"); code != 2 {
		t.Errorf("login-items disable --run-commands: exit %d, want 2 (unknown flag)", code)
	}
}

func TestApplySaysUnreadableNotChanged(t *testing.T) {
	e := newHome(t)
	locked := filepath.Join(e.Home, "Library/Caches/node-gyp/sub")
	writeFile(t, filepath.Join(locked, "x"), "x")
	file := filepath.Join(t.TempDir(), "plan.json")
	if _, errOut, code := sapu(t, e, "", "clean", "--plan", file); code != 0 {
		t.Fatalf("clean --plan: exit %d\n%s", code, errOut)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	out, _, code := sapu(t, e, "", "apply", file, "--yes")
	if code != 3 {
		t.Errorf("exit %d, want 3 (some items skipped)\n%s", code, out)
	}
	if !strings.Contains(out, "1 unreadable") || !strings.Contains(out, "couldn't read it (Full Disk Access may be needed; see sapu doctor)") {
		t.Errorf("output does not say the folder could not be read:\n%s", out)
	}
	if strings.Contains(out, "changed since the plan was made") {
		t.Errorf("an unreadable folder is reported as changed:\n%s", out)
	}
	if !exists(filepath.Join(e.Home, "Library/Caches/node-gyp")) {
		t.Error("the unreadable folder was moved")
	}
}

func TestCleanLeavesOutEmptyCacheFolders(t *testing.T) {
	e := newHome(t)
	empty := filepath.Join(e.Home, "Library/Application Support/Slack/GPUCache")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	out, _, code := sapu(t, e, "", "clean", "--plan", "-")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	p, err := plan.Read(strings.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Items) == 0 {
		t.Fatal("no items at all: the junk of the fake home is missing")
	}
	for _, it := range p.Items {
		if it.Bytes == 0 || it.Path == empty {
			t.Errorf("the plan holds an empty item: %+v", it)
		}
	}
}

func TestPlanTableSaysWhyAPathIsProtected(t *testing.T) {
	why, err := protectWhy(env.New(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	p := &plan.Plan{Home: testHome, Skipped: []plan.Skip{
		{Path: testHome + "/Library/Containers/com.docker.docker", Reason: plan.ReasonProtected, Detail: "protected:docker"},
		{Path: testHome + "/Documents", Reason: plan.ReasonWhitelisted, Detail: "whitelisted:~/Documents"},
	}}
	var buf bytes.Buffer
	writePlanTable(&buf, p, why, false, false)
	out := buf.String()
	for _, want := range []string{"~/Library/Containers/com.docker.docker", "(Docker Desktop disk image", "(whitelisted:~/Documents)"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "protected:docker") {
		t.Errorf("the table shows the entry id instead of the reason:\n%s", out)
	}
	if why("whitelisted:~/x") != "" || why("protected:no-such-entry") != "" {
		t.Error("protectWhy answers for something that is not a protection entry")
	}
}

// A result with a kept copy and the unreadable skip reason validates against
// the schemas of the commands that apply plans.
func TestSchemasAllowKeepCopyAndUnreadable(t *testing.T) {
	it := item("dupes", "a/copy", 1_000, rules.RiskSafe, true)
	it.KeepCopy = &plan.KeepCopy{Path: testHome + "/a/orig", Identity: plan.Identity{Dev: 1, Ino: 2, Mode: 0o100644, Size: 3, MtimeNs: 4}}
	res := &engine.Result{
		RunID: "20261108-120000-abcd", Moved: []plan.Item{it}, Failed: []engine.Failure{}, Suggested: []plan.Item{},
		Skipped: []plan.Skip{{Path: testHome + "/b", Source: "x", Reason: plan.ReasonUnreadable, Detail: "permission denied"}},
	}
	data := runData{Summary: summarize(&plan.Plan{Items: []plan.Item{it}}), Result: res}
	for _, name := range []string{"clean", "apply", "purge", "uninstall", "dupes"} {
		if name == "dupes" {
			check(t, name, dupesData{data, []dupeGroup{}})
			continue
		}
		check(t, name, data)
	}
}
