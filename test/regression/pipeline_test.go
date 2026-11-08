// This file holds the rule sets, the pipeline steps and the assertions on
// plans and results. How the suite reads the contracts is written next to
// each assertion and summed up in README.md.
package regression

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/scan"
	ruledata "github.com/khmuhtadin/sapu/rules"
)

// Every fake home that runs the aggressive rules also holds this ordinary
// cache. It must be moved, which proves the run really changed something and
// that a test did not pass only because nothing ran.
const (
	junkItem = "Library/Caches/com.example.junk"
	junkFile = junkItem + "/cache.db"
)

// synthRule builds a quarantine rule in Go. Scan does not validate rules, so
// these can be as broad as a buggy rule would be.
func synthRule(id string, risk rules.Risk, paths ...string) rules.Rule {
	regenerates := true
	return rules.Rule{
		ID:          id,
		Category:    rules.CategorySystem,
		Paths:       paths,
		Risk:        risk,
		Regenerates: &regenerates,
		Explain:     map[string]string{"en": "Synthetic rule from the data-loss regression suite."},
		Action:      rules.ActionQuarantine,
		Fixture:     "regression",
	}
}

// aggressiveRules simulate a buggy rule set that aims straight at the places
// where user data lives. Protection must hold even then.
func aggressiveRules() []rules.Rule {
	return []rules.Rule{
		synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
		synthRule("synthetic.dot-cache", rules.RiskSafe, "~/.cache/**"),
		synthRule("synthetic.claude", rules.RiskSafe, "~/.claude/**"),
		synthRule("synthetic.claude-memory", rules.RiskSafe, "~/.claude/projects/*/memory"),
		synthRule("synthetic.app-support", rules.RiskSafe, "~/Library/Application Support/**"),
		synthRule("synthetic.group-containers", rules.RiskSafe, "~/Library/Group Containers/**"),
		synthRule("synthetic.containers", rules.RiskSafe, "~/Library/Containers/**"),
		synthRule("synthetic.mail", rules.RiskSafe, "~/Library/Mail/**"),
		synthRule("synthetic.npx", rules.RiskSafe, "~/.npm/_npx/**"),
		synthRule("synthetic.trash", rules.RiskSafe, "~/.Trash/**"),
	}
}

// embeddedRules loads the real cleanup rules from the binary. It keeps only
// quarantine rules and only their paths inside the home folder: even with a
// buggy engine, a test must never touch system folders or other volumes
// (AGENTS.md rule 2) or run another tool's cleanup command on this Mac
// (rule 3). Official commands are covered by TestOfficialCommandsAreNeverRun
// with a harmless command.
func embeddedRules(t *testing.T) []rules.Rule {
	t.Helper()
	fsys, err := fs.Sub(ruledata.FS, "cleaners")
	if err != nil {
		t.Fatal(err)
	}
	all, err := rules.Load(fsys)
	if err != nil {
		t.Fatalf("rules.Load: %v", err)
	}
	var kept []rules.Rule
	for _, r := range all {
		if r.Action != rules.ActionQuarantine {
			continue
		}
		var paths []string
		for _, p := range r.Paths {
			if strings.HasPrefix(p, "~/") {
				paths = append(paths, p)
			}
		}
		if len(paths) == 0 {
			continue
		}
		r.Paths = paths
		kept = append(kept, r)
	}
	return kept
}

func scanItems(t *testing.T, e env.Env, rs []rules.Rule) []plan.Item {
	t.Helper()
	items, err := scan.Scan(t.Context(), e, rs, nil)
	if err != nil {
		t.Fatalf("scan.Scan: %v", err)
	}
	return items
}

func makePlan(t *testing.T, e env.Env, items []plan.Item) *plan.Plan {
	t.Helper()
	p, err := engine.Plan(t.Context(), e, plan.KindClean, items)
	if err != nil {
		t.Fatalf("engine.Plan: %v", err)
	}
	return p
}

func applyPlan(t *testing.T, e env.Env, p *plan.Plan, opts engine.Options) *engine.Result {
	t.Helper()
	res, err := engine.Apply(t.Context(), e, p, opts)
	if err != nil {
		t.Fatalf("engine.Apply: %v", err)
	}
	for _, f := range res.Failed {
		t.Logf("apply failed on %s: %s", display(e, f.Item.Path), f.Error)
	}
	return res
}

// clean runs the whole pipeline with default options, like `sapu clean --yes`.
func clean(t *testing.T, e env.Env, rs []rules.Rule) (*plan.Plan, *engine.Result) {
	t.Helper()
	p := makePlan(t, e, scanItems(t, e, rs))
	return p, applyPlan(t, e, p, engine.DefaultOptions())
}

func whitelist(t *testing.T, e env.Env, line string) {
	t.Helper()
	if err := protect.AddWhitelist(e, line); err != nil {
		t.Fatalf("protect.AddWhitelist: %v", err)
	}
}

func identityOf(t *testing.T, p string) plan.Identity {
	t.Helper()
	id, err := plan.IdentityOf(p)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// forgedItem is a plan item written by hand, as someone editing a saved plan
// file would. Its identity is correct, so only Sapu's own checks can stop it.
func forgedItem(t *testing.T, id, p string) plan.Item {
	t.Helper()
	return plan.Item{
		ID:          id,
		Source:      "synthetic.caches",
		Path:        p,
		Bytes:       4096,
		Risk:        rules.RiskSafe,
		Regenerates: true,
		Explain:     "Forged item.",
		Action:      rules.ActionQuarantine,
		Identity:    identityOf(t, p),
		Selected:    true,
	}
}

func writePlanFile(t *testing.T, file string, p *plan.Plan) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Write(f, p); err != nil {
		_ = f.Close() // the write error is the one to report
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func readPlanFile(t *testing.T, file string) *plan.Plan {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	p, err := plan.Read(f)
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// startSleepIn starts `sleep` with its working directory in dir, the way a
// running `npx` sits in its own _npx folder. The child is killed when the
// test ends. It sleeps far longer than the test runs, so it is still alive
// when Apply re-checks open files even on a slow CI machine.
func startSleepIn(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("/bin/sleep", "600")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep in %s: %v", dir, err)
	}
	t.Cleanup(func() {
		if err := cmd.Process.Kill(); err != nil {
			t.Logf("kill sleep: %v", err)
		}
		_ = cmd.Wait() // reports "signal: killed", which is expected
	})
}

// requireSkip checks that skips holds the item at ~/rel with this reason.
// Skip.Path is the path of the item that was skipped (what scan produced),
// not the protected entry that blocked it.
func requireSkip(t *testing.T, e env.Env, skips []plan.Skip, rel, reason string) {
	t.Helper()
	p := homePath(e, rel)
	for _, s := range skips {
		if s.Path == p && s.Reason == reason {
			return
		}
	}
	var got []string
	for _, s := range skips {
		got = append(got, fmt.Sprintf("%s (%s)", display(e, s.Path), s.Reason))
	}
	t.Errorf("want ~/%s skipped as %q; skipped: %v", rel, reason, got)
}

// requireNotPlanned checks that a skipped item is not also left in p.Items:
// Plan moves a blocked item from Items to Skipped.
func requireNotPlanned(t *testing.T, e env.Env, p *plan.Plan, rel string) {
	t.Helper()
	for _, it := range p.Items {
		if it.Path == homePath(e, rel) {
			t.Errorf("~/%s is skipped but still in plan.Items (selected=%v)", rel, it.Selected)
		}
	}
}

// requirePlanned returns the plan item at ~/rel.
func requirePlanned(t *testing.T, e env.Env, p *plan.Plan, rel string) plan.Item {
	t.Helper()
	for _, it := range p.Items {
		if it.Path == homePath(e, rel) {
			return it
		}
	}
	var got []string
	for _, it := range p.Items {
		got = append(got, display(e, it.Path))
	}
	t.Fatalf("want ~/%s in the plan; plan items: %v", rel, got)
	return plan.Item{}
}

// requireMoved checks that the item at ~/rel is reported as moved and is no
// longer in its place.
func requireMoved(t *testing.T, e env.Env, res *engine.Result, rel string) {
	t.Helper()
	p := homePath(e, rel)
	found := false
	for _, it := range res.Moved {
		found = found || it.Path == p
	}
	if !found {
		// Say why, so a rare failure explains itself (e.g. a background
		// process holding a file, which makes the item in_use).
		t.Errorf("want ~/%s in Result.Moved; skipped %+v; failed %+v", rel, res.Skipped, res.Failed)
	}
	if _, err := os.Lstat(p); !os.IsNotExist(err) {
		t.Errorf("want ~/%s gone from its place after the move (lstat err: %v)", rel, err)
	}
}

func requireNotMoved(t *testing.T, e env.Env, res *engine.Result, rel string) {
	t.Helper()
	for _, it := range res.Moved {
		if it.Path == homePath(e, rel) {
			t.Errorf("~/%s must not be moved, but it is in Result.Moved", rel)
		}
	}
}

func requireMissing(t *testing.T, e env.Env, rel string) {
	t.Helper()
	if _, err := os.Lstat(homePath(e, rel)); !os.IsNotExist(err) {
		t.Errorf("want ~/%s gone (lstat err: %v)", rel, err)
	}
}

func requireExists(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); err != nil {
		t.Errorf("want %s to exist: %v", p, err)
	}
}

// quarantinePath is where fsop puts an item moved in this run (fsop
// invariant 1): <StateDir>/quarantine/<run_id>/<item_id>-<name>.
func quarantinePath(e env.Env, runID string, it plan.Item) string {
	return filepath.Join(e.QuarantineDir(), runID, it.ID+"-"+filepath.Base(it.Path))
}

// requireRunQuarantineEmpty checks that nothing of the run is left in
// quarantine: its folder is gone or empty.
func requireRunQuarantineEmpty(t *testing.T, e env.Env, runID string) {
	t.Helper()
	dir := filepath.Join(e.QuarantineDir(), runID)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	if len(names) > 0 {
		t.Errorf("quarantine of run %s still holds %v", runID, names)
	}
}

// parentDirs lists the folders (relative to the home) that items were moved
// out of. Their mtimes change with every move.
func parentDirs(t *testing.T, e env.Env, items []plan.Item) []string {
	t.Helper()
	var dirs []string
	for _, it := range items {
		dirs = append(dirs, path.Dir(relPath(t, e, it.Path)))
	}
	return dirs
}

func inItems(items []plan.Item, p string) bool {
	for _, it := range items {
		if it.Path == p {
			return true
		}
	}
	return false
}
