// Package menu is the main menu of plain `sapu` on a terminal (design 04): the
// mascot on the left, seven numbered choices on the right.
//
// Safety rules the menu keeps:
//   - No choice moves or deletes anything by itself. Scan & review and the
//     folder explorer open the existing screens, which ask for their own
//     confirmation. Every other choice only shows information.
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
)

// New returns the menu screen as a model, for a test that drives it with
// messages. Run is the way to show it.
func New(ctx context.Context, b Backend, color bool) tea.Model { return newModel(ctx, b, color) }

// Run shows the menu until the user quits. Review and the explorer are
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

		var err error
		switch m.launch {
		case launchReview:
			err = b.Review(ctx)
		case launchAnalyze:
			err = b.Analyze(ctx)
		case launchNone:
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
