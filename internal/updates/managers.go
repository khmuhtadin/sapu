package updates

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// checkBrew asks Homebrew about casks.
func checkBrew(ctx context.Context) ([]Update, Source) {
	src := Source{ID: SourceHomebrew}
	if _, err := LookPath("brew"); err != nil {
		src.Skipped = "Homebrew is not installed"
		return nil, src
	}
	// env sets HOMEBREW_NO_AUTO_UPDATE so that brew does not update itself.
	out, err := RunCommand(ctx, "env", "HOMEBREW_NO_AUTO_UPDATE=1",
		"brew", "outdated", "--cask", "--greedy", "--json=v2")
	if err != nil {
		src.Skipped = "brew outdated failed: " + commandError(err)
		return nil, src
	}
	updates, err := parseBrew(out)
	if err != nil {
		src.Skipped = err.Error()
		return nil, src
	}
	return updates, src
}

// checkAppStore asks mas about App Store apps. mas contacts Apple, so
// offline skips it.
func checkAppStore(ctx context.Context, offline bool) ([]Update, Source) {
	src := Source{ID: SourceAppStore}
	if offline {
		src.Skipped = "skipped because of --offline"
		return nil, src
	}
	if _, err := LookPath("mas"); err != nil {
		src.Skipped = "mas is not installed"
		return nil, src
	}
	out, err := RunCommand(ctx, "mas", "outdated")
	if err != nil {
		src.Skipped = "mas outdated failed: " + commandError(err)
		return nil, src
	}
	return parseMas(out), src
}

// commandError is the first line of what a failed command printed, or the
// error itself.
func commandError(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		first, _, _ := strings.Cut(string(ee.Stderr), "\n")
		return first
	}
	return err.Error()
}
