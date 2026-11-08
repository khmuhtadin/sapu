// Package engine builds plans and applies them. Every way of changing the
// disk (clean, purge, analyze, later MCP) goes through Plan and Apply.
//
// Plan(items):
//  1. checks every item with protect (blocked -> Skip protected/whitelisted)
//     and inuse (Check and CheckConditions -> Skip in_use)
//  2. drops duplicates: an item whose path equals or sits inside another
//     remaining quarantine item's path is skipped as "duplicate"
//     (official_command items never cover others)
//  3. numbers the remaining items "0001", "0002", ... in path order
//  4. sets Selected: safe and moderate true, risky false
//  5. never writes anything to disk
//
// Apply(plan) refuses a plan whose Home differs from env.Home. When there is
// something to move it takes the state folder's lock (fsop.Lock) and holds it
// to the end, so expiry, every move and the permanent emptying of one Apply
// are covered and no other sapu process can empty or undo this run while it
// is applied. It expires old quarantine (fsop.Expire) unless
// Options.SkipExpire is set, opens a run (runlog.Start), and then, for each
// SELECTED item with action quarantine, the items without Requires first
// and then those with it, each group in plan order:
//  1. skips an item whose Requires names an item that was not moved in this
//     run as "requires"
//  2. takes a new open-files snapshot (inuse.Snapshot) when the one it has
//     is older than 3 seconds, and checks protect and inuse again (they may
//     have changed since planning)
//  3. for a folder, walks it (inuse.ChangedSince) and skips it as "changed"
//     when anything inside was modified after Plan.CreatedAt; a folder's
//     own identity does not cover its contents; a folder whose contents
//     cannot be read (no Full Disk Access) is skipped as "unreadable"
//  4. calls fsop.Quarantine, which re-checks identity before and after the
//     rename, or fsop.QuarantineAndClone for an item with CloneFrom
//  5. records skips and failures in the journal and the Result
//
// Requires (plan.Item): a selected item whose Requires does not name an
// item of the plan to quarantine that has no Requires of its own is refused
// into Failed before anything is moved. A plan without Requires is applied
// as it is: the plan is the user's.
//
// An item with CloneFrom (sapu dupes --replace-with-clone) passes the same
// gates, including its kept copy's identity (KeepCopy), and is then moved to
// quarantine and replaced by an APFS clone of the kept copy. It goes to
// Result.Moved and Result.Cloned. When it was moved but the clone could not
// be placed, it goes to Moved and to Failed, with an error that starts with
// fsop.ErrNotCloned's text; undo brings it back. A selected item whose
// CloneFrom is not exactly KeepCopy.Path, or that is not a quarantine item,
// is refused into Failed before anything is moved: a plan edited by hand
// must not pick the source of a clone.
//
// Items with action official_command are not run unless Options.RunCommands
// is set; without it they stay in the Result as Suggested, exactly as in M1.
// With RunCommands, Apply runs each selected official command AFTER the
// moves, in plan order (see command.go for the rules):
//  1. skips an item whose Requires names an item not moved in this run, and
//     checks protect, the whitelist, open files and protect_if again on the
//     item's path, as for a move
//  2. refuses a program that gains admin rights, deletes or moves files,
//     runs scripts or runs other programs (sudo, su, rm, mv, osascript, the
//     shells, env, xargs, ...): Result.Failed, never started
//  3. refuses any command Sapu does not make itself for the item's Source:
//     a cleanup rule's command (in the rules in use), sapu packages',
//     sapu optimize's, the git worktree suggestion's or uninstall's
//     tccutil; the program must be where PATH leads now
//  4. skips the item as "gone" when the program is not on PATH
//  5. journals command_begin, runs the program directly (no shell) in the
//     home folder with no input and a 10 minute limit, and journals command
//     with the exit code
//  6. records every started command in Result.Commands; a non-zero exit, a
//     timeout or a failure to start also goes to Result.Failed
//
// Commands are not undoable: undo restores only what was moved.
//
// With Options.Permanent, the run's quarantine is emptied after the moves
// (and commands), but never when a move failed: a failed item may have left
// something in quarantine (fsop.ErrStranded) that only undo can bring back.
// Clones are not in quarantine and stay; a clone that was not placed is a
// failure, so its item stays undoable.
// A failed command does not stop the emptying; it leaves nothing there.
//
// What is left open: an app can open or write an item after its checks and
// before or while it is moved. The open-files snapshot is at most about 3
// seconds old at each check, and lsof sees only files open at that moment.
// fsop's identity checks catch a file item that changed, and a folder whose
// own entry changed, but not a write deep inside a folder in that window.
//
// Implementation notes (W2-A):
//   - A blocked item leaves Plan.Items and appears once in Plan.Skipped.
//     Detail holds the checker's own text ("protected:claude",
//     "whitelisted:~/Documents", "open in node (pid 4242)") or, for a
//     duplicate, the item that covers it.
//   - Apply refuses a wrong schema version, another home, and options that
//     would expire the whole quarantine (MaxAge or MaxBytes not positive,
//     Now unset), all before writing anything.
//   - Apply creates a run only when at least one selected item is to be
//     quarantined, or run as a command with RunCommands. Otherwise
//     Result.RunID is empty and only expiry runs.
//   - Per item, Apply checks in this order: protect and whitelist, open
//     files, protect_if conditions, a folder's contents, then
//     fsop.Quarantine (path rules and identity). The first hit decides the
//     skip reason.
//   - A path fsop refuses (outside home, unclean, Sapu's own folder), an
//     item fsop left stranded in quarantine (fsop.ErrStranded) and an
//     unknown action go to Result.Failed. Changed and gone items are skips.
//   - Another sapu process holding the lock makes Apply fail with
//     fsop.ErrLocked before it moves anything. Apply with nothing to move
//     takes no lock of its own; fsop.Expire takes it when there are runs.
//   - On cancel, Apply stops between items, closes the run with its "end"
//     event, does not empty the quarantine even with Permanent, and returns
//     the partial Result with ctx.Err(). A running command is stopped
//     (SIGTERM to its process group, SIGKILL 5 seconds later) and no further
//     command starts. Commands never start after a cancelled move.
package engine

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/inuse"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/protect"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/runlog"
)

type Options struct {
	Permanent   bool          // empty this run's quarantine right after moving
	RunCommands bool          // run the selected official commands after the moves; not undoable
	SkipExpire  bool          // never expire old quarantine (the scheduled clean); Freed covers only this run
	MaxAge      time.Duration // quarantine retention
	MaxBytes    int64         // quarantine size cap
	Now         func() time.Time
	Progress    func(done, total int, it plan.Item) // may be nil
	Command     func(it plan.Item)                  // called right before each command starts; may be nil
}

// snapshotMaxAge is how old the open-files snapshot may get during Apply
// before it is taken again. Tests set it to zero.
var snapshotMaxAge = 3 * time.Second

func DefaultOptions() Options {
	return Options{MaxAge: fsop.DefaultMaxAge, MaxBytes: fsop.DefaultMaxBytes, Now: time.Now}
}

type Failure struct {
	Item  plan.Item `json:"item"`
	Error string    `json:"error"`
}

type Result struct {
	RunID     string          `json:"run_id"`
	Moved     []plan.Item     `json:"moved"`
	Skipped   []plan.Skip     `json:"skipped"`
	Failed    []Failure       `json:"failed"`
	Suggested []plan.Item     `json:"suggested"`          // official commands shown, not run
	Cloned    []plan.Item     `json:"cloned,omitempty"`   // moved and replaced by an APFS clone of the kept copy (CloneFrom)
	Commands  []CommandResult `json:"commands,omitempty"` // official commands run (RunCommands)
	Bytes     int64           `json:"bytes"`              // bytes moved into quarantine
	Freed     int64           `json:"freed"`              // bytes deleted for good now (expiry unless SkipExpire, + permanent)
}

func Plan(ctx context.Context, e env.Env, kind string, items []plan.Item) (*plan.Plan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	checker, err := protect.Load(e)
	if err != nil {
		return nil, err
	}
	open, err := inuse.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := &plan.Plan{
		SchemaVersion: plan.SchemaVersion,
		Kind:          kind,
		CreatedAt:     now,
		Home:          e.Home,
		Items:         []plan.Item{},
		Skipped:       []plan.Skip{},
	}

	// Path order puts a folder right before everything inside it.
	sorted := slices.Clone(items)
	slices.SortStableFunc(sorted, func(a, b plan.Item) int { return cmp.Compare(a.Path, b.Path) })

	var allowed []plan.Item
	for _, it := range sorted {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if skip, blocked := gate(e.StateDir, checker, open, it, now); blocked {
			p.Skipped = append(p.Skipped, skip)
			continue
		}
		allowed = append(allowed, it)
	}

	kept := map[string]string{} // path -> item id
	for _, it := range allowed {
		if path, id, ok := coveredBy(kept, it.Path); ok {
			p.Skipped = append(p.Skipped, skipOf(it, plan.ReasonDuplicate, fmt.Sprintf("covered by item %s (%s)", id, path)))
			continue
		}
		it.ID = fmt.Sprintf("%04d", len(p.Items)+1)
		it.Selected = it.Risk == rules.RiskSafe || it.Risk == rules.RiskModerate
		if it.Action != rules.ActionOfficialCommand {
			// A command never hides the items inside it (a worktree suggestion
			// and the worktree's node_modules): Apply moves them first and
			// runs the command after them, or only shows it.
			kept[it.Path] = it.ID
		}
		p.Items = append(p.Items, it)
	}
	slices.SortStableFunc(p.Skipped, func(a, b plan.Skip) int { return cmp.Compare(a.Path, b.Path) })
	return p, nil
}

// coveredBy finds a kept item whose path equals path or holds it.
func coveredBy(kept map[string]string, path string) (string, string, bool) {
	for p := path; ; p = filepath.Dir(p) {
		if id, ok := kept[p]; ok {
			return p, id, true
		}
		if p == filepath.Dir(p) {
			return "", "", false
		}
	}
}

// gate runs the gatekeepers on one item and returns a skip when the item
// must not be touched: protected or whitelisted, open in a process, or one of
// its protect_if conditions holds.
func gate(stateDir string, c *protect.Checker, o *inuse.Open, it plan.Item, now time.Time) (plan.Skip, bool) {
	if overlapsState(stateDir, it.Path) {
		return skipOf(it, plan.ReasonProtected, "protected:sapu-state"), true
	}
	if blocked, why := c.CheckSource(it.Path, it.Source); blocked {
		reason := plan.ReasonProtected
		if strings.HasPrefix(why, "whitelisted:") {
			reason = plan.ReasonWhitelisted
		}
		return skipOf(it, reason, why), true
	}
	if busy, why := o.Check(it.Path); busy {
		return skipOf(it, plan.ReasonInUse, why), true
	}
	if busy, why := o.CheckConditions(it.Path, it.ProtectIf, now); busy {
		return skipOf(it, plan.ReasonInUse, why), true
	}
	return plan.Skip{}, false
}

// overlapsState reports whether path is Sapu's own state folder, sits inside
// it, or contains it. fsop refuses these too (by device and inode); checking
// here shows them as protected in the plan. APFS ignores case, so this does.
func overlapsState(stateDir, path string) bool {
	s := strings.ToLower(filepath.Clean(stateDir))
	p := strings.ToLower(filepath.Clean(path))
	return p == s || strings.HasPrefix(p, s+"/") || strings.HasPrefix(s, p+"/")
}

func skipOf(it plan.Item, reason, detail string) plan.Skip {
	return plan.Skip{Path: it.Path, Source: it.Source, Reason: reason, Detail: detail}
}

func Apply(ctx context.Context, e env.Env, p *plan.Plan, opts Options) (*Result, error) {
	if p.SchemaVersion != plan.SchemaVersion {
		return nil, fmt.Errorf("plan schema_version %d is not supported (want %d)", p.SchemaVersion, plan.SchemaVersion)
	}
	if p.Home != e.Home {
		return nil, fmt.Errorf("plan was made for home %s, not %s", p.Home, e.Home)
	}
	if opts.Now == nil || opts.MaxAge <= 0 || opts.MaxBytes <= 0 {
		return nil, errors.New("engine options need Now and a positive MaxAge and MaxBytes; start from DefaultOptions")
	}
	// The changed-contents check compares against CreatedAt, so a plan dated
	// in the future would wave changed folders through.
	// Wall-clock time on purpose: opts.Now may be moved for expiry.
	if p.CreatedAt.After(time.Now().Add(time.Minute)) {
		return nil, fmt.Errorf("plan says it was made at %s, which is in the future; make a new plan", p.CreatedAt.Format(time.RFC3339))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{Moved: []plan.Item{}, Skipped: []plan.Skip{}, Failed: []Failure{}, Suggested: []plan.Item{}}
	req := &requires{paths: map[string]string{}, moved: map[string]bool{}}
	byID := map[string]plan.Item{}
	for _, it := range p.Items {
		byID[it.ID] = it
		req.paths[it.ID] = it.Path
	}
	var todo, dependent, cmds []plan.Item
	for _, it := range p.Items {
		required, found := byID[it.Requires]
		switch {
		case !it.Selected:
			// left alone
		case it.CloneFrom != "" && (it.Action != rules.ActionQuarantine || it.KeepCopy == nil || it.CloneFrom != it.KeepCopy.Path):
			res.Failed = append(res.Failed, Failure{Item: it, Error: "clone_from must be the path of keep_copy on an item to quarantine; was the plan edited by hand?"})
		case it.Requires != "" && (!found || required.Action != rules.ActionQuarantine || required.Requires != ""):
			res.Failed = append(res.Failed, Failure{Item: it, Error: fmt.Sprintf("requires %q, which is not an item of this plan to move; was the plan edited by hand?", it.Requires)})
		case it.Action == rules.ActionOfficialCommand && opts.RunCommands:
			cmds = append(cmds, it)
		case it.Action == rules.ActionOfficialCommand:
			res.Suggested = append(res.Suggested, it)
		case it.Action == rules.ActionQuarantine && it.Requires != "":
			dependent = append(dependent, it)
		case it.Action == rules.ActionQuarantine:
			todo = append(todo, it)
		default:
			res.Failed = append(res.Failed, Failure{Item: it, Error: fmt.Sprintf("unknown action %q", it.Action)})
		}
	}
	todo = append(todo, dependent...) // after the items they require
	var cleaners []rules.Rule
	if len(cmds) > 0 {
		var err error
		if cleaners, err = activeCleaners(e); err != nil {
			return nil, err
		}
	}
	busy := len(todo) > 0 || len(cmds) > 0
	if busy {
		unlock, err := fsop.Lock(e)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	if !opts.SkipExpire {
		freed, err := fsop.Expire(e, opts.Now(), opts.MaxAge, opts.MaxBytes)
		if err != nil {
			return nil, fmt.Errorf("expire quarantine: %w", err)
		}
		res.Freed = freed
	}
	if !busy {
		return res, nil
	}

	checker, err := protect.Load(e)
	if err != nil {
		return nil, err
	}
	g := &gatekeepers{stateDir: e.StateDir, protect: checker, taken: time.Now()}
	if g.open, err = inuse.Snapshot(ctx); err != nil {
		return nil, err
	}
	run, err := runlog.Start(e, p, opts.Now())
	if err != nil {
		return nil, err
	}
	res.RunID = run.ID

	runErr := moveAll(ctx, e, run, p.CreatedAt, g, req, todo, opts, res)
	moveFailed := len(res.Failed) > 0
	if runErr == nil {
		runErr = runCommands(ctx, e, run, g, cmds, req, cleaners, opts, res)
	}
	if err := run.Close(opts.Now(), true); err != nil {
		return res, errors.Join(runErr, err)
	}
	if runErr != nil {
		return res, runErr
	}
	if opts.Permanent && len(res.Moved) > 0 && !moveFailed {
		freed, err := fsop.Empty(e, run.ID)
		res.Freed += freed
		if err != nil {
			return res, fmt.Errorf("empty quarantine of run %s: %w", run.ID, err)
		}
	}
	return res, nil
}

// activeCleaners returns the cleanup rules in use: an installed rule update
// or the built-in rules (rulesupdate.Active).
func activeCleaners(e env.Env) ([]rules.Rule, error) {
	r, err := rulesupdate.Active(e)
	if err != nil {
		return nil, err
	}
	return rules.Load(r.Cleaners)
}

// requires knows, for Item.Requires, the path of every item of the plan and
// which items were moved in this run.
type requires struct {
	paths map[string]string // item id -> path
	moved map[string]bool   // item ids moved in this run
}

// skip returns a skip for it when it requires an item that was not moved.
// verb is "moved" or "run", for the detail.
func (r *requires) skip(it plan.Item, verb string) (plan.Skip, bool) {
	if it.Requires == "" || r.moved[it.Requires] {
		return plan.Skip{}, false
	}
	return skipOf(it, plan.ReasonRequires, fmt.Sprintf("not %s because %s was not moved", verb, r.paths[it.Requires])), true
}

// gatekeepers are what Apply checks again right before each item: the
// protection list and whitelist, and an open-files snapshot taken at taken.
type gatekeepers struct {
	stateDir string
	protect  *protect.Checker
	open     *inuse.Open
	taken    time.Time
}

// check runs gate on it, first taking a new open-files snapshot when the one
// it has is older than snapshotMaxAge. The error is the snapshot's.
func (g *gatekeepers) check(ctx context.Context, it plan.Item, now time.Time) (plan.Skip, bool, error) {
	if time.Since(g.taken) >= snapshotMaxAge {
		g.taken = time.Now()
		fresh, err := inuse.Snapshot(ctx)
		if err != nil {
			return plan.Skip{}, false, err
		}
		g.open = fresh
	}
	skip, blocked := gate(g.stateDir, g.protect, g.open, it, now)
	return skip, blocked, nil
}

// moveAll quarantines the items in order and fills res. It returns an error
// only when the whole run must stop: cancelled, a new open-files snapshot
// fails, or the journal cannot be written.
func moveAll(ctx context.Context, e env.Env, run *runlog.Run, planned time.Time, g *gatekeepers, req *requires, todo []plan.Item, opts Options, res *Result) error {
	for i, it := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		skip, blocked := req.skip(it, "moved")
		if !blocked {
			var err error
			skip, blocked, err = g.check(ctx, it, opts.Now())
			if err != nil {
				return err
			}
		}
		if !blocked {
			skip, blocked = changedInside(it, planned)
		}
		if !blocked {
			skip, blocked = keptCopyChanged(it)
		}
		if !blocked {
			quarantine := fsop.Quarantine
			if it.CloneFrom != "" {
				quarantine = fsop.QuarantineAndClone
			}
			err := quarantine(e, run, it)
			switch {
			case err == nil:
				res.Moved = append(res.Moved, it)
				res.Bytes += it.Bytes
				req.moved[it.ID] = true
				if it.CloneFrom != "" {
					res.Cloned = append(res.Cloned, it)
				}
			case errors.Is(err, fsop.ErrNotCloned): // in quarantine, nothing in its place
				res.Moved = append(res.Moved, it)
				res.Bytes += it.Bytes
				req.moved[it.ID] = true
				res.Failed = append(res.Failed, Failure{Item: it, Error: err.Error()})
			case errors.Is(err, fsop.ErrChanged):
				skip, blocked = skipOf(it, plan.ReasonChanged, err.Error()), true
			case errors.Is(err, fsop.ErrGone):
				skip, blocked = skipOf(it, plan.ReasonGone, err.Error()), true
			default: // ErrRefused (outside what Sapu may change) and ErrStranded included
				res.Failed = append(res.Failed, Failure{Item: it, Error: err.Error()})
			}
		}
		if blocked {
			res.Skipped = append(res.Skipped, skip)
			ev := runlog.Event{Time: opts.Now(), Type: runlog.EventSkip, Item: it.ID, From: it.Path, Reason: skip.Reason + ": " + skip.Detail}
			if err := run.Log(ev); err != nil {
				return err
			}
		}
		if opts.Progress != nil {
			opts.Progress(i+1, len(todo), it)
		}
	}
	return nil
}

// keptCopyChanged skips a duplicate whose kept copy is gone or changed since
// the plan was made (plan.Item.KeepCopy).
func keptCopyChanged(it plan.Item) (plan.Skip, bool) {
	if it.KeepCopy == nil {
		return plan.Skip{}, false
	}
	id, err := plan.IdentityOf(it.KeepCopy.Path)
	if err != nil || id != it.KeepCopy.Identity {
		return skipOf(it, plan.ReasonChanged, "the copy that stays ("+it.KeepCopy.Path+") is gone or changed"), true
	}
	return plan.Skip{}, false
}

// changedInside skips a folder item when anything inside it was modified
// after the plan was made at planned. The folder's identity covers only its
// own entry, not the files deeper inside. Files are left to fsop, whose
// identity check covers them.
func changedInside(it plan.Item, planned time.Time) (plan.Skip, bool) {
	if it.Identity.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return plan.Skip{}, false
	}
	changed, err := inuse.ChangedSince(it.Path, planned)
	if err != nil {
		return skipOf(it, plan.ReasonUnreadable, err.Error()), true
	}
	if changed {
		return skipOf(it, plan.ReasonChanged, "something inside changed after the plan was made"), true
	}
	return plan.Skip{}, false
}
