package cli

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/config"
	"github.com/khmuhtadin/sapu/internal/dupes"
	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
)

// maxDupeGroups is how many groups the table lists unless --verbose.
const maxDupeGroups = 20

const dupesCloneNote = "APFS clones share storage with the file they were copied from, and Sapu cannot tell clones apart. The space you get back can be less than shown."

// dupesReplaceNote explains --replace-with-clone, in the table and before the question.
const dupesReplaceNote = "Each extra copy is replaced by an APFS clone of the kept file. The file stays where it is and opens the same, but the duplicate space is shared. Changing one copy later does not change the other."

func newDupesCmd() *cobra.Command {
	var f cleanupFlags
	var keep, minSize string
	var clone bool
	cmd := &cobra.Command{
		Use:   "dupes [DIR...]",
		Short: "Find duplicate files and move the extra copies to quarantine",
		Long: "Find files with the same content and move all but one copy of each to quarantine.\n\n" +
			"Without DIR, Sapu looks in ~/Downloads, ~/Desktop, ~/Documents, ~/Pictures,\n" +
			"~/Movies and ~/Music. It never looks inside ~/Library, app bundles, photo and\n" +
			"music libraries, .git or node_modules. Hard links are not duplicates.\n\n" +
			"With --replace-with-clone, each extra copy stays where it is: Sapu moves it to\n" +
			"quarantine and puts an APFS clone of the kept file in its place, so the two\n" +
			"share their space. `sapu undo` puts the original back.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "dupes", runDupes(cmd, args, &f, keep, minSize, clone))
		},
	}
	f.addPlanFlag(cmd)
	cmd.Flags().BoolVar(&f.yes, "yes", false, "do not ask for confirmation")
	cmd.Flags().BoolVarP(&f.verbose, "verbose", "v", false, "list every group")
	cmd.Flags().StringVar(&keep, "keep", "oldest", "which copy stays: oldest, newest or shortest-path")
	cmd.Flags().StringVar(&minSize, "min-size", "1MB", "ignore files smaller than this, e.g. 500KB, 10MB, 1GB")
	cmd.Flags().BoolVar(&clone, "replace-with-clone", false, "replace each extra copy by an APFS clone of the kept file instead of only moving it")
	return cmd
}

func runDupes(cmd *cobra.Command, args []string, f *cleanupFlags, keep, minSize string, clone bool) error {
	policy := dupes.Keep(keep)
	switch policy {
	case dupes.KeepOldest, dupes.KeepNewest, dupes.KeepShortestPath:
	default:
		return usageErr(fmt.Errorf("--keep must be oldest, newest or shortest-path, not %q", keep))
	}
	min, err := parseDupesMinSize(minSize)
	if err != nil {
		return usageErr(fmt.Errorf("--min-size: %w", err))
	}
	if err := f.check(); err != nil {
		return err
	}

	e, err := loadEnv()
	if err != nil {
		return err
	}
	roots := args
	if len(roots) == 0 {
		roots = dupes.DefaultRoots(e)
	}

	ctx := cmd.Context()
	prog := newProgress(cmd)
	found, err := dupes.Find(ctx, e, roots, dupes.Options{
		MinSize: min,
		Keep:    policy,
		Scan:    prog.Scan,
		Status:  func(text string) { prog.Status("scan", text) },
	})
	prog.Done()
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindDupes, dupes.Items(found))
	if err != nil {
		return err
	}
	for i := range p.Items { // the user asked for duplicates, so all extras are selected
		p.Items[i].Selected = true
		if clone && p.Items[i].KeepCopy != nil {
			p.Items[i].CloneFrom = p.Items[i].KeepCopy.Path
			p.Items[i].Explain = fmt.Sprintf("Same content as %s. It is replaced by an APFS clone of that file, and this copy goes to quarantine.", p.Items[i].KeepCopy.Path)
		}
	}
	groups := dupeGroupsOf(p)

	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	out := humanOut(cmd)
	writeDupes(out, p, groups, why, f.verbose, clone)
	if f.planFile != "" {
		return saveDupesPlan(cmd, p, f.planFile, groups)
	}
	sum := summarize(p)
	if sum.SelectedItems == 0 {
		return printData(cmd, "dupes", dupesData{runData{Summary: sum}, groups})
	}

	if !f.yes {
		fmt.Fprintln(out)
		moving := fmt.Sprintf("%s (%s)", extraCopies(sum.SelectedItems), output.Bytes(sum.SelectedBytes))
		question := fmt.Sprintf("Move %s to quarantine?", moving)
		if clone { // the table above ends with dupesReplaceNote
			question = fmt.Sprintf("Replace %s by clones of the kept files? The originals go to quarantine.", moving)
		}
		if err := confirmYes(cmd, question); err != nil {
			return err
		}
	}
	return applyDupes(cmd, e, p, groups, f.verbose)
}

// applyDupes moves the selected extras and reports. It is what runPlan does
// after its table, with the groups in the JSON.
func applyDupes(cmd *cobra.Command, e env.Env, p *plan.Plan, groups []dupeGroup, verbose bool) error {
	cfg, err := config.Load(e)
	if err != nil {
		return err
	}
	opts := engine.DefaultOptions()
	opts.MaxAge, opts.MaxBytes = cfg.MaxAge, cfg.MaxBytes // sapu.toml [quarantine]
	prog := newProgress(cmd)
	opts.Progress = prog.Apply
	start := time.Now()
	res, err := engine.Apply(cmd.Context(), e, p, opts)
	prog.Done()
	if res == nil {
		return err
	}

	data := dupesData{runData{Summary: summarize(p), Result: res, Cancelled: cmd.Context().Err() != nil}, groups}
	if res.RunID != "" {
		t := expiresAfter(start, cfg)
		data.ExpiresAfter = &t
	}
	out := humanOut(cmd)
	fmt.Fprintln(out)
	writeResult(out, e.Home, res, cfg, start, false, verbose)
	if jerr := printData(cmd, "dupes", data); jerr != nil {
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

// writeClones says which extra copies were replaced by a clone and which
// were only moved, and that the originals still take space. It prints
// nothing for a run without clone items (every command shares writeResult).
func writeClones(w io.Writer, home string, res *engine.Result, verbose bool) {
	st := newStyle(w)
	cloned := map[string]bool{}
	for _, it := range res.Cloned {
		cloned[it.ID] = true
	}
	var notPlaced []plan.Item
	for _, it := range res.Moved {
		if it.CloneFrom != "" && !cloned[it.ID] {
			notPlaced = append(notPlaced, it)
		}
	}
	if len(res.Cloned) == 0 && len(notPlaced) == 0 {
		return
	}
	if len(res.Cloned) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(fmt.Sprintf("Clones placed (%d)", len(res.Cloned))))
		shown := res.Cloned
		if !verbose && len(shown) > maxDupeGroups {
			shown = shown[:maxDupeGroups]
		}
		for _, it := range shown {
			fmt.Fprintf(w, "  %s %s\n", tilde(home, it.Path), st.dim("(clone of "+tilde(home, it.CloneFrom)+")"))
		}
		if len(shown) < len(res.Cloned) {
			fmt.Fprintf(w, "  … and %d more (--verbose lists them all)\n", len(res.Cloned)-len(shown))
		}
	}
	if len(notPlaced) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(fmt.Sprintf("Clones not placed (%d)", len(notPlaced))))
		for _, it := range notPlaced {
			fmt.Fprintf(w, "  %s\n", tilde(home, it.Path))
		}
		fmt.Fprintln(w, st.dim("  They are in quarantine with nothing in their place."))
		fmt.Fprintln(w, st.dim("  To put them back: `sapu undo "+res.RunID+"`"))
	}
	if len(res.Moved) > 0 {
		fmt.Fprintln(w)
		writeWrapped(w, st, 0, "The quarantined originals still use space until the quarantine is emptied. The clones share their space with the kept files.")
	}
}

// saveDupesPlan writes the plan like savePlan does. With --json it prints one
// envelope that also holds the groups.
func saveDupesPlan(cmd *cobra.Command, p *plan.Plan, file string, groups []dupeGroup) error {
	if !jsonOut {
		return savePlan(cmd, "dupes", p, cleanupFlags{planFile: file})
	}
	// savePlan prints an envelope without the groups; keep that one off stdout.
	stdout := cmd.OutOrStdout()
	cmd.SetOut(io.Discard)
	err := savePlan(cmd, "dupes", p, cleanupFlags{planFile: file})
	cmd.SetOut(stdout)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return err
	}
	return printData(cmd, "dupes", dupesData{runData{PlanFile: abs, Summary: summarize(p)}, groups})
}

// dupesData is the data of --json: what clean says, and the groups.
type dupesData struct {
	runData
	Groups []dupeGroup `json:"groups"`
}

type dupeGroup struct {
	Keep   dupeFile   `json:"keep"`
	Extras []dupeFile `json:"extras"`
	Bytes  int64      `json:"bytes"` // allocated bytes of the extras
}

type dupeFile struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes,omitempty"` // extras only
}

// dupeGroupsOf rebuilds the groups from the items that are left in the plan,
// largest first. The copy that stays is the one every item of a group names.
func dupeGroupsOf(p *plan.Plan) []dupeGroup {
	at := map[string]int{} // path of the copy that stays -> index in groups
	groups := []dupeGroup{}
	for _, it := range p.Items {
		if it.KeepCopy == nil {
			continue
		}
		i, ok := at[it.KeepCopy.Path]
		if !ok {
			i = len(groups)
			at[it.KeepCopy.Path] = i
			groups = append(groups, dupeGroup{Keep: dupeFile{Path: it.KeepCopy.Path}})
		}
		groups[i].Extras = append(groups[i].Extras, dupeFile{Path: it.Path, Bytes: it.Bytes})
		groups[i].Bytes += it.Bytes
	}
	slices.SortFunc(groups, func(a, b dupeGroup) int {
		return cmp.Or(cmp.Compare(b.Bytes, a.Bytes), cmp.Compare(a.Keep.Path, b.Keep.Path))
	})
	for i := range groups {
		slices.SortFunc(groups[i].Extras, func(a, b dupeFile) int { return cmp.Compare(a.Path, b.Path) })
	}
	return groups
}

// writeDupes prints the groups, what is not touched, and the clone notes.
func writeDupes(w io.Writer, p *plan.Plan, groups []dupeGroup, why func(string) string, verbose, clone bool) {
	st := newStyle(w)
	if len(groups) == 0 {
		fmt.Fprintln(w, "No duplicate files found.")
	}

	shown := groups
	if !verbose && len(shown) > maxDupeGroups {
		shown = shown[:maxDupeGroups]
	}
	for i, g := range shown {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, st.bold(row(0, fmt.Sprintf("Same content · %s", dupesCopies(len(g.Extras)+1)), output.Bytes(g.Bytes))))
		fmt.Fprintf(w, "  keep  %s\n", tilde(p.Home, g.Keep.Path))
		verb := "move  "
		if clone {
			verb = "clone "
		}
		for _, x := range g.Extras {
			fmt.Fprintln(w, row(2, verb+tilde(p.Home, x.Path), output.Bytes(x.Bytes)))
		}
	}
	if len(shown) < len(groups) {
		fmt.Fprintf(w, "\n… and %d more groups (--verbose lists them all)\n", len(groups)-len(shown))
	}

	if len(groups) > 0 {
		sum := summarize(p)
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(row(0, fmt.Sprintf("Total · %s in %s", extraCopies(sum.SelectedItems), plural(len(groups), "group")), output.Bytes(sum.SelectedBytes))))
		if clone {
			writeWrapped(w, st, 0, dupesReplaceNote)
		}
		writeWrapped(w, st, 0, dupesCloneNote)
	}
	writeSkips(w, st, p.Home, "Not touched", p.Skipped, why, verbose)
}

func extraCopies(n int) string {
	if n == 1 {
		return "1 extra copy"
	}
	return fmt.Sprintf("%d extra copies", n)
}

func dupesCopies(n int) string {
	if n == 1 {
		return "1 copy"
	}
	return fmt.Sprintf("%d copies", n)
}

// parseDupesMinSize reads a size such as 500KB, 10MB or 1.5GB. Units are
// decimal, like the sizes Sapu prints.
func parseDupesMinSize(s string) (int64, error) {
	text := strings.ToUpper(strings.TrimSpace(s))
	text = strings.TrimSuffix(text, "B")
	mult := int64(1)
	for i, unit := range "KMGT" {
		if strings.HasSuffix(text, string(unit)) {
			text = strings.TrimSuffix(text, string(unit))
			mult = 1
			for range i + 1 {
				mult *= 1000
			}
		}
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	bytes := n * float64(mult)
	if err != nil || !(bytes >= 1 && bytes < 1e18) { // also rejects NaN and Inf
		return 0, fmt.Errorf("%q is not a size of at least 1 byte; use a number with an optional unit such as KB, MB or GB", s)
	}
	return int64(bytes), nil
}
