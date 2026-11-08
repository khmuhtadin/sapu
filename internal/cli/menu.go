package cli

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/menu"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/review"
)

// useMenu says whether plain `sapu` opens the menu: a person at a terminal,
// and nothing that asks for plain output. SAPU_NO_TUI turns the menu off.
func useMenu(cmd *cobra.Command) bool {
	return !jsonOut && os.Getenv("SAPU_NO_TUI") == "" &&
		isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout())
}

// colorOK says whether w may get color: a terminal, NO_COLOR unset.
func colorOK(w any) bool {
	return isTerminal(w) && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
}

// runMenu opens the menu. Its choices open the screens the commands use.
func runMenu(cmd *cobra.Command) error {
	return menu.Run(cmd.Context(), menuBackend(cmd), colorOK(cmd.OutOrStdout()))
}

func menuBackend(cmd *cobra.Command) menu.Backend {
	return menu.Backend{
		// The same screen as interactive `sapu clean`, with the same settings.
		// A partial or cancelled run already printed its receipt.
		Review: func(context.Context) error {
			return menuError(runClean(cmd, nil, &cleanupFlags{}, "", "", nil, nil))
		},
		Analyze: func(context.Context) error {
			return menuError(runAnalyze(cmd, nil, 20, 1))
		},
		Explain:    func(ctx context.Context) (string, error) { return commandText(ctx, "explain") },
		History:    func(ctx context.Context) (string, error) { return commandText(ctx, "history") },
		Doctor:     func(ctx context.Context) (string, error) { return commandText(ctx, "doctor") },
		Settings:   settingsText,
		Quarantine: menuQuarantine,
		Restore:    menuRestore,
		Delete:     menuDelete,
		Tools:      menuTools,
		Folder:     menuFolder,
		Apps:       menuApps,
		RunTool: func(ctx context.Context, id string, dirs []string, app string) error {
			return menuError(runTool(cmd, id, dirs, app))
		},
	}
}

// menuTools lists the tools for the menu, with the folders each looks in.
func menuTools() ([]menu.Tool, error) {
	e, err := loadEnv()
	if err != nil {
		return nil, err
	}
	var out []menu.Tool
	for _, t := range tools {
		mt := menu.Tool{ID: t.ID, Title: t.Title, Short: t.Short, Folders: t.Folders, App: t.App}
		if t.DefaultDirs != nil {
			dirs, err := t.DefaultDirs(e)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", t.Title, err)
			}
			for _, d := range dirs {
				mt.Dirs = append(mt.Dirs, tilde(e.Home, d))
			}
		}
		out = append(out, mt)
	}
	return out, nil
}

// menuFolder is the folder a user typed, ~ expanded, when it is a folder.
// The error says why it is not one.
func menuFolder(path string) (string, error) {
	e, err := loadEnv()
	if err != nil {
		return "", err
	}
	p, err := filepath.Abs(e.Expand(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", errors.New("it does not exist")
	case err != nil:
		return "", fmt.Errorf("cannot open it: %w", err)
	case !info.IsDir():
		return "", errors.New("it is a file, not a folder")
	}
	return p, nil
}

// menuApps lists the installed apps uninstall may remove: not Apple's (the
// rule pickApp keeps), by name.
func menuApps(ctx context.Context) ([]menu.App, error) {
	e, err := loadEnv()
	if err != nil {
		return nil, err
	}
	installed, err := apps.List(ctx, e)
	if err != nil {
		return nil, err
	}
	var out []menu.App
	for _, a := range installed {
		if !apps.IsApple(a.ID) {
			out = append(out, menu.App{Name: a.Name, Path: a.Path})
		}
	}
	slices.SortFunc(out, func(a, b menu.App) int {
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Path, b.Path))
	})
	return out, nil
}

// runTool runs the review screen of a tool, the way runReview does for
// clean, with the tool's name in the header. dirs nil means the tool's
// default folders; app is what `sapu uninstall APP` would get.
func runTool(cmd *cobra.Command, id string, dirs []string, app string) error {
	e, err := loadEnv()
	if err != nil {
		return err
	}
	title, planFn, err := toolScreen(e, id, dirs, app)
	if err != nil {
		return err
	}
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	res, err := review.RunTitled(cmd.Context(), e, cfg, title, planFn, why)
	return finishReview(cmd.OutOrStdout(), e.Home, cfg, res, err)
}

// toolScreen is the header and the plan of a tool's review screen.
func toolScreen(e env.Env, id string, dirs []string, app string) (string, review.PlanFunc, error) {
	t, ok := toolByID(id)
	if !ok {
		return "", nil, fmt.Errorf("no tool %q", id)
	}
	title := lowerFirst(t.Title)
	if t.App && app != "" {
		title = "uninstall " + strings.TrimSuffix(filepath.Base(app), ".app")
	}
	return title, t.Plan(e, dirs, app), nil
}

// lowerFirst makes "Build folders" "build folders" for a header, and leaves
// "iPhone and iPad backups" as it is.
func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[0]) && unicode.IsLower(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}

// menuError drops what the screen already told the user: a run that moved
// some items and skipped others, or a Ctrl-C.
func menuError(err error) error {
	var ee *exitError
	if errors.As(err, &ee) && (ee.code == output.ExitPartial || ee.code == output.ExitCancelled) {
		return nil
	}
	return err
}

// textMu serialises commandText: every command run builds the global flags.
var textMu sync.Mutex

// commandText runs a read-only command and returns what it prints, so a menu
// screen shows exactly what the command shows.
func commandText(ctx context.Context, args ...string) (string, error) {
	textMu.Lock()
	defer textMu.Unlock()
	var out, errOut bytes.Buffer
	if code := runArgs(ctx, args, strings.NewReader(""), &out, &errOut); code != output.ExitOK {
		return "", errors.New(strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// settingsText is the whitelist and the settings in effect.
func settingsText(ctx context.Context) (string, error) {
	whitelist, err := commandText(ctx, "whitelist", "list")
	if err != nil {
		return "", err
	}
	settings, err := commandText(ctx, "config", "show")
	if err != nil {
		return "", err
	}
	return "Protected paths (the whitelist)\n\n" + whitelist + "\nSettings\n\n" + settings, nil
}

func menuQuarantine() (menu.Quarantine, error) {
	e, err := loadEnv()
	if err != nil {
		return menu.Quarantine{}, err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return menu.Quarantine{}, err
	}
	runs, err := heldRuns(e)
	if err != nil {
		return menu.Quarantine{}, err
	}
	q := menu.Quarantine{Retention: retentionText(cfg)}
	for _, r := range runs {
		q.Runs = append(q.Runs, menu.Held{ID: r.ID, Started: r.Started, Bytes: r.Bytes, ExpiresAfter: r.ExpiresAfter})
	}
	return q, nil
}

// retentionText is the sentence the confirmation and the quarantine screen
// share, with the limits of sapu.toml.
func retentionText(cfg config.Config) string {
	return fmt.Sprintf("Quarantined files still use disk space. Runs can expire after %s, or earlier when quarantine exceeds %s. Restore them before expiry.",
		config.AgeWords(cfg.MaxAge), output.Bytes(cfg.MaxBytes))
}

// menuRestore puts a run back, the way `sapu undo RUN_ID` does.
func menuRestore(runID string) (string, error) {
	e, err := loadEnv()
	if err != nil {
		return "", err
	}
	restored, err := fsop.Restore(e, runID)
	conflicts := 0
	for _, r := range restored {
		if r.Conflict {
			conflicts++
		}
	}
	text := fmt.Sprintf("Restored %s from run %s.", plural(len(restored), "item"), runID)
	if conflicts > 0 {
		text += fmt.Sprintf("\n%s was already at the original place, so it was restored next to the original.", plural(conflicts, "item"))
	}
	return text, err
}

// menuDelete deletes runs from quarantine for good, the way
// `sapu quarantine empty RUN_ID` does for each.
func menuDelete(runIDs []string) (string, error) {
	e, err := loadEnv()
	if err != nil {
		return "", err
	}
	var total int64
	for i, id := range runIDs {
		freed, err := fsop.Empty(e, id)
		total += freed
		if err != nil {
			return fmt.Sprintf("Deleted %s of %s for good (%s).", plural(i, "run"), plural(len(runIDs), "run"), output.Bytes(total)),
				fmt.Errorf("run %s: %w", id, err)
		}
	}
	if len(runIDs) == 1 {
		return fmt.Sprintf("Deleted run %s for good. Freed %s.", runIDs[0], output.Bytes(total)), nil
	}
	return fmt.Sprintf("Deleted %s for good. Freed %s.", plural(len(runIDs), "run"), output.Bytes(total)), nil
}
