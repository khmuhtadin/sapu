package regression

import (
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// Case 9, Mole #1870: a cleaner deleted ~/.npm/_npx/<hash> while npx was
// running from it. A folder that a process has as its working directory is
// in use: Plan skips it, and Apply checks again right before moving.
func TestNpxFolderInUseIsSkipped(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		".npm/_npx/abc/package.json":                       `{"dependencies":{"create-thing":"1.0.0"}}`,
		".npm/_npx/abc/node_modules/create-thing/index.js": "console.log('scaffolding')",
		".npm/_npx/def/package.json":                       `{"dependencies":{"old-tool":"2.0.0"}}`,
		".npm/_npx/0f3e/node_modules/x/index.js":           "module.exports = 1",
	}
	const busy = ".npm/_npx/abc"

	t.Run("aggressive rules", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		startSleepIn(t, homePath(e, busy))
		before := snapshotEach(t, e, []string{busy})

		p, res := clean(t, e, aggressiveRules())

		requireSkip(t, e, p.Skipped, busy, plan.ReasonInUse)
		requireNotPlanned(t, e, p, busy)
		requireSurvived(t, e, before)
		requireMoved(t, e, res, ".npm/_npx/def")
		requireMoved(t, e, res, ".npm/_npx/0f3e")
	})

	t.Run("process starts after planning", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
		requirePlanned(t, e, p, busy)
		startSleepIn(t, homePath(e, busy))
		before := snapshotEach(t, e, []string{busy})

		res := applyPlan(t, e, p, engine.DefaultOptions())

		requireSkip(t, e, res.Skipped, busy, plan.ReasonInUse)
		requireNotMoved(t, e, res, busy)
		requireSurvived(t, e, before)
		requireMoved(t, e, res, ".npm/_npx/def")
	})

	t.Run("embedded rules", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		startSleepIn(t, homePath(e, busy))
		before := snapshotEach(t, e, []string{busy})

		clean(t, e, embeddedRules(t))

		requireSurvived(t, e, before)
	})
}

// Case 10, Mole #163: emptying the Trash ignored the user's whitelist. A
// whitelisted Trash item survives; the rest of the Trash is moved.
func TestWhitelistedTrashItemSurvives(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		".Trash/keep.pdf":             "%PDF-1.7 signed contract",
		".Trash/old-installer.dmg":    "dmg",
		".Trash/Old Folder/notes.txt": "old notes",
	}
	const keep = ".Trash/keep.pdf"

	t.Run("aggressive rules", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		whitelist(t, e, "~/.Trash/keep.pdf")
		before := snapshotEach(t, e, []string{keep})

		p, res := clean(t, e, aggressiveRules())

		requireSkip(t, e, p.Skipped, keep, plan.ReasonWhitelisted)
		requireNotPlanned(t, e, p, keep)
		requireSurvived(t, e, before)
		requireMoved(t, e, res, ".Trash/old-installer.dmg")
		requireMoved(t, e, res, ".Trash/Old Folder")
	})

	t.Run("embedded rules", func(t *testing.T) {
		t.Parallel()
		e := newHome(t, files)
		whitelist(t, e, "~/.Trash/keep.pdf")
		before := snapshotEach(t, e, []string{keep})

		clean(t, e, embeddedRules(t))

		requireSurvived(t, e, before)
	})
}
