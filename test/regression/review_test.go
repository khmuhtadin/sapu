package regression

// Cases from an independent review of Sapu (Nov 2026). Each one moved user
// data before its fix.

import (
	"testing"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// Review case 6: an app kept writing deep inside a cache folder after the
// plan was made. Writing to a file does not change the folder's own identity,
// so the folder was moved with the new data inside. It is skipped as
// "changed" and left exactly as it is now.
func TestFolderChangedInsideAfterPlanningIsSkipped(t *testing.T) {
	t.Parallel()
	const edited = "Library/Caches/com.example.editor"
	e := newHome(t, map[string]string{
		junkFile:                         "ordinary cache",
		edited + "/sessions/current.db":  "session v1",
		edited + "/sessions/settings.db": "settings",
	})
	p := makePlan(t, e, scanItems(t, e, aggressiveRules()))
	requirePlanned(t, e, p, edited)

	appendFile(t, homePath(e, edited+"/sessions/current.db"), ", then unsaved work")
	before := snapshotEach(t, e, []string{edited})

	res := applyPlan(t, e, p, engine.DefaultOptions())

	requireSkip(t, e, res.Skipped, edited, plan.ReasonChanged)
	requireNotMoved(t, e, res, edited)
	requireSurvived(t, e, before)
	requireMoved(t, e, res, junkItem)
}

// Review case 5: a protected folder that is a symlink was protected only
// under the link's name. With ~/.claude moved into the caches and linked
// back, a rule for ~/Library/Caches/** took the Claude data under its real
// name. The same held for a whitelisted symlink.
func TestSymlinkedProtectedFolderIsProtectedWhereItLeads(t *testing.T) {
	t.Parallel()
	const (
		claudeData = "Library/Caches/claude-moved"
		keptData   = "Library/Caches/kept-project"
	)
	e := newHome(t, map[string]string{
		junkFile:                                 "ordinary cache",
		claudeData + "/projects/app/memory/m.md": "what the agent learned",
		keptData + "/notes.txt":                  "keep me",
	})
	symlink(t, homePath(e, ".claude"), homePath(e, claudeData))
	symlink(t, homePath(e, "kept"), homePath(e, keptData))
	whitelist(t, e, "~/kept")
	fixTimes(t, e.Home)
	before := snapshotEach(t, e, []string{claudeData, keptData})

	p, res := clean(t, e, aggressiveRules())

	requireSkip(t, e, p.Skipped, claudeData, plan.ReasonProtected)
	requireSkip(t, e, p.Skipped, keptData, plan.ReasonWhitelisted)
	requireNotMoved(t, e, res, claudeData)
	requireNotMoved(t, e, res, keptData)
	requireSurvived(t, e, before)
	requireMoved(t, e, res, junkItem)
}
