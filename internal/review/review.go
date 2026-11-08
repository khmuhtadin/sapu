// Package review is the interactive review screen of `sapu clean`: a
// bubbletea TUI that scans, lists the plan grouped by risk (SAFE, MODERATE,
// RISKY), lets the user change the selection, asks once on a confirm screen,
// moves the selected items to quarantine with engine.Apply (or deletes them
// for good, when the user chose that on the confirm screen) and shows a
// receipt.
//
// The screens are: scanning, list, confirm, applying, receipt.
//
// Safety rules the model keeps:
//   - Only y on the confirm screen starts a move. Nothing else does.
//   - Apply gets a copy of the plan that holds only the selected quarantine
//     items, with the default engine options (and the quarantine settings of sapu.toml):
//     no commands, and no --permanent unless the user pressed d on the
//     confirm screen, read the "for good" question it shows, and then y.
//   - RISKY items start collapsed. `a` and a group toggle never select them;
//     space on one shows why it is risky first and selects it only when
//     pressed a second time.
//   - Official-command items are listed for the user to run. They are never
//     selected or run from here.
//   - Ctrl-C while moving cancels the context, so the engine stops between
//     items. The screen stays until the engine returns, and Run never returns
//     while a move is running, so nothing stops halfway.
package review

import (
	"context"
	"errors"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// PlanFunc scans and plans, with the starting selection already set. It
// only reads. progress may be called from another goroutine.
type PlanFunc func(ctx context.Context, progress func(walk.Event)) (*plan.Plan, error)

// Outcome is what happened on the screen.
type Outcome struct {
	Result    *engine.Result // nil when nothing was applied
	Start     time.Time      // when the move began
	Err       error          // what engine.Apply returned
	Cancelled bool           // Ctrl-C or a signal stopped the screen or the move
	Deleted   bool           // the user chose delete for good and the engine emptied the run
}

// Run shows the screen until the user quits. The quarantine settings of cfg
// (max_age, max_size) go to engine.Apply and into the texts. why turns the detail of a
// protected skip into words (it may be nil). The error is for a scan or
// plan that failed, or a terminal that could not be used.
func Run(ctx context.Context, e env.Env, cfg config.Config, planFn PlanFunc, why func(detail string) string) (Outcome, error) {
	m := newModel(ctx, e, planFn, engine.Apply, why)
	m.cfg = cfg
	_, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	m.stopScan()

	out := Outcome{Cancelled: m.cancelled || ctx.Err() != nil}
	if m.run != nil {
		// The program can end while a move runs (a signal). Stop it between
		// items and wait: returning now could end the process mid-item.
		m.run.cancel()
		<-m.run.done
		out.Result, out.Err, out.Start = m.run.result, m.run.err, m.run.start
		out.Deleted = m.deleted()
	}
	switch {
	case errors.Is(err, tea.ErrInterrupted), errors.Is(err, tea.ErrProgramKilled):
		out.Cancelled = true
	case err != nil:
		return out, err
	}
	if m.fatal != nil {
		return out, m.fatal
	}
	return out, nil
}
