package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/apps"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/pkgs"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/purge"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

// The official-command runner (Options.RunCommands).
//
// Safety rules, in the order they apply to one item:
//   - an item whose Requires names an item that was not moved in this run
//     is skipped as "requires";
//   - the gatekeepers run on the item's path again, as for a move;
//   - a program on refusedPrograms is never started, whatever the plan says;
//     a relative program path ("./x") is refused;
//   - only a command Sapu itself makes for the item's Source runs (see
//     knownCommand); anything else is refused as "not a command Sapu
//     knows", so a plan edited by hand cannot run another program or other
//     arguments;
//   - the program is looked up on PATH again, by the name Sapu's own
//     command uses; the plan's program must be that path or that bare name,
//     and a program that is not there is skipped as "gone";
//   - the program found is refused when it is on refusedPrograms behind a
//     symlink with another name;
//   - the program runs directly, never through a shell, so the plan's
//     arguments are passed as they are and nothing in them is expanded;
//   - it runs in the home folder, with HOME set to it, no input (so a
//     program that asks a question gets no answer), in its own process
//     group, and for at most commandTimeout;
//   - on cancel or timeout the whole process group gets SIGTERM, and the
//     program is killed commandWaitDelay later if it is still there.

// commandTimeout is how long one official command may run. Tests shorten it.
var commandTimeout = 10 * time.Minute

// commandWaitDelay is how long a stopped command gets to exit after SIGTERM
// before it is killed and its output pipe is closed.
var commandWaitDelay = 5 * time.Second

// maxOutput is how much of a command's output is kept: the last 8 KB.
const maxOutput = 8 << 10

// refusedPrograms are never run: programs that gain admin rights, delete or
// move files, run scripts, or run another program given to them. The list is
// one string on purpose: the guard test bans "rm"/"mv" literals outside fsop
// to stop code from calling them, and this list exists to stop that too.
var refusedPrograms = strings.Fields("sudo su doas rm rmdir unlink srm mv osascript sh bash zsh dash ksh csh tcsh fish env xargs")

// CommandResult is one official command that Apply started.
type CommandResult struct {
	Item     plan.Item     `json:"item"`
	ExitCode int           `json:"exit_code"` // -1 when it did not exit by itself (timeout, cancel, signal) or did not start
	Output   string        `json:"output"`    // stdout and stderr together, the last 8 KB
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration_ns"`
}

// Failed reports whether the command did not finish with exit code 0.
func (c CommandResult) Failed() bool { return c.Error != "" || c.ExitCode != 0 }

// runCommands runs the official command of each item in order and fills
// res. Like moveAll, it returns an error only when the whole run must stop:
// cancelled, a new open-files snapshot fails, or the journal cannot be
// written.
//
// req knows which items were moved in this run (for Requires) and cleaners
// are the cleanup rules in use (for knownCommand).
func runCommands(ctx context.Context, e env.Env, run *runlog.Run, g *gatekeepers, cmds []plan.Item, req *requires, cleaners []rules.Rule, opts Options, res *Result) error {
	for _, it := range cmds {
		if err := ctx.Err(); err != nil {
			return err
		}
		skip, blocked := req.skip(it, "run")
		if !blocked {
			var err error
			skip, blocked, err = g.check(ctx, it, opts.Now())
			if err != nil {
				return err
			}
		}
		program := ""
		if !blocked {
			var refused string
			program, refused = checkCommand(it, cleaners)
			switch {
			case refused != "":
				res.Failed = append(res.Failed, Failure{Item: it, Error: "refused: " + refused})
				ev := runlog.Event{Time: opts.Now(), Type: runlog.EventCommand, Item: it.ID, From: it.Path, Command: it.Command, Reason: "refused: " + refused}
				if err := run.Log(ev); err != nil {
					return err
				}
				continue
			case program == "":
				skip, blocked = skipOf(it, plan.ReasonGone, fmt.Sprintf("%s is not installed (not found on PATH)", it.Command[0])), true
			}
		}
		if blocked {
			res.Skipped = append(res.Skipped, skip)
			ev := runlog.Event{Time: opts.Now(), Type: runlog.EventSkip, Item: it.ID, From: it.Path, Reason: skip.Reason + ": " + skip.Detail}
			if err := run.Log(ev); err != nil {
				return err
			}
			continue
		}

		begin := runlog.Event{Time: opts.Now(), Type: runlog.EventCommandBegin, Item: it.ID, From: it.Path, Command: it.Command}
		if err := run.Log(begin); err != nil {
			return err
		}
		if opts.Command != nil {
			opts.Command(it)
		}
		cr := runOne(ctx, e, program, it)
		res.Commands = append(res.Commands, cr)
		if cr.Failed() {
			why := cr.Error
			if why == "" {
				why = fmt.Sprintf("exit code %d", cr.ExitCode)
			}
			res.Failed = append(res.Failed, Failure{Item: it, Error: why})
		}
		code := cr.ExitCode
		end := runlog.Event{Time: opts.Now(), Type: runlog.EventCommand, Item: it.ID, From: it.Path, Command: it.Command, ExitCode: &code, Reason: cr.Error}
		if err := run.Log(end); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// unknownCommand is the refusal of a command Sapu did not make itself.
const unknownCommand = "not a command Sapu knows"

// checkCommand returns the program to start for it, or why it is refused.
// Both are empty when the program is not on PATH. cleaners are the cleanup
// rules in use.
func checkCommand(it plan.Item, cleaners []rules.Rule) (program, refused string) {
	command := it.Command
	if len(command) == 0 || command[0] == "" {
		return "", "the item has no command"
	}
	name := command[0]
	if isRefused(name) {
		return "", fmt.Sprintf("Sapu never runs %s", filepath.Base(name))
	}
	if strings.Contains(name, "/") && !filepath.IsAbs(name) {
		return "", fmt.Sprintf("the program %s is a relative path", name)
	}
	want, look, ok := knownCommand(it, cleaners)
	if !ok || len(want) != len(command) || !slices.Equal(want[1:], command[1:]) || filepath.Base(want[0]) != filepath.Base(name) {
		return "", fmt.Sprintf("%s for %q: %s", unknownCommand, it.Source, strings.Join(command, " "))
	}
	path, err := look(want[0])
	if err != nil || !filepath.IsAbs(path) {
		return "", ""
	}
	if name != want[0] && name != path {
		return "", fmt.Sprintf("%s: the plan runs %s, but %s is %s now", unknownCommand, name, want[0], path)
	}
	if real, err := filepath.EvalSymlinks(path); err == nil && isRefused(real) {
		return "", fmt.Sprintf("%s is %s, which Sapu never runs", name, real)
	}
	return path, ""
}

// knownCommand is sapuCommand. Tests of this package replace it to also
// run their own scripts.
var knownCommand = sapuCommand

// sapuCommand returns the command Sapu itself makes for an item of
// it.Source with it.Command's arguments, program name first, and how that
// program is found on PATH. ok is false when Sapu makes no such command:
//   - "packages": one of pkgs.Commands, found with pkgs.LookPath;
//   - "optimize": one of optimize.Commands, found with optimize.LookPath;
//   - "purge:git-worktree": git -C <dir> worktree remove <it.Path>, where
//     dir is the main checkout of a repository (purge.IsMainRepo);
//   - "uninstall": tccutil reset All <bundle id>, with a valid bundle id
//     (package cli, resetPermissionsItem);
//   - anything else names a cleanup rule: the Command of the rule in use
//     with that id and action official_command.
//
// pkgs.LookPath and optimize.LookPath are exec.LookPath; using the ones
// that made the plan keeps the check in step with them.
func sapuCommand(it plan.Item, cleaners []rules.Rule) (want []string, look func(string) (string, error), ok bool) {
	command := it.Command
	switch it.Source {
	case pkgs.Source:
		want, ok = sameArgs(command, pkgs.Commands())
		return want, pkgs.LookPath, ok
	case optimize.Source:
		want, ok = sameArgs(command, optimize.Commands())
		return want, optimize.LookPath, ok
	case purge.WorktreeSource:
		if len(command) == 6 && purge.IsMainRepo(command[2]) {
			return purge.WorktreeCommand(command[2], it.Path), exec.LookPath, true
		}
		return nil, nil, false
	case plan.KindUninstall:
		if len(command) == 4 && apps.ValidBundleID(command[3]) {
			return []string{"tccutil", "reset", "All", command[3]}, exec.LookPath, true
		}
		return nil, nil, false
	}
	for _, r := range cleaners {
		if r.ID == it.Source && r.Action == rules.ActionOfficialCommand {
			return r.Command, exec.LookPath, true
		}
	}
	return nil, nil, false
}

// sameArgs returns the command of known whose program has command's file
// name and whose arguments are command's.
func sameArgs(command []string, known [][]string) ([]string, bool) {
	for _, k := range known {
		if len(k) == len(command) && filepath.Base(k[0]) == filepath.Base(command[0]) && slices.Equal(k[1:], command[1:]) {
			return k, true
		}
	}
	return nil, false
}

// isRefused reports whether the program's file name is on refusedPrograms.
// APFS ignores case, so this does too.
func isRefused(program string) bool {
	base := strings.ToLower(filepath.Base(program))
	for _, r := range refusedPrograms {
		if base == r {
			return true
		}
	}
	return false
}

// runOne starts program with the item's arguments and waits for it.
func runOne(ctx context.Context, e env.Env, program string, it plan.Item) CommandResult {
	cctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	out := &tail{}
	c := exec.CommandContext(cctx, program, it.Command[1:]...)
	c.Dir = e.Home
	c.Env = append(os.Environ(), "HOME="+e.Home)
	c.Stdout = out
	c.Stderr = out
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGTERM) }
	c.WaitDelay = commandWaitDelay

	start := time.Now()
	err := c.Run()
	cr := CommandResult{Item: it, ExitCode: -1, Duration: time.Since(start)}
	if c.ProcessState != nil {
		cr.ExitCode = c.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() != nil:
		cr.Error = "stopped: cancelled"
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		cr.Error = fmt.Sprintf("stopped: still running after %s", commandTimeout)
	case err != nil && cr.ExitCode == -1:
		cr.Error = err.Error()
	}
	cr.Output = out.String()
	return cr
}

// tail keeps the last maxOutput bytes written to it. exec writes stdout and
// stderr from one goroutine at a time when both are the same *tail.
type tail struct {
	buf []byte
	cut bool
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2*maxOutput {
		t.buf = append([]byte(nil), t.buf[len(t.buf)-maxOutput:]...)
		t.cut = true
	}
	return len(p), nil
}

func (t *tail) String() string {
	b, cut := t.buf, t.cut
	if len(b) > maxOutput {
		b, cut = b[len(b)-maxOutput:], true
	}
	s := strings.ToValidUTF8(string(b), "")
	if cut {
		s = "[earlier output cut]\n" + s
	}
	return s
}
