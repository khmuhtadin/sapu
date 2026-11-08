package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/menu"
	"github.com/khmuhtadin/sapu/internal/output"
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
	}
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
