// Package cli wires the commands. Each command lives in its own file and owns
// nothing but flag parsing, calling the packages above, and printing.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/mascot"
	"github.com/khmuhtadin/sapu/internal/output"
)

// Version is set at build time with -ldflags "-X github.com/khmuhtadin/sapu/internal/cli.Version=...".
var Version = "dev"

const rootLong = `Sapu shows where your Mac's disk space went and cleans it safely.

Safety: cleaned items go to a quarantine folder first, so "sapu undo"
can bring them back.

Run "sapu" on its own for the menu. In a script or a pipe it prints a summary
and the next steps instead.`

// Global flags.
var (
	jsonOut  bool
	progress string // "" or "ndjson"
)

// loadEnv returns the real environment. Tests replace it with
// func() (env.Env, error) { return env.New(t.TempDir()), nil }.
var loadEnv = env.FromOS

// newProgress reports long operations on stderr: an updating line on a
// terminal (never with --json), NDJSON with --progress ndjson.
func newProgress(cmd *cobra.Command) *output.Progress {
	return output.NewProgress(cmd.ErrOrStderr(), progress == "ndjson", !jsonOut && output.IsTerminal(os.Stderr))
}

// withMascot adds the mascot and a one-line introduction above the help of
// the root command, on an interactive terminal only. Piped help, --json and
// SAPU_NO_TUI stay plain, and so does every other command's help.
func withMascot(help func(*cobra.Command, []string)) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		if w := cmd.OutOrStdout(); cmd == cmd.Root() && !jsonOut && os.Getenv("SAPU_NO_TUI") == "" && isTerminal(w) {
			color := colorOK(w)
			fmt.Fprintln(w)
			for _, l := range mascot.Render(color) {
				fmt.Fprintln(w, l)
			}
			fmt.Fprintln(w, mascot.Accent("sapu", color)+" / CLI / Open source: a careful Mac cleanup.")
			fmt.Fprintln(w)
		}
		help(cmd, args)
	}
}

// exitError carries a specific exit code out of a command.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "sapu",
		Short:         "A Mac cleaner that never deletes anything you can't undo",
		Long:          rootLong,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          runSummary,
	}
	root.SetHelpFunc(withMascot(root.HelpFunc()))
	root.PersistentFlags().BoolVar(&jsonOut, "json", false, "print one JSON document on stdout")
	root.PersistentFlags().StringVar(&progress, "progress", "", `progress events on stderr: "ndjson"`)
	root.AddGroup(
		&cobra.Group{ID: "clean", Title: "Clean:"},
		&cobra.Group{ID: "look", Title: "Look:"},
		&cobra.Group{ID: "settings", Title: "Settings:"},
	)
	groups := map[string]*cobra.Command{
		"clean":      newCleanCmd(),
		"apply":      newApplyCmd(),
		"undo":       newUndoCmd(),
		"quarantine": newQuarantineCmd(),
		"purge":      newPurgeCmd(),
		"analyze":    newAnalyzeCmd(),
		"explain":    newExplainCmd(),
		"history":    newHistoryCmd(),
		"doctor":     newDoctorCmd(),
		"whitelist":  newWhitelistCmd(),
		"version":    newVersionCmd(),
		// M2 (Wave 5)
		"uninstall":   newUninstallCmd(),
		"login-items": newLoginItemsCmd(),
		"large":       newLargeCmd(),
		"backups":     newBackupsCmd(),
		"messages":    newMessagesCmd(),
		"trash":       newTrashCmd(),
		"installer":   newInstallerCmd(),
		"dupes":       newDupesCmd(),
		"packages":    newPackagesCmd(),
		"optimize":    newOptimizeCmd(),
		// M2 (Wave 6)
		"schedule": newScheduleCmd(),
		"alerts":   newAlertsCmd(),
		"mcp":      newMCPCmd(),
		"diff":     newDiffCmd(),
		"status":   newStatusCmd(),
		// M3 (Wave 8)
		"reset":   newResetCmd(),
		"similar": newSimilarCmd(),
		"tidy":    newTidyCmd(),
		"slim":    newSlimCmd(),
		"health":  newHealthCmd(),
		"updates": newUpdatesCmd(),
		"rules":   newRulesCmd(),
		"config":  newConfigCmd(),
	}
	for name, group := range map[string]string{
		"clean": "clean", "purge": "clean", "apply": "clean", "undo": "clean", "quarantine": "clean",
		"analyze": "look", "explain": "look", "history": "look", "doctor": "look",
		"whitelist": "settings", "version": "settings",
		"uninstall": "clean", "large": "clean", "backups": "clean", "messages": "clean",
		"trash": "clean", "installer": "clean", "dupes": "clean", "packages": "clean", "optimize": "clean",
		"login-items": "settings", "schedule": "settings", "alerts": "settings", "mcp": "settings",
		"diff": "look", "status": "look",
		"reset": "clean", "similar": "clean", "tidy": "clean",
		"slim": "look", "health": "look", "updates": "look", "rules": "settings",
		"config": "settings",
	} {
		groups[name].GroupID = group
		root.AddCommand(groups[name])
	}
	root.AddCommand(newAgentCmd()) // hidden: what the launchd agent runs
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runArgs(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

// runArgs runs one command line and returns its exit code. Tests call it
// through the sapu() helper.
func runArgs(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageErr(err)
	})
	// In --json mode stdout must hold exactly one envelope. If a command
	// failed without writing one, the error goes out as a JSONError.
	var wrote *writeTracker
	root.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if jsonOut {
			wrote = &writeTracker{w: cmd.OutOrStdout()}
			cmd.SetOut(wrote)
		}
	}
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return output.ExitOK
	}
	code, errCode := exitCodeOf(ctx, err)
	if code == output.ExitCancelled {
		err = errors.New("cancelled")
	}
	text, friendlyCode := friendlyErr(err)
	if friendlyCode != "" {
		errCode = friendlyCode
	}
	fmt.Fprintln(stderr, "sapu:", text)
	if jsonOut && (wrote == nil || !wrote.wrote) {
		// The same name the command uses when it succeeds: "quarantine-list".
		name := strings.TrimPrefix(cmd.CommandPath(), "sapu")
		if name = strings.ReplaceAll(strings.TrimSpace(name), " ", "-"); name == "" {
			name = "summary"
		}
		if jerr := output.JSONError(stdout, name, errCode, text); jerr != nil {
			fmt.Fprintln(stderr, "sapu:", jerr)
		}
	}
	return code
}

// exitCodeOf maps an error to the process exit code and a JSON error code.
func exitCodeOf(ctx context.Context, err error) (int, string) {
	if ctx.Err() != nil {
		return output.ExitCancelled, "cancelled"
	}
	var ee *exitError
	switch {
	case errors.As(err, &ee):
		switch ee.code {
		case output.ExitUsage:
			return ee.code, "usage"
		case output.ExitPartial:
			return ee.code, "partial"
		}
		return ee.code, "error"
	case strings.HasPrefix(err.Error(), "unknown command") || strings.Contains(err.Error(), "arg(s)"):
		return output.ExitUsage, "usage" // cobra's own argument errors
	}
	return output.ExitError, "error"
}

// writeTracker remembers whether anything was written to stdout.
type writeTracker struct {
	w     io.Writer
	wrote bool
}

func (t *writeTracker) Write(b []byte) (int, error) {
	if len(b) > 0 {
		t.wrote = true
	}
	return t.w.Write(b)
}

// usageErr marks err as a usage mistake (exit code 2).
func usageErr(err error) error { return &exitError{code: output.ExitUsage, err: err} }

// partialErr marks a run where some items were skipped or failed (exit code 3).
func partialErr(err error) error { return &exitError{code: output.ExitPartial, err: err} }
