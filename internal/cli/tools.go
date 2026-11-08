package cli

import (
	"context"

	"github.com/khmuhtadin/sapu/internal/dupes"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/similar"
	"github.com/khmuhtadin/sapu/internal/tidy"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// tool is one entry of the menu's Tools screen: a command
// the user can run by picking it from a list instead of typing it.
//
// Contract shared by the menu, the typed commands and the Mac app:
//   - Plan only reads. It returns the PlanFunc the review screen runs; the
//     plan comes with its starting selection set (what the typed command
//     selects by default; nothing for tools whose items need a person to
//     choose, such as large, backups and similar).
//   - dirs nil means the tool's default folders (DefaultDirs).
//   - app is the app to remove; only uninstall reads it, and it is the same
//     query `sapu uninstall APP` takes (a name, a path or a bundle id).
type tool struct {
	ID      string // the command name, e.g. "purge" or "login-items"
	Title   string // the menu line, e.g. "Build folders"
	Short   string // one sentence: what it finds
	Folders bool   // it looks in folders the user can change
	App     bool   // the user picks an app first (uninstall)
	// DefaultDirs are the folders it looks in when dirs is nil. nil for a
	// tool without folders.
	DefaultDirs func(e env.Env) ([]string, error)
	Plan        func(e env.Env, dirs []string, app string) review.PlanFunc
}

// tools are the entries, in menu order.
var tools = []tool{
	{ID: "uninstall", Title: "Uninstall an app", Short: "Remove an app and the files it left behind", App: true, Plan: uninstallTool},
	{ID: "purge", Title: "Build folders", Short: "Find build folders in your projects (node_modules, target, ...)", Folders: true, DefaultDirs: purgeDefaultDirs, Plan: purgeTool},
	{ID: "large", Title: "Large old files", Short: "Find big files you have not opened in a long time", Folders: true, DefaultDirs: homeDir, Plan: largeTool},
	{ID: "dupes", Title: "Duplicate files", Short: "Find extra copies of the same file", Folders: true, DefaultDirs: rootsOf(dupes.DefaultRoots), Plan: dupesTool},
	{ID: "similar", Title: "Similar photos", Short: "Find photos that look alike and keep the best one", Folders: true, DefaultDirs: rootsOf(similar.DefaultRoots), Plan: similarTool},
	{ID: "installer", Title: "Old installers", Short: "Find old .dmg, .pkg and .iso files", Folders: true, DefaultDirs: rootsOf(installerDefaultDirs), Plan: installerTool},
	{ID: "tidy", Title: "Empty folders and broken links", Short: "Find empty folders, broken links and empty files", Folders: true, DefaultDirs: rootsOf(tidy.DefaultRoots), Plan: tidyTool},
	{ID: "trash", Title: "Empty the Trash", Short: "Empty the Trash through quarantine, hidden files included", Plan: trashTool},
	{ID: "backups", Title: "iPhone and iPad backups", Short: "Find old device backups", Plan: backupsTool},
	{ID: "login-items", Title: "Login items", Short: "Stop things from starting at login", Plan: loginItemsTool},
}

// homeDir is the default folder of large: the home folder.
func homeDir(e env.Env) ([]string, error) { return []string{e.Home}, nil }

// rootsOf turns a list of default folders that cannot fail into DefaultDirs.
func rootsOf(roots func(env.Env) []string) func(env.Env) ([]string, error) {
	return func(e env.Env) ([]string, error) { return roots(e), nil }
}

// planned is a PlanFunc that returns a plan already made.
func planned(p *plan.Plan) review.PlanFunc {
	return func(context.Context, func(walk.Event)) (*plan.Plan, error) { return p, nil }
}

// toolByID returns the tool with that id.
func toolByID(id string) (tool, bool) {
	for _, t := range tools {
		if t.ID == id {
			return t, true
		}
	}
	return tool{}, false
}
