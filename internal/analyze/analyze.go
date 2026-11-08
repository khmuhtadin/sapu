// Package analyze is the disk explorer (blueprint F3): a bubbletea TUI that
// shows live totals while scanning, can be cancelled at any moment, lists
// entries largest first, drills into folders, and lets the user mark entries.
// Marked entries become plan items (Source "analyze", risk "risky", not
// regenerating) and go through engine.Plan, a confirmation, and engine.Apply.
//
// The screens are: scanning, browsing, planning, reviewing, applying, result.
// Nothing is written to disk until the user presses y on the review screen.
//
// Implemented in Wave 2 (W2-C).
package analyze

import (
	"context"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// Run starts the TUI at root. Moves use the [quarantine] settings of sapu.toml.
func Run(ctx context.Context, e env.Env, root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	m := newModel(ctx, e, abs)
	m.cfg = cfg
	m.planFn = engine.Plan
	m.applyFn = engine.Apply

	_, err = tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen()).Run()
	m.stopScan()
	if err != nil {
		return err
	}
	return m.fatal
}

// Top measures root and returns it with only the n largest children kept at
// each of the first depth levels. Used by `sapu analyze --json`.
// Totals (Bytes, Files) stay those of the whole tree.
func Top(ctx context.Context, root string, n, depth int) (*walk.Node, error) {
	tree, err := walk.Tree(ctx, root, nil)
	if err != nil {
		return nil, err
	}
	trim(tree, max(n, 0), depth)
	return tree, nil
}

// trim keeps the n largest children of node, and of their children, for depth
// levels. Children are already sorted largest first.
func trim(node *walk.Node, n, depth int) {
	if depth <= 0 {
		node.Children = nil
		return
	}
	if len(node.Children) > n {
		node.Children = node.Children[:n]
	}
	for _, c := range node.Children {
		trim(c, n, depth-1)
	}
}
