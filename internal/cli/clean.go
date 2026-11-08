package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sys/unix"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/fsop"
	"github.com/khmuhtadin/sapu/internal/optimize"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/review"
	"github.com/khmuhtadin/sapu/internal/rules"
	"github.com/khmuhtadin/sapu/internal/safefile"
	"github.com/khmuhtadin/sapu/internal/scan"
	"github.com/khmuhtadin/sapu/internal/walk"
)

// cleanupFlags are the flags clean, purge and apply share, and what a
// command tells runPlan beyond them.
type cleanupFlags struct {
	planFile     string // clean and purge only
	yes          bool
	permanent    bool
	verbose      bool
	runCommands  bool // run the selected official commands (--run-commands; packages and optimize always)
	selectedOnly bool // the table lists only selected items (apply)

	runWhy string                 // why this command runs commands without --run-commands, said in the confirmation
	notes  []string               // what the command left out and why
	admin  []optimize.AdminAction // commands that need admin rights: shown, never run
}

func (f *cleanupFlags) addApplyFlags(cmd *cobra.Command) {
	f.addConfirmFlags(cmd)
	cmd.Flags().BoolVar(&f.permanent, "permanent", false, "delete for good instead of keeping the items in quarantine")
}

// addRunCommandsFlag adds --run-commands, only for the commands that can plan
// official cleanup commands: clean, purge and apply. packages and optimize
// run their commands by design, and the rest never plan any.
func (f *cleanupFlags) addRunCommandsFlag(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.runCommands, "run-commands", false, "also run the selected official cleanup commands (not undoable)")
}

// addConfirmFlags adds --yes and --verbose, for commands without the other
// apply flags.
func (f *cleanupFlags) addConfirmFlags(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "list every path")
}

func (f *cleanupFlags) addPlanFlag(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.planFile, "plan", "", "write the plan to FILE and change nothing (- for stdout)")
}

// check rejects flag combinations that cannot work.
func (f *cleanupFlags) check() error {
	switch {
	case f.planFile != "" && f.permanent:
		return usageErr(errors.New("--plan changes nothing, so --permanent has no effect; give --permanent to apply or to the command that applies the plan"))
	case f.planFile != "" && f.runCommands:
		return usageErr(errors.New("--plan changes nothing, so --run-commands has no effect; give --run-commands to apply"))
	case f.planFile == "-" && jsonOut:
		return usageErr(errors.New("--plan - already prints the plan as JSON; drop --json"))
	case f.planFile == "" && jsonOut && !f.yes:
		return usageErr(errors.New("--json cannot ask for confirmation; add --yes, or use --plan FILE to only write the plan"))
	}
	return nil
}

func newCleanCmd() *cobra.Command {
	var f cleanupFlags
	var risk, profile string
	var only, exclude []string
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Find caches and junk, then move the selected ones to quarantine",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "clean", runClean(cmd, args, &f, risk, profile, only, exclude))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	f.addRunCommandsFlag(cmd)
	addProfileFlags(cmd, &risk, &profile)
	cmd.Flags().StringSliceVar(&only, "only", nil, "select only this rule id or category (repeatable)")
	cmd.Flags().StringSliceVar(&exclude, "exclude", nil, "do not select this rule id or category (repeatable)")
	return cmd
}

// addProfileFlags adds --risk and --profile, which clean and purge share.
func addProfileFlags(cmd *cobra.Command, risk, profile *string) {
	cmd.Flags().StringVar(risk, "risk", "", "highest risk to select: safe, moderate or risky (wins over --profile)")
	cmd.Flags().StringVar(profile, "profile", "", "conservative, standard (the default) or aggressive; see sapu config")
}

// maxRiskOf picks the highest risk a cleanup selects at the start: --risk
// when given, else --profile, else the profile of sapu.toml (standard when
// it sets none). Conservative is safe, standard moderate, aggressive risky.
func maxRiskOf(risk, profile string, cfg config.Config) (rules.Risk, error) {
	if risk != "" {
		return parseRisk(risk)
	}
	if profile == "" {
		profile = cfg.Profile
	}
	r, ok := config.ProfileRisk(profile)
	if !ok {
		return "", usageErr(fmt.Errorf("--profile must be conservative, standard or aggressive, not %q", profile))
	}
	return r, nil
}

func runClean(cmd *cobra.Command, args []string, f *cleanupFlags, risk, profile string, only, exclude []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("clean takes no arguments, got %q", args[0]))
	}
	if err := f.check(); err != nil {
		return err
	}
	e, err := loadEnv()
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	maxRisk, err := maxRiskOf(risk, profile, cfg)
	if err != nil {
		return err
	}

	rs, err := loadCleaners(cmd, e)
	if err != nil {
		return err
	}
	category := map[string]string{} // rule id -> category
	known := map[string]bool{}
	for _, r := range rs {
		category[r.ID] = r.Category
		known[r.ID] = true
		known[r.Category] = true
	}
	for _, name := range slices.Concat(only, exclude) {
		if !known[name] {
			return usageErr(fmt.Errorf("no cleanup rule or category named %q", name))
		}
	}
	if err := cfg.CheckExclude(rs); err != nil {
		return err
	}
	exclude = slices.Concat(exclude, cfg.CleanExclude)

	ctx := cmd.Context()
	if useReview(cmd, f) {
		return runReview(cmd, e, func(ctx context.Context, progress func(walk.Event)) (*plan.Plan, error) {
			items, err := scan.Scan(ctx, e, rs, progress)
			if err != nil {
				return nil, err
			}
			p, err := engine.Plan(ctx, e, plan.KindClean, items)
			if err != nil {
				return nil, err
			}
			selectItems(p, maxRisk, only, exclude, category)
			return p, nil
		})
	}
	prog := newProgress(cmd)
	items, err := scan.Scan(ctx, e, rs, prog.Scan)
	prog.Done()
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindClean, items)
	if err != nil {
		return err
	}
	selectItems(p, maxRisk, only, exclude, category)
	return runPlan(cmd, "clean", e, p, *f)
}

// useReview says whether clean opens the review screen: a person at a
// terminal, and no flag that asks for the plain flow. SAPU_NO_TUI=1 turns
// the screen off.
func useReview(cmd *cobra.Command, f *cleanupFlags) bool {
	return !jsonOut && f.planFile == "" && !f.yes && !f.permanent && !f.runCommands &&
		os.Getenv("SAPU_NO_TUI") == "" &&
		isTerminal(cmd.InOrStdin()) && isTerminal(cmd.OutOrStdout())
}

// isTerminal reports whether v is a terminal file. Unlike output.IsTTY it
// asks the terminal driver, so /dev/null (a character device) is not one.
// Tests replace it to check that the flags alone keep the text flow.
var isTerminal = func(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}

// runReview runs the review screen, then prints what was moved where the
// terminal keeps it.
func runReview(cmd *cobra.Command, e env.Env, planFn review.PlanFunc) error {
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	res, err := review.Run(cmd.Context(), e, cfg, planFn, why)
	return finishReview(cmd.OutOrStdout(), e.Home, cfg, res, err)
}

// finishReview prints the result of the review screen and returns the exit
// status of the text flow: 0, 3 when anything was skipped or failed, 130
// when cancelled.
func finishReview(w io.Writer, home string, cfg config.Config, res review.Outcome, err error) error {
	switch {
	case res.Result != nil:
		writeResult(w, home, res.Result, cfg, res.Start, res.Deleted, false)
	case err == nil:
		fmt.Fprintln(w, "Nothing was changed.")
	}
	switch {
	case err != nil:
		return err
	case res.Cancelled:
		return &exitError{code: output.ExitCancelled, err: errors.New("cancelled")}
	case res.Err != nil:
		return res.Err
	case res.Result == nil:
		return nil
	}
	if n := len(res.Result.Skipped) + len(res.Result.Failed); n > 0 {
		return partialErr(fmt.Errorf("%s skipped or failed", plural(n, "item")))
	}
	return nil
}

func parseRisk(s string) (rules.Risk, error) {
	switch r := rules.Risk(s); r {
	case rules.RiskSafe, rules.RiskModerate, rules.RiskRisky:
		return r, nil
	}
	return "", usageErr(fmt.Errorf("--risk must be safe, moderate or risky, not %q", s))
}

// selectItems sets Selected: the item's risk is at most maxRisk, it is in
// only (when given) and not in exclude. A name matches a rule id or a category.
func selectItems(p *plan.Plan, maxRisk rules.Risk, only, exclude []string, category map[string]string) {
	matches := func(names []string, it plan.Item) bool {
		return slices.Contains(names, it.Source) || slices.Contains(names, category[it.Source])
	}
	for i := range p.Items {
		it := &p.Items[i]
		it.Selected = riskRank(it.Risk) <= riskRank(maxRisk) &&
			(len(only) == 0 || matches(only, *it)) &&
			!matches(exclude, *it)
	}
}

// planSummary is what --json says about a plan.
type planSummary struct {
	SelectedItems    int   `json:"selected_items"` // selected items that Sapu moves
	SelectedBytes    int64 `json:"selected_bytes"`
	NotSelectedItems int   `json:"not_selected_items"`
	Skipped          int   `json:"skipped"`  // items not touched: protected, whitelisted, in use, duplicate
	Commands         int   `json:"commands"` // selected official commands: shown, or run with --run-commands
}

func summarize(p *plan.Plan) planSummary {
	s := planSummary{Skipped: len(p.Skipped)}
	for _, it := range p.Items {
		switch {
		case !it.Selected:
			s.NotSelectedItems++
		case it.Action == rules.ActionOfficialCommand:
			s.Commands++
		default:
			s.SelectedItems++
			s.SelectedBytes += it.Bytes
		}
	}
	return s
}

// runData is the data of --json for clean, purge, apply, packages and optimize.
type runData struct {
	PlanFile     string                 `json:"plan_file,omitempty"` // with --plan FILE
	Summary      planSummary            `json:"summary"`
	Result       *engine.Result         `json:"result,omitempty"`        // when applied
	ExpiresAfter *time.Time             `json:"expires_after,omitempty"` // earliest time a later run may expire this quarantine
	Deleted      bool                   `json:"deleted,omitempty"`       // --permanent emptied the quarantine
	Cancelled    bool                   `json:"cancelled,omitempty"`
	Notes        []string               `json:"notes,omitempty"` // packages: what was left out and why
	Admin        []optimize.AdminAction `json:"admin,omitempty"` // optimize: needs admin rights, shown only
	Space        *runSpace              `json:"space,omitempty"` // free space after the run (U4)
}

// runPlan is what clean, purge and apply do once they hold a plan: write it,
// or show it, confirm, apply and report.
func runPlan(cmd *cobra.Command, name string, e env.Env, p *plan.Plan, f cleanupFlags) error {
	if f.planFile != "" {
		return savePlan(cmd, name, p, f)
	}

	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	out := humanOut(cmd)
	view := p
	if f.selectedOnly {
		view = onlySelected(p)
	}
	writePlanTable(out, view, why, f.verbose, f.runCommands)
	writeNotes(out, f.notes)
	writeAdmin(out, f.admin)

	sum := summarize(p)
	var cmds []plan.Item
	if f.runCommands {
		cmds = selectedCommands(p)
	}
	if sum.SelectedItems == 0 && len(cmds) == 0 {
		fmt.Fprintln(out, "\nNothing to move.")
		return printData(cmd, name, runData{Summary: sum, Notes: f.notes, Admin: f.admin})
	}

	moving := fmt.Sprintf("%s (%s)", plural(sum.SelectedItems, "item"), output.Bytes(sum.SelectedBytes))
	running := plural(len(cmds), "command")
	if f.permanent && sum.SelectedItems > 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "WARNING: --permanent deletes these items for good. There is no undo.\n"+
			"Sapu moves them to quarantine and empties it right away.")
	}
	if len(cmds) > 0 {
		writeCommandWarning(cmd.ErrOrStderr(), cmds, sum.SelectedItems > 0, f.runWhy)
	}
	if !f.yes {
		fmt.Fprintln(out)
		var err error
		switch {
		case sum.SelectedItems == 0:
			err = confirmYes(cmd, fmt.Sprintf("Run %s?", running))
		case f.permanent && len(cmds) > 0:
			err = confirmDelete(cmd, fmt.Sprintf("Delete %s for good and run %s?", moving, running))
		case f.permanent:
			err = confirmDelete(cmd, fmt.Sprintf("Delete %s for good?", moving))
		case len(cmds) > 0:
			err = confirmYes(cmd, fmt.Sprintf("Move %s to quarantine and run %s?", moving, running))
		default:
			err = confirmYes(cmd, fmt.Sprintf("Move %s to quarantine?", moving))
		}
		if err != nil {
			return err
		}
	}

	opts := engine.DefaultOptions()
	opts.MaxAge, opts.MaxBytes = cfg.MaxAge, cfg.MaxBytes // sapu.toml [quarantine]
	opts.Permanent = f.permanent
	opts.RunCommands = f.runCommands
	prog := newProgress(cmd)
	opts.Progress = prog.Apply
	opts.Command = func(it plan.Item) {
		prog.Status("command", "Running "+shellJoin(it.Command)+"…")
	}
	start := time.Now()
	res, err := engine.Apply(cmd.Context(), e, p, opts)
	prog.Done()
	if res == nil {
		return err
	}

	// The engine keeps the run in quarantine when a move failed, so ask the
	// quarantine rather than trust --permanent.
	deleted := f.permanent && err == nil && len(res.Moved) > 0 && !inQuarantine(e, res.RunID)
	data := runData{Summary: sum, Result: res, Deleted: deleted, Cancelled: cmd.Context().Err() != nil, Notes: f.notes, Admin: f.admin}
	if len(res.Moved) > 0 && !deleted {
		t := start.Add(opts.MaxAge)
		data.ExpiresAfter = &t
	}
	data.Space, _ = newRunSpace(e.Home, res, deleted) // the text result reads it too; a failed read just leaves it out
	fmt.Fprintln(out)
	writeResult(out, e.Home, res, cfg, start, deleted, f.verbose)
	if jerr := printData(cmd, name, data); jerr != nil {
		return errors.Join(err, jerr)
	}

	if err != nil {
		return reported{err}
	}
	if n := len(res.Skipped) + len(res.Failed); n > 0 {
		return reported{partialErr(fmt.Errorf("%s skipped or failed", plural(n, "item")))}
	}
	return nil
}

// selectedCommands returns the selected official-command items of p.
func selectedCommands(p *plan.Plan) []plan.Item {
	var out []plan.Item
	for _, it := range p.Items {
		if it.Selected && it.Action == rules.ActionOfficialCommand {
			out = append(out, it)
		}
	}
	return out
}

// onlySelected returns a copy of p without the items that are not selected.
func onlySelected(p *plan.Plan) *plan.Plan {
	view := *p
	view.Items = nil
	for _, it := range p.Items {
		if it.Selected {
			view.Items = append(view.Items, it)
		}
	}
	return &view
}

// savePlan writes the plan to f.planFile (- for stdout) and changes nothing
// else. It replaces a regular file of this user that is already there, and
// refuses a folder, a symlink, any other non-regular file and a file with
// other names (hard links).
func savePlan(cmd *cobra.Command, name string, p *plan.Plan, f cleanupFlags) error {
	file := f.planFile
	var buf bytes.Buffer
	if err := plan.Write(&buf, p); err != nil {
		return err
	}
	if file == "-" {
		_, err := cmd.OutOrStdout().Write(buf.Bytes())
		return err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// a new file
	case err != nil:
		return err
	case info.IsDir():
		return fmt.Errorf("%s is a folder; give --plan a file name", file)
	case info.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink; Sapu will not write through it, so give the real file name", file)
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file; give --plan a file name", file)
	}
	// safefile checks the opened file before it cuts anything: a hard link
	// to a document, or a file of another user, is refused, never replaced.
	if err := safefile.Write(filepath.Dir(abs), abs, buf.Bytes(), 0o600); err != nil {
		return err
	}

	sum := summarize(p)
	out := humanOut(cmd)
	var parts []string
	if sum.SelectedItems > 0 || sum.Commands == 0 {
		parts = append(parts, fmt.Sprintf("%s (%s)", plural(sum.SelectedItems, "item"), output.Bytes(sum.SelectedBytes)))
	}
	if sum.Commands > 0 {
		parts = append(parts, plural(sum.Commands, "command"))
	}
	fmt.Fprintf(out, "Plan written to %s: %s selected. Nothing was changed.\n", file, strings.Join(parts, " and "))
	switch {
	case sum.Commands > 0 && f.runCommands:
		fmt.Fprintf(out, "Apply it with: sapu apply %s --run-commands\n", file)
	case sum.Commands > 0:
		fmt.Fprintf(out, "Apply it with: sapu apply %s\n", file)
		fmt.Fprintf(out, "Add --run-commands to also run its %s; without it they are only shown.\n", plural(sum.Commands, "command"))
	default:
		fmt.Fprintf(out, "Apply it with: sapu apply %s\n", file)
	}
	writeNotes(out, f.notes)
	return printData(cmd, name, runData{PlanFile: abs, Summary: sum, Notes: f.notes, Admin: f.admin})
}

// inQuarantine reports whether run still holds items in quarantine. A
// quarantine that cannot be read counts as holding them.
func inQuarantine(e env.Env, run string) bool {
	sizes, err := fsop.QuarantineSize(e)
	if err != nil {
		return true
	}
	_, ok := sizes[run]
	return ok
}
