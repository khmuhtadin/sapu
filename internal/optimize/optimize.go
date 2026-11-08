// Package optimize lists what sapu optimize (F6) does. Every action clears
// a cache that macOS rebuilds by itself, through macOS's own command, and
// changes no setting, so there is nothing to roll back. None of them makes
// a Mac faster in general; each fixes one specific problem, and the text
// says which.
//
//   - Items returns the actions that need no admin rights as official_command
//     items, Source "optimize", risk safe. Their Path is the program (the
//     caches they clear are held open by macOS services and are not
//     readable, so there is no folder Sapu can measure) and Bytes is 0. An
//     action whose program is not on PATH is left out.
//   - Commands returns the commands of those actions. Apply runs an item of
//     Source "optimize" only when its command is one of them, with the
//     program where LookPath finds it then.
//   - Admin returns the actions that need admin rights. Sapu never runs sudo,
//     so these are only shown, with the exact command to type.
package optimize

import (
	"os/exec"
	"path/filepath"

	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
)

// Source is the Source of every item, and the plan kind.
const Source = plan.KindOptimize

// LookPath finds a program on PATH. Tests replace it.
var LookPath = exec.LookPath

// Intro and Rollback are what sapu optimize says before anything else.
const (
	Intro = "sapu optimize clears caches that macOS rebuilds by itself. Each one fixes a " +
		"specific problem (stale previews, broken fonts, DNS trouble); none of them makes " +
		"your Mac faster in general, so skip any whose problem you do not have."
	Rollback = "Nothing to roll back: these only clear caches macOS rebuilds."
)

type action struct {
	program string
	args    []string
	explain string // what it does, why it is safe, what you will notice
}

var actions = []action{
	{
		program: "qlmanage", args: []string{"-r", "cache"},
		explain: "Resets the QuickLook cache: the thumbnails and previews Finder shows for your files. " +
			"Safe, because it holds only pictures made from your files, and macOS makes them again. " +
			"You will notice thumbnails taking a moment to appear in each folder the first time. " +
			"It fixes previews that are stale, blank or wrong.",
	},
	{
		program: "atsutil", args: []string{"databases", "-removeUser"},
		explain: "Removes your user font caches. " +
			"Safe, because your fonts stay installed and macOS rebuilds the caches from them. " +
			"You will notice apps opening slowly the first time; log out and back in so every app uses the new caches. " +
			"It fixes fonts that look garbled, go missing or show as the wrong font.",
	},
}

// AdminAction is an action that needs admin rights, shown and never run.
type AdminAction struct {
	Title   string `json:"title"`
	Command string `json:"command"` // exactly what to type in Terminal
	Explain string `json:"explain"`
}

var admin = []AdminAction{
	{
		Title:   "Flush the DNS cache",
		Command: "sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder",
		Explain: "Forgets the website addresses macOS has looked up. " +
			"Safe, because macOS looks them up again as you browse. " +
			"You will notice nothing, except that a site whose address changed loads again. " +
			"It fixes sites that fail to load after a network or DNS change.",
	},
	{
		Title:   "Rebuild the Spotlight index",
		Command: "sudo mdutil -E /",
		Explain: "Erases the Spotlight index of the startup disk so that macOS builds it again. " +
			"Safe, because the index only helps search; your files are not touched. " +
			"You will notice incomplete search results, and a busy Mac, for an hour or more while it rebuilds. " +
			"It fixes searches that miss files you know are there.",
	},
}

// Items returns the actions Sapu can run, one item each.
func Items() []plan.Item {
	var items []plan.Item
	for _, a := range actions {
		path, err := LookPath(a.program)
		if err != nil {
			continue
		}
		id, err := plan.IdentityOf(path)
		if err != nil {
			continue
		}
		items = append(items, plan.Item{
			Source:      Source,
			Path:        filepath.Clean(path),
			Risk:        rules.RiskSafe,
			Regenerates: true,
			Explain:     a.explain,
			Action:      rules.ActionOfficialCommand,
			Command:     append([]string{path}, a.args...),
			Identity:    id,
		})
	}
	return items
}

// Commands returns the command of every action Items can return, program
// name first.
func Commands() [][]string {
	var out [][]string
	for _, a := range actions {
		out = append(out, append([]string{a.program}, a.args...))
	}
	return out
}

// Admin returns the actions that need admin rights.
func Admin() []AdminAction {
	out := make([]AdminAction, len(admin))
	copy(out, admin)
	return out
}
