// Package menu is the main menu of plain `sapu` on a terminal (design 04): the
// mascot on the left, seven numbered choices on the right.
//
// Safety rules the menu keeps:
//   - No choice moves or deletes anything by itself. Scan & review, the
//     folder explorer and every tool open the existing screens, which ask for
//     their own confirmation: a tool only looks, then hands its plan to the
//     review screen, and nothing moves until y there. Every other choice only
//     shows information.
//   - The tool screens only read: they list the tools, check a folder the
//     user typed and list the installed apps. esc on any of them runs nothing.
//   - The only changes the menu can make are on the quarantine screen:
//     restoring a run, or deleting one run or all runs for good. Each needs
//     y on its own confirmation. Enter never confirms.
//
// The menu does not know the commands. The CLI hands it a Backend.
package menu

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Backend is what the menu needs from the CLI. Every function but Restore
// and Delete only reads.
type Backend struct {
	Review     func(ctx context.Context) error           // the review screen of `sapu clean`
	Analyze    func(ctx context.Context) error           // the folder explorer of `sapu analyze`
	Explain    func(ctx context.Context) (string, error) // the text of `sapu explain`
	History    func(ctx context.Context) (string, error) // the text of `sapu history`
	Doctor     func(ctx context.Context) (string, error) // the text of `sapu doctor`
	Settings   func(ctx context.Context) (string, error) // whitelist and settings, as text
	Quarantine func() (Quarantine, error)                // runs held in quarantine
	Restore    func(runID string) (string, error)        // like `sapu undo RUN_ID`; returns what to tell the user
	Delete     func(runIDs []string) (string, error)     // like `sapu quarantine empty RUN_ID`, for each run; returns what to tell the user

	Tools  func() ([]Tool, error)                   // the tools, in menu order
	Folder func(path string) (string, error)        // the folder path names, ~ expanded; the error says why it is not one
	Apps   func(ctx context.Context) ([]App, error) // the installed apps a user may remove, by name
	// RunTool runs the review screen of a tool and prints its receipt. dirs
	// nil means the tool's default folders; app is the app to remove.
	RunTool func(ctx context.Context, id string, dirs []string, app string) error
}

// Tool is one entry of the Tools screen.
type Tool struct {
	ID      string
	Title   string
	Short   string   // what it finds, one sentence
	Folders bool     // it looks in folders the user can change
	App     bool     // the user picks an app first
	Dirs    []string // the folders it looks in by default, home shown as ~
}

// App is one installed app the user can pick.
type App struct {
	Name string
	Path string // the .app bundle, absolute; RunTool gets it as the app
}

// Quarantine is what the quarantine screen shows.
type Quarantine struct {
	Runs      []Held
	Retention string // how long runs are kept, from sapu.toml
}

// Held is one run that still holds items in quarantine.
type Held struct {
	ID           string
	Started      time.Time
	Bytes        int64
	ExpiresAfter time.Time
}

// launch says which screen the menu leaves to open.
type launch int

const (
	launchNone launch = iota
	launchReview
	launchAnalyze
	launchTool
)

// toolRun is the tool the menu leaves to run.
type toolRun struct {
	id   string
	dirs []string
	app  string
}

// New returns the menu screen as a model, for a test that drives it with
// messages. Run is the way to show it.
func New(ctx context.Context, b Backend, color bool) tea.Model { return newModel(ctx, b, color) }

// Run shows the menu until the user quits. Review, the explorer and the tools are
// programs of their own: the menu closes, runs one, and opens again at the
// same choice, so leaving a screen returns to the menu. An error of a screen
// shows on the menu; only a cancelled ctx or a terminal that cannot be used
// ends Run with an error.
func Run(ctx context.Context, b Backend, color bool) error {
	cursor, notice := 0, ""
	for {
		m := newModel(ctx, b, color)
		m.cursor, m.notice = cursor, notice
		if _, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen()).Run(); err != nil {
			return err
		}
		cursor, notice = m.cursor, ""

		again, err := runLaunched(ctx, b, m)
		if !again {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			notice = "Could not finish: " + err.Error()
		}
	}
}

// runLaunched runs the screen the menu closed for. again is false when the
// user quit the menu.
func runLaunched(ctx context.Context, b Backend, m *model) (again bool, err error) {
	switch m.launch {
	case launchReview:
		return true, b.Review(ctx)
	case launchAnalyze:
		return true, b.Analyze(ctx)
	case launchTool:
		return true, b.RunTool(ctx, m.run.id, m.run.dirs, m.run.app)
	}
	return false, nil
}
