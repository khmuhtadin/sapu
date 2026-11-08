package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

const testHome = "/home/test"

func item(source, path string, bytes int64, risk rules.Risk, selected bool) plan.Item {
	return plan.Item{
		Source: source, Path: testHome + "/" + path, Bytes: bytes, Risk: risk, Selected: selected, Regenerates: true,
		Explain: "Explanation of " + source + ".", Action: rules.ActionQuarantine,
	}
}

func table(p *plan.Plan, verbose bool) string {
	var buf bytes.Buffer
	writePlanTable(&buf, p, nil, verbose, false)
	return buf.String()
}

func TestPlanTableGroupsByRiskAndRule(t *testing.T) {
	p := &plan.Plan{Home: testHome, Items: []plan.Item{
		item("npm.cache", ".npm/_cacache", 2_000_000, rules.RiskModerate, true),
		item("logs.homebrew", "Library/Logs/Homebrew/a", 1_000, rules.RiskSafe, true),
		item("logs.homebrew", "Library/Logs/Homebrew/b", 3_000, rules.RiskSafe, true),
		item("danger.rule", "Library/Danger", 9_000_000, rules.RiskRisky, false),
	}}
	out := table(p, false)

	order := []string{
		"Safe — rebuilt automatically",
		"logs.homebrew · 2 items",
		"Explanation of logs.homebrew.",
		"Moderate — rebuilt, but may take time or a download",
		"npm.cache · 1 item",
		"Risky — not selected; use --risk risky",
		"danger.rule · 1 item",
		"Total selected · 3 items",
	}
	at := -1
	for _, want := range order {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("table lacks %q:\n%s", want, out)
		}
		if i < at {
			t.Errorf("%q is out of order:\n%s", want, out)
		}
		at = i
	}
	if strings.Count(out, "Explanation of logs.homebrew.") != 1 {
		t.Errorf("a group's explanation should appear once:\n%s", out)
	}
	// Home shows as ~, the biggest path first, and the risky bytes stay out of the total.
	if !strings.Contains(out, "~/Library/Logs/Homebrew/b") || strings.Contains(out, testHome) {
		t.Errorf("paths are not shown with ~:\n%s", out)
	}
	if strings.Index(out, "Homebrew/b") > strings.Index(out, "Homebrew/a") {
		t.Errorf("the biggest path should come first:\n%s", out)
	}
	if !strings.Contains(out, "Total selected · 3 items") || !strings.Contains(out, "2.0 MB") {
		t.Errorf("total:\n%s", out)
	}
	if strings.Contains(out, "11.0 MB") || strings.Contains(out, "9.0 MB\n\nNot") {
		t.Errorf("risky bytes are in the total:\n%s", out)
	}
}

func TestPlanTableSizesAreRightAligned(t *testing.T) {
	p := &plan.Plan{Home: testHome, Items: []plan.Item{
		item("a.rule", "short", 1_500, rules.RiskSafe, true),
		item("a.rule", "a/much/longer/path/than/the/other", 123_000_000, rules.RiskSafe, true),
	}}
	for _, line := range strings.Split(table(p, false), "\n") {
		if strings.HasSuffix(line, " B") || strings.HasSuffix(line, "KB") || strings.HasSuffix(line, "MB") {
			if n := len([]rune(line)); n != tableWidth {
				t.Errorf("line is %d wide, want %d: %q", n, tableWidth, line)
			}
		}
	}
}

func TestPlanTableListsFivePathsUnlessVerbose(t *testing.T) {
	p := &plan.Plan{Home: testHome}
	for i := range 8 {
		p.Items = append(p.Items, item("many.rule", fmt.Sprintf("dir/item%d", i), int64(1000*(i+1)), rules.RiskSafe, true))
	}
	out := table(p, false)
	if got := strings.Count(out, "~/dir/item"); got != 5 {
		t.Errorf("%d paths listed, want 5:\n%s", got, out)
	}
	if !strings.Contains(out, "… and 3 more") {
		t.Errorf("no '… and 3 more':\n%s", out)
	}
	out = table(p, true)
	if got := strings.Count(out, "~/dir/item"); got != 8 || strings.Contains(out, "more") {
		t.Errorf("verbose lists %d paths:\n%s", got, out)
	}
}

func TestPlanTableNamesEachProjectWhenExplanationsDiffer(t *testing.T) {
	a := item("purge:node", "code/a/node_modules", 2000, rules.RiskSafe, true)
	a.Explain = "Installed npm packages. Project: " + testHome + "/code/a"
	b := item("purge:node", "code/b/node_modules", 1000, rules.RiskSafe, true)
	b.Explain = "Installed npm packages. Project: " + testHome + "/code/b"
	out := table(&plan.Plan{Home: testHome, Items: []plan.Item{a, b}}, false)
	for _, want := range []string{"Project: ~/code/a", "Project: ~/code/b"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, testHome) {
		t.Errorf("home is not shortened:\n%s", out)
	}
}

func TestPlanTableUnselectedAndRiskySelected(t *testing.T) {
	p := &plan.Plan{Home: testHome, Items: []plan.Item{
		item("npm.cache", ".npm/_cacache", 2_000, rules.RiskModerate, false),
		item("danger.rule", "Library/Danger", 5_000, rules.RiskRisky, true),
	}}
	out := table(p, false)
	if !strings.Contains(out, "npm.cache · 1 item · not selected") {
		t.Errorf("an unselected moderate group is not marked:\n%s", out)
	}
	if !strings.Contains(out, "Risky — selected with --risk risky") || strings.Contains(out, "use --risk risky") {
		t.Errorf("heading of a selected risky group:\n%s", out)
	}
	if !strings.Contains(out, "Total selected · 1 item ") {
		t.Errorf("total:\n%s", out)
	}
}

func TestPlanTableShowsAnItemWithAnUnknownRisk(t *testing.T) {
	p := &plan.Plan{Home: testHome, Items: []plan.Item{item("odd.rule", "odd", 1000, "bogus", true)}}
	out := table(p, false)
	if !strings.Contains(out, `Unknown risk "bogus"`) || !strings.Contains(out, "~/odd") {
		t.Errorf("a hand-edited plan must still show every selected item:\n%s", out)
	}
}

func TestPlanTableNotTouchedAndCommands(t *testing.T) {
	cmd := item("purge:git-worktree", "work/tree", 7_000_000, rules.RiskModerate, true)
	cmd.Action = rules.ActionOfficialCommand
	cmd.Command = []string{"git", "-C", testHome + "/repo", "worktree", "remove", testHome + "/work/my tree"}
	p := &plan.Plan{Home: testHome,
		Items: []plan.Item{item("a.rule", "a", 1_000_000, rules.RiskSafe, true), cmd},
		Skipped: []plan.Skip{
			{Path: testHome + "/Library/Caches/JetBrains", Source: "a.rule", Reason: plan.ReasonProtected, Detail: "protected:jetbrains"},
			{Path: testHome + "/p2", Reason: plan.ReasonProtected},
			{Path: testHome + "/p3", Reason: plan.ReasonProtected},
			{Path: testHome + "/p4", Reason: plan.ReasonProtected},
			{Path: testHome + "/Documents", Reason: plan.ReasonWhitelisted, Detail: "whitelisted:~/Documents"},
			{Path: testHome + "/node", Reason: plan.ReasonInUse, Detail: "open in node (pid 42)"},
			{Path: testHome + "/dup", Reason: plan.ReasonDuplicate, Detail: "covered by item 0001 (" + testHome + "/a)"},
		}}
	out := table(p, false)

	for _, want := range []string{
		"Not touched",
		"4 protected", "~/Library/Caches/JetBrains", "(protected:jetbrains)", "… and 1 more",
		"1 whitelisted", "1 in use", "(open in node (pid 42))", "1 duplicate",
		"Commands you can run",
		"git -C /home/test/repo worktree remove '/home/test/work/my tree'", "7.0 MB",
		"Sapu does not run these",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "covered by item") {
		t.Errorf("a duplicate's detail repeats a full path:\n%s", out)
	}
	// Order: total, then not touched, then commands. The command's bytes are not in the total.
	if strings.Index(out, "Total selected") >= strings.Index(out, "Not touched") ||
		strings.Index(out, "Not touched") >= strings.Index(out, "Commands you can run") {
		t.Errorf("sections out of order:\n%s", out)
	}
	if !strings.Contains(out, "Total selected · 1 item ") || !strings.Contains(out, "1.0 MB") {
		t.Errorf("total:\n%s", out)
	}
	if strings.Contains(table(p, true), "… and") {
		t.Error("verbose still cuts the examples")
	}
}

func TestPlanTableEmpty(t *testing.T) {
	out := table(&plan.Plan{Home: testHome}, false)
	if !strings.Contains(out, "Nothing found") || strings.Contains(out, "Total") {
		t.Errorf("empty plan:\n%s", out)
	}
}

func TestNoColorOutsideATerminal(t *testing.T) {
	if strings.Contains(table(&plan.Plan{Home: testHome, Items: []plan.Item{item("a", "a", 1, rules.RiskSafe, true)}}, false), "\x1b") {
		t.Error("escape codes in output that is not a terminal")
	}
	if newStyle(&bytes.Buffer{}).color {
		t.Error("colour for a buffer")
	}
}

func TestWriteResult(t *testing.T) {
	start := time.Date(2026, 11, 8, 12, 0, 0, 0, time.Local)
	res := &engine.Result{
		RunID: "20261108-120000-abcd", Bytes: 38_000_000_000, Freed: 5_000_000,
		Moved:   []plan.Item{item("a", "a", 1, rules.RiskSafe, true), item("b", "b", 1, rules.RiskSafe, true)},
		Skipped: []plan.Skip{{Path: testHome + "/c", Reason: plan.ReasonChanged, Detail: "item changed since the plan was made"}},
		Failed:  []engine.Failure{{Item: item("d", "d", 1, rules.RiskSafe, true), Error: "permission denied"}},
	}
	var buf bytes.Buffer
	writeResult(&buf, testHome, res, config.Defaults(), start, false, false)
	out := buf.String()
	for _, want := range []string{
		"Moved 2 items (38.0 GB) to quarantine, run 20261108-120000-abcd.",
		"The space is not free yet: these items stay in quarantine until at least Sun 15 Nov.",
		"After that, a later sapu clean deletes them (sooner if the quarantine passes 20.0 GB).",
		"To free the space now: `sapu quarantine empty 20261108-120000-abcd`",
		"Undo: `sapu undo 20261108-120000-abcd`",
		"Also deleted 5.0 MB",
		"Skipped while applying", "1 changed", "~/c",
		"Failed (1)", "~/d (permission denied)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}

	buf.Reset()
	writeResult(&buf, testHome, &engine.Result{}, config.Defaults(), start, false, false)
	if out := buf.String(); !strings.Contains(out, "Nothing was moved.") || strings.Contains(out, "Undo") {
		t.Errorf("empty result:\n%s", out)
	}

	buf.Reset()
	writeResult(&buf, testHome, &engine.Result{Moved: res.Moved, Bytes: 2000}, config.Defaults(), start, true, false)
	if out := buf.String(); !strings.Contains(out, "Deleted 2 items (2.0 KB) for good") || strings.Contains(out, "quarantine") {
		t.Errorf("deleted result:\n%s", out)
	}
}

func TestShellJoin(t *testing.T) {
	got := shellJoin([]string{"git", "-C", "/a b/it's", "plain-1.2/x"})
	if want := `git -C '/a b/it'\''s' plain-1.2/x`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}

func TestAgeText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "just now",
		5 * time.Minute:  "5 min",
		3 * time.Hour:    "3 h",
		72 * time.Hour:   "3 days",
	} {
		if got := ageText(d); got != want {
			t.Errorf("ageText(%v) = %q, want %q", d, got, want)
		}
	}
}
