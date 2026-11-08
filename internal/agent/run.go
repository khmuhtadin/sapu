package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/rulesupdate"
	"github.com/khmuhtadin/sapu/internal/safefile"
	"github.com/khmuhtadin/sapu/internal/scan"
	"github.com/khmuhtadin/sapu/internal/trends"
)

// How long free-space samples are kept, and the window of the drop alert.
const (
	keepSamples = 8 * 24 * time.Hour
	dropWindow  = 24 * time.Hour
	alertEvery  = 24 * time.Hour // at most one notification per alert kind
)

// Osascript runs /usr/bin/osascript -e script. It is the only use of
// osascript in Sapu, and only shows notifications. Tests replace it.
var Osascript = func(script string) error {
	out, err := exec.Command("/usr/bin/osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cleanerRules returns the cleanup rules in use: an installed rule update
// or the built-in rules (rulesupdate.Active). Tests replace it.
var cleanerRules = func(e env.Env) ([]rules.Rule, error) {
	r, err := rulesupdate.Active(e)
	if err != nil {
		return nil, err
	}
	return rules.Load(r.Cleaners)
}

// apply is engine.Apply. Tests replace it to check the options.
var apply = engine.Apply

// Notify shows a macOS notification titled "Sapu".
func Notify(text string) error {
	return Osascript(`display notification "` + quote(text) + `" with title "Sapu"`)
}

// quote escapes text for an AppleScript string literal.
func quote(text string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
}

// Run is one round of the agent, run by launchd every hour:
//  1. when another sapu holds fsop.Lock, it returns nil and changes nothing;
//  2. alerts (when on): records free space, notifies when it is low or fell fast;
//  3. scheduled clean (when on and due): safe caches into quarantine; it
//     never expires or empties quarantine, so nothing is deleted for good
//     (expiry happens only when the user runs sapu);
//  4. size snapshot (when alerts or schedule are on): one a day;
//  5. saves the state.
//
// Every step writes one line to agent.log. Errors go to the log, and a
// failed step shows one notification (at most one a day).
func Run(ctx context.Context, e env.Env, now time.Time) (err error) {
	unlock, err := fsop.Lock(e)
	if errors.Is(err, fsop.ErrLocked) {
		return nil // another sapu is working; the next round runs in an hour
	}
	if err != nil {
		return err
	}
	defer unlock()

	// Never appended through a symlink or a hard link, or outside the state
	// folder: a linked agent.log is refused before anything is written.
	logFile, err := safefile.Open(e.StateDir, LogPath(e), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, logFile.Close()) }()
	r := &runner{e: e, now: now, log: logFile}

	st, err := LoadState(e)
	if err != nil {
		r.logf("state: %v; starting with an empty state", err)
		st = State{}
	}
	if st.Notified == nil {
		st.Notified = map[string]time.Time{}
	}
	r.st = &st

	var failed error
	cfg, err := LoadConfig(e)
	switch {
	case err != nil:
		failed = r.fail("read the settings", fmt.Errorf("%w; run sapu schedule on or sapu alerts on again", err))
	case !cfg.Alerts.Enabled && !cfg.Schedule.Enabled:
		r.logf("nothing is on; run sapu schedule off to remove the agent")
	default:
		if cfg.Alerts.Enabled {
			failed = errors.Join(failed, r.alerts(cfg.Alerts))
		}
		if cfg.Schedule.Enabled {
			failed = errors.Join(failed, r.clean(ctx, cfg.Schedule))
		}
		failed = errors.Join(failed, r.snapshot(ctx))
	}

	st.LastCheck = &now
	if err := saveState(e, st); err != nil {
		r.logf("error: save the state: %v", err)
		return errors.Join(failed, err)
	}
	return failed
}

type runner struct {
	e   env.Env
	now time.Time
	log io.Writer
	st  *State
}

// logf writes one line to agent.log.
func (r *runner) logf(format string, args ...any) {
	fmt.Fprintf(r.log, "%s %s\n", r.now.Format(time.RFC3339), fmt.Sprintf(format, args...))
}

// fail logs a failed step, shows one notification a day about failures,
// and returns the error.
func (r *runner) fail(step string, err error) error {
	err = fmt.Errorf("%s: %w", step, err)
	r.logf("error: %v", err)
	r.notify(AlertFailed, "Sapu's scheduled run failed: "+err.Error())
	return err
}

// notify shows text unless an alert of this kind was shown in the last 24
// hours. It reports whether it showed one.
func (r *runner) notify(kind, text string) bool {
	if last, ok := r.st.Notified[kind]; ok && r.now.Sub(last) < alertEvery {
		return false
	}
	if err := Notify(text); err != nil {
		r.logf("error: notify: %v", err)
		return false
	}
	r.st.Notified[kind] = r.now
	return true
}

// alerts records free space and notifies when it is below MinFreeBytes or
// fell more than DropBytesPerDay in the last 24 hours.
func (r *runner) alerts(c Alerts) error {
	free, err := FreeBytes(r.e)
	if err != nil {
		return r.fail("alerts", err)
	}
	drop := dropSince(r.st.Samples, r.now, free)
	r.st.Samples = keepRecent(append(r.st.Samples, Sample{Time: r.now, FreeBytes: free}), r.now)

	line := output.Bytes(free) + " free"
	if c.MinFreeBytes > 0 && free < c.MinFreeBytes {
		text := fmt.Sprintf("Only %s free, below your %s alert. Run sapu to see what to clean, or sapu explain.",
			output.Bytes(free), output.Bytes(c.MinFreeBytes))
		line += ", below " + output.Bytes(c.MinFreeBytes) + sent(r.notify(AlertLow, text))
	}
	if c.DropBytesPerDay > 0 && drop > c.DropBytesPerDay {
		text := fmt.Sprintf("Free space fell by %s in the last 24 hours, to %s. Run sapu to see what grew, or sapu explain.",
			output.Bytes(drop), output.Bytes(free))
		line += ", fell " + output.Bytes(drop) + " in 24 h" + sent(r.notify(AlertDrop, text))
	}
	r.logf("alerts: %s", line)
	return nil
}

func sent(notified bool) string {
	if notified {
		return " (notified)"
	}
	return " (already notified today)"
}

// dropSince returns how far free space fell from its highest sample in the
// last 24 hours to free; 0 without such a sample.
func dropSince(samples []Sample, now time.Time, free int64) int64 {
	var drop int64
	for _, s := range samples {
		if s.Time.After(now.Add(-dropWindow)) && s.Time.Before(now) && s.FreeBytes-free > drop {
			drop = s.FreeBytes - free
		}
	}
	return drop
}

// keepRecent drops samples older than about 8 days.
func keepRecent(samples []Sample, now time.Time) []Sample {
	var kept []Sample
	for _, s := range samples {
		if s.Time.After(now.Add(-keepSamples)) {
			kept = append(kept, s)
		}
	}
	return kept
}

// clean runs the scheduled clean when the last one is older than c.Every.
func (r *runner) clean(ctx context.Context, c Schedule) error {
	period, err := Period(c.Every)
	if err != nil {
		return r.fail("clean", err)
	}
	if last := r.st.LastClean; last != nil {
		if due := last.Time.Add(period); r.now.Before(due) {
			r.logf("clean: not due until %s", due.Format(time.RFC3339))
			return nil
		}
	}

	res, err := cleanSafe(ctx, r.e)
	if ctx.Err() != nil {
		r.logf("clean: cancelled; it runs again at the next check")
		return ctx.Err()
	}
	rec := Clean{Time: r.now}
	if res != nil {
		rec.RunID, rec.Items, rec.Bytes = res.RunID, len(res.Moved), res.Bytes
	}
	switch {
	case err != nil:
		rec.Result, rec.Error = CleanFailed, err.Error()
	case len(res.Moved) == 0:
		rec.Result = CleanNothing
	default:
		rec.Result = CleanMoved
	}
	r.st.LastClean = &rec

	if err != nil {
		return r.fail("clean", err)
	}
	if len(res.Moved) == 0 {
		r.logf("clean: nothing to move")
		return nil
	}
	r.logf("clean: moved %d items (%s) to quarantine, run %s; %d skipped, %d failed",
		len(res.Moved), output.Bytes(res.Bytes), res.RunID, len(res.Skipped), len(res.Failed))
	text := fmt.Sprintf("Sapu moved %s of caches and logs to quarantine. Undo: sapu undo %s", output.Bytes(res.Bytes), res.RunID)
	if err := Notify(text); err != nil {
		r.logf("error: notify: %v", err)
	}
	return nil
}

// snapshot records the daily size snapshot of the home folder (trends) unless
// today's exists. It only reads the home folder and writes that one file.
func (r *runner) snapshot(ctx context.Context) error {
	if trends.Has(r.e, r.now) {
		r.logf("trends: today's snapshot exists")
		return nil
	}
	s, err := trends.Record(ctx, r.e, r.now, nil)
	if ctx.Err() != nil {
		r.logf("trends: cancelled; it runs again at the next check")
		return ctx.Err()
	}
	if err != nil {
		return r.fail("size snapshot", err)
	}
	r.logf("trends: saved the sizes of %s (%s, %d folders)", s.Date, output.Bytes(s.HomeBytes), len(s.Folders))
	return nil
}

// cleanSafe scans with the safe quarantine rules only, selects only safe
// quarantine items and moves them to quarantine. It never deletes for good:
// it does not expire old quarantine (engine.Options.SkipExpire) and does not
// empty its own run. It never runs commands. sapu.toml applies: [clean]
// exclude leaves rules out; [quarantine] sets the retention that the user's
// own runs of sapu expire with.
func cleanSafe(ctx context.Context, e env.Env) (*engine.Result, error) {
	cfg, err := config.Load(e)
	if err != nil {
		return nil, err
	}
	all, err := cleanerRules(e)
	if err != nil {
		return nil, err
	}
	if err := cfg.CheckExclude(all); err != nil {
		return nil, err
	}
	var safe []rules.Rule
	for _, rl := range all {
		if rl.Risk == rules.RiskSafe && rl.Action == rules.ActionQuarantine && !cfg.Excluded(rl) {
			safe = append(safe, rl)
		}
	}
	items, err := scan.Scan(ctx, e, safe, nil)
	if err != nil {
		return nil, err
	}
	p, err := engine.Plan(ctx, e, plan.KindClean, items)
	if err != nil {
		return nil, err
	}
	for i := range p.Items {
		it := &p.Items[i]
		it.Selected = it.Risk == rules.RiskSafe && it.Action == rules.ActionQuarantine
	}
	opts := engine.DefaultOptions()
	opts.MaxAge, opts.MaxBytes = cfg.MaxAge, cfg.MaxBytes
	opts.SkipExpire = true
	return apply(ctx, e, p, opts)
}
