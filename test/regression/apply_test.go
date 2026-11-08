package regression

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// kitchenSinkHome holds every protected case, an npx folder, a Trash, the
// junk cache and a symlink from the caches into ~/.claude.
func kitchenSinkHome(t *testing.T) env.Env {
	t.Helper()
	cases := []protectedCase{dataGripCase, huggingFaceCase, gitHubDesktopCase, claudeMemoryCase, appleNotesCase, appleMailCase, browsersCase}
	files := map[string]string{
		junkFile:                     "ordinary cache",
		".npm/_npx/abc/package.json": "{}",
		".Trash/old-installer.dmg":   "dmg",
		"Documents/thesis.docx":      "chapter 1",
	}
	for _, c := range cases {
		files = merge(files, c.files)
	}
	e := newHome(t, files)
	for _, c := range cases {
		for rel, target := range c.links {
			symlink(t, homePath(e, rel), target)
		}
	}
	symlink(t, homePath(e, "Library/Caches/link"), homePath(e, ".claude"))
	fixTimes(t, e.Home)
	return e
}

// Case 11, Mole #1865: a "dry run" deleted files. Scanning and planning never
// write anything: the home is identical afterwards (the home folder's own
// mtime included, which catches a file created and removed again), and Sapu's
// state folder is not created.
func TestDryRunNeverWrites(t *testing.T) {
	t.Parallel()
	ruleSets := map[string]func(*testing.T) []rules.Rule{
		"aggressive rules": func(*testing.T) []rules.Rule { return aggressiveRules() },
		"embedded rules":   embeddedRules,
	}
	for name, ruleSet := range ruleSets {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e := kitchenSinkHome(t)
			rs := ruleSet(t)
			before := takeSnapshot(t, e.Home)

			makePlan(t, e, scanItems(t, e, rs))

			requireSameTree(t, "home after a dry run", before, takeSnapshot(t, e.Home))
			if _, err := os.Lstat(e.StateDir); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a dry run created Sapu's state folder %s (lstat err: %v)", e.StateDir, err)
			}
		})
	}

	t.Run("with a whitelist", func(t *testing.T) {
		t.Parallel()
		e := kitchenSinkHome(t)
		whitelist(t, e, "~/Documents")
		before := takeSnapshot(t, e.Home) // Sapu's state folder included

		p := makePlan(t, e, scanItems(t, e, aggressiveRules()))

		requireSameTree(t, "home after a dry run", before, takeSnapshot(t, e.Home))
		if len(p.Items) == 0 {
			t.Error("the plan is empty; the dry run must still find the junk cache")
		}
	})
}

// Case 13: an item that changed between planning and applying (written to,
// grown, or swapped for another file with the same bytes and mtime) is
// skipped as "changed" and left exactly as it is now.
func TestChangedAfterPlanningIsSkipped(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/edited.db":    "original",
		"Library/Caches/grown/a.bin":  "a",
		"Library/Caches/swapped.db":   "same bytes",
		"Library/Caches/steady/a.bin": "a",
	})
	p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
	changed := []string{"Library/Caches/edited.db", "Library/Caches/grown", "Library/Caches/swapped.db"}
	for _, rel := range changed {
		requirePlanned(t, e, p, rel)
	}

	// The owning app keeps working after the plan was made.
	appendFile(t, homePath(e, "Library/Caches/edited.db"), " and new data")
	writeFile(t, homePath(e, "Library/Caches/grown/b.bin"), "b")
	swapInode(t, homePath(e, "Library/Caches/swapped.db"))
	before := snapshotEach(t, e, changed)

	res := applyPlan(t, e, p, engine.DefaultOptions())

	for _, rel := range changed {
		requireSkip(t, e, res.Skipped, rel, plan.ReasonChanged)
		requireNotMoved(t, e, res, rel)
	}
	requireSurvived(t, e, before)
	requireMoved(t, e, res, "Library/Caches/steady")
}

// swapInode replaces a file with a new file that has the same contents, mode
// and mtime. Only the inode differs.
func swapInode(t *testing.T, p string) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	tmp := p + ".swap"
	writeFile(t, tmp, string(data)) // created while p exists, so it gets another inode
	removeAll(t, p)
	hardlink(t, p, tmp)
	removeAll(t, tmp)
	fixTimes(t, p)
}

// Case 14: an item deleted between planning and applying is skipped as
// "gone". That is not an error.
func TestRemovedAfterPlanningIsSkipped(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Caches/gone.db":        "x",
		"Library/Caches/gone-dir/a.bin": "a",
		"Library/Caches/steady/a.bin":   "a",
	})
	p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
	gone := []string{"Library/Caches/gone.db", "Library/Caches/gone-dir"}
	for _, rel := range gone {
		requirePlanned(t, e, p, rel)
		removeAll(t, homePath(e, rel)) // the user or the app deletes it
	}

	res, err := engine.Apply(t.Context(), e, p, engine.DefaultOptions())

	if err != nil {
		t.Fatalf("Apply must not fail because an item is gone: %v", err)
	}
	for _, rel := range gone {
		requireSkip(t, e, res.Skipped, rel, plan.ReasonGone)
		requireMissing(t, e, rel)
	}
	for _, f := range res.Failed {
		t.Errorf("unexpected failure on %s: %s", display(e, f.Item.Path), f.Error)
	}
	requireMoved(t, e, res, "Library/Caches/steady")
}

// Case 16: someone edits a saved plan file so that a selected item points at
// a protected file, with that file's correct identity, and adds an item for
// a file the user whitelisted after planning. Apply checks protection and the
// whitelist again before every move, so both are refused and survive.
func TestTamperedPlanIsRecheckedAtApply(t *testing.T) {
	t.Parallel()
	e := newHome(t, merge(claudeMemoryCase.files, map[string]string{
		"Library/Caches/junk-a/cache.db": "a",
		"Library/Caches/junk-b/cache.db": "b",
		"Documents/keep.txt":             "the user's file",
	}))
	planFile := filepath.Join(t.TempDir(), "plan.json")
	writePlanFile(t, planFile, makePlan(t, e, scanItems(t, e, []rules.Rule{
		synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
	})))

	// The tamper: point junk-a's item at Claude's memory, add a forged item.
	tampered := readPlanFile(t, planFile)
	memory := homePath(e, ".claude/projects/-proj-x/memory/MEMORY.md")
	edited := false
	for i, it := range tampered.Items {
		if it.Path == homePath(e, "Library/Caches/junk-a") {
			tampered.Items[i].Path = memory
			tampered.Items[i].Identity = identityOf(t, memory)
			tampered.Items[i].Selected = true
			edited = true
		}
	}
	if !edited {
		t.Fatal("test setup: junk-a is not in the plan")
	}
	tampered.Items = append(tampered.Items, forgedItem(t, "9001", homePath(e, "Documents/keep.txt")))
	writePlanFile(t, planFile, tampered)
	whitelist(t, e, "~/Documents/keep.txt")
	p := readPlanFile(t, planFile)
	before := snapshotEach(t, e, []string{".claude", "Documents", "Library/Caches/junk-a"})

	res := applyPlan(t, e, p, engine.DefaultOptions())

	requireSkip(t, e, res.Skipped, ".claude/projects/-proj-x/memory/MEMORY.md", plan.ReasonProtected)
	requireSkip(t, e, res.Skipped, "Documents/keep.txt", plan.ReasonWhitelisted)
	requireNotMoved(t, e, res, ".claude/projects/-proj-x/memory/MEMORY.md")
	requireNotMoved(t, e, res, "Documents/keep.txt")
	requireSurvived(t, e, before) // junk-a too: it is no longer in the plan
	requireMoved(t, e, res, "Library/Caches/junk-b")
}

// Case 17: a plan made for another home folder is refused as a whole, before
// anything is written (no quarantine expiry, no run folder).
func TestPlanForAnotherHomeIsRefused(t *testing.T) {
	t.Parallel()
	files := map[string]string{junkFile: "ordinary cache"}

	t.Run("applied in another home", func(t *testing.T) {
		t.Parallel()
		mine := newHome(t, files)
		p := makePlan(t, mine, scanItems(t, mine, aggressiveRules()))
		requirePlanned(t, mine, p, junkItem)
		other := newHome(t, files)
		beforeMine, beforeOther := takeSnapshot(t, mine.Home), takeSnapshot(t, other.Home)

		_, err := engine.Apply(t.Context(), other, p, engine.DefaultOptions())

		if err == nil {
			t.Errorf("Apply accepted a plan made for %s while running in %s", p.Home, other.Home)
		}
		requireSameTree(t, "home the plan was made for", beforeMine, takeSnapshot(t, mine.Home))
		requireSameTree(t, "home Apply ran in", beforeOther, takeSnapshot(t, other.Home))
	})

	t.Run("plan home edited", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
		requirePlanned(t, e, p, junkItem)
		p.Home = t.TempDir()
		before := takeSnapshot(t, e.Home)

		_, err := engine.Apply(t.Context(), e, p, engine.DefaultOptions())

		if err == nil {
			t.Errorf("Apply accepted a plan for home %s while running in %s", p.Home, e.Home)
		}
		requireSameTree(t, "home", before, takeSnapshot(t, e.Home))
	})
}

// Case 18: plan items outside the home folder (a plain path in another
// folder, and an unclean path that climbs out with "..") are refused, never
// moved. Apply may refuse the whole plan with an error, or report each such
// item in Result.Failed or Result.Skipped.
func TestItemOutsideHomeIsRefused(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{junkFile: "ordinary cache"})
	outside := t.TempDir() // a sibling of the home folder
	build(t, outside, map[string]string{
		"victim.txt":          "not Sapu's to touch",
		"victim-dir/data.bin": "not Sapu's to touch either",
	})
	fixTimes(t, outside)
	p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
	targets := []string{
		filepath.Join(outside, "victim.txt"),
		filepath.Join(outside, "victim-dir"),
		e.Home + "/../" + filepath.Base(outside) + "/victim.txt",
	}
	for i, target := range targets {
		p.Items = append(p.Items, forgedItem(t, fmt.Sprintf("90%02d", i+1), target))
	}
	before := takeSnapshot(t, outside)

	res, err := engine.Apply(t.Context(), e, p, engine.DefaultOptions())

	requireSameTree(t, "folder outside home", before, takeSnapshot(t, outside))
	if err != nil {
		return // refusing the whole plan is fine
	}
	for _, target := range targets {
		if inItems(res.Moved, target) {
			t.Errorf("Apply reports %s as moved; it is outside home", target)
		}
		refused := false
		for _, f := range res.Failed {
			refused = refused || f.Item.Path == target
		}
		for _, s := range res.Skipped {
			refused = refused || s.Path == target
		}
		if !refused {
			t.Errorf("%s is outside home but is in neither Result.Failed nor Result.Skipped", target)
		}
	}
}

// Case 19: a rule whose path runs through a symlink must not reach what the
// link points to. ~/Library/Caches/link points at ~/.claude (and at a folder
// outside home); a rule targets ~/Library/Caches/link/**. Nothing behind the
// link moves.
func TestSymlinkEscapeMovesNothing(t *testing.T) {
	t.Parallel()

	t.Run("link into ~/.claude", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, merge(claudeMemoryCase.files, map[string]string{".cache/junk/blob": "junk"}))
		symlink(t, homePath(e, "Library/Caches/link"), homePath(e, ".claude"))
		symlink(t, homePath(e, "Library/Caches/rel-link"), "../../.claude")
		fixTimes(t, e.Home)
		before := snapHome(t, e)

		_, res := clean(t, e, []rules.Rule{
			synthRule("synthetic.link", rules.RiskSafe, "~/Library/Caches/link/**"),
			synthRule("synthetic.rel-link", rules.RiskSafe, "~/Library/Caches/rel-link/**"),
			synthRule("synthetic.dot-cache", rules.RiskSafe, "~/.cache/**"),
		})

		requireMoved(t, e, res, ".cache/junk")
		after := snapHome(t, e)
		before.removeTree(".cache/junk")
		before.clearMtime(".cache")
		after.clearMtime(".cache")
		requireSameTree(t, "home apart from .cache/junk", before, after)
	})

	t.Run("link outside home", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, map[string]string{".cache/junk/blob": "junk"})
		outside := t.TempDir()
		build(t, outside, map[string]string{
			"important/data.txt": "outside home",
			"photos/1.jpg":       "jpeg",
		})
		fixTimes(t, outside)
		symlink(t, homePath(e, "Library/Caches/ext"), outside)
		fixTimes(t, e.Home)
		beforeOutside := takeSnapshot(t, outside)
		beforeHome := snapHome(t, e)

		_, res := clean(t, e, []rules.Rule{
			synthRule("synthetic.ext", rules.RiskSafe, "~/Library/Caches/ext/**"),
			synthRule("synthetic.dot-cache", rules.RiskSafe, "~/.cache/**"),
		})

		requireMoved(t, e, res, ".cache/junk")
		requireSameTree(t, "folder behind the link", beforeOutside, takeSnapshot(t, outside))
		afterHome := snapHome(t, e)
		beforeHome.removeTree(".cache/junk")
		beforeHome.clearMtime(".cache")
		afterHome.clearMtime(".cache")
		requireSameTree(t, "home apart from .cache/junk", beforeHome, afterHome)
	})
}

// Case 20: risky items are in the plan but not selected, and Apply leaves
// them alone. Safe and moderate items are selected.
func TestRiskyItemsAreNotSelected(t *testing.T) {
	t.Parallel()
	e := newHome(t, map[string]string{
		"Library/Logs/com.example.app/app.log": "log",
		"Library/Logs/old.log":                 "old",
		junkFile:                               "ordinary cache",
		".cache/tool/blob":                     "blob",
	})
	p := makePlan(t, e, scanItems(t, e, []rules.Rule{
		synthRule("synthetic.logs", rules.RiskRisky, "~/Library/Logs/**"),
		synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
		synthRule("synthetic.dot-cache", rules.RiskModerate, "~/.cache/**"),
	}))
	wantSelected := map[string]bool{
		"Library/Logs/com.example.app": false,
		"Library/Logs/old.log":         false,
		junkItem:                       true,
		".cache/tool":                  true,
	}
	for _, rel := range sortedKeys(wantSelected) {
		if it := requirePlanned(t, e, p, rel); it.Selected != wantSelected[rel] {
			t.Errorf("~/%s (%s): selected=%v, want %v", rel, it.Risk, it.Selected, wantSelected[rel])
		}
	}
	before := snapshotEach(t, e, []string{"Library/Logs"})

	res := applyPlan(t, e, p, engine.DefaultOptions())

	requireSurvived(t, e, before)
	requireNotMoved(t, e, res, "Library/Logs/com.example.app")
	requireNotMoved(t, e, res, "Library/Logs/old.log")
	requireMoved(t, e, res, junkItem)
	requireMoved(t, e, res, ".cache/tool")
}

// Case 23: in M1, official commands are shown, never run (a plan decision). The
// command here would leave a marker file if it ran.
func TestOfficialCommandsAreNeverRun(t *testing.T) {
	t.Parallel()
	const vms = "Library/Containers/com.example.docker/Data/vms"
	e := newHome(t, map[string]string{
		vms + "/0/data/Docker.raw": "disk image",
		junkFile:                   "ordinary cache",
	})
	marker := homePath(e, "official-command-ran")
	official := synthRule("synthetic.official", rules.RiskSafe, "~/"+vms)
	official.Action = rules.ActionOfficialCommand
	official.Command = []string{"/usr/bin/touch", marker}
	p := makePlan(t, e, scanItems(t, e, []rules.Rule{
		official,
		synthRule("synthetic.caches", rules.RiskSafe, "~/Library/Caches/**"),
	}))
	if it := requirePlanned(t, e, p, vms); it.Action != rules.ActionOfficialCommand {
		t.Fatalf("~/%s has action %q, want %q", vms, it.Action, rules.ActionOfficialCommand)
	}
	before := snapshotEach(t, e, []string{vms})

	res := applyPlan(t, e, p, engine.DefaultOptions())

	if !inItems(res.Suggested, homePath(e, vms)) {
		t.Errorf("want ~/%s in Result.Suggested", vms)
	}
	requireNotMoved(t, e, res, vms)
	requireSurvived(t, e, before)
	requireMissing(t, e, "official-command-ran")
	requireMoved(t, e, res, junkItem)
}
