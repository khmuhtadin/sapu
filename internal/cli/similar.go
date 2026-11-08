package cli

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/similar"
)

const similarLong = `Finds photos that look alike (burst shots, edits, screenshots of the same
thing), keeps the biggest and newest of each group and lists the others.

Without DIR, Sapu looks in ~/Pictures, ~/Downloads and ~/Desktop. It never
looks inside a Photos library, ~/Library, app bundles or git repositories, and
it skips files under 50 KB. Videos are not handled.

Similar is not identical, so nothing is chosen for you. Look at the list, then
pick numbers with --pick, e.g. --pick 1,3 or --pick 2-4, or take every photo
that is not kept with --pick-all. Sapu asks before it moves them to quarantine.`

func newSimilarCmd() *cobra.Command {
	var f cleanupFlags
	var threshold int
	var pick string
	var pickAll bool
	cmd := &cobra.Command{
		Use:   "similar [DIR...]",
		Short: "Find photos that look alike and keep the best one",
		Long:  similarLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "similar", runSimilar(cmd, args, &f, threshold, pick, pickAll))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().IntVar(&threshold, "threshold", similar.DefaultThreshold, "how different photos may be, 0 to 64; lower is stricter")
	addListedPickFlags(cmd, &pick, &pickAll, "photo")
	return cmd
}

func runSimilar(cmd *cobra.Command, args []string, f *cleanupFlags, threshold int, pick string, pickAll bool) error {
	if threshold < 0 || threshold > 64 {
		return usageErr(fmt.Errorf("--threshold must be between 0 and 64, not %d", threshold))
	}
	picks, err := listedPicks(cmd, pick, pickAll)
	if err != nil {
		return err
	}
	if err := checkListedFlags(f, picks); err != nil {
		return err
	}
	if err := checkFoundDirs(args); err != nil {
		return err
	}

	e, err := loadEnv()
	if err != nil {
		return err
	}
	roots := args
	if len(roots) == 0 {
		roots = similar.DefaultRoots(e)
	}
	ctx := cmd.Context()
	prog := newProgress(cmd)
	found, err := similar.Find(ctx, e, roots, similar.Options{
		Threshold: threshold,
		Status:    func(text string) { prog.Status("similar", text) },
	})
	prog.Done()
	var tooMany *similar.TooManyError
	if errors.As(err, &tooMany) {
		return usageErr(err)
	}
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindSimilar, similar.Items(found.Groups))
	if err != nil {
		return err
	}

	// Number the photos that are still in the plan (the engine leaves out
	// protected, whitelisted and in-use ones), group by group.
	explain := map[string]string{} // path -> explanation
	for _, it := range p.Items {
		explain[it.Path] = it.Explain
	}
	data := similarList{Groups: []similarGroup{}, Skipped: p.Skipped, Images: found.Images, Unreadable: found.Unreadable}
	var paths []string
	for _, g := range found.Groups {
		sg := similarGroup{Keep: similarKeep{Path: g.Keep.Path, Pixels: g.Keep.Pixels}}
		for _, photo := range g.Extras {
			if _, ok := explain[photo.Path]; !ok {
				continue
			}
			paths = append(paths, photo.Path)
			sg.Extras = append(sg.Extras, similarExtra{
				Number: len(paths), Path: photo.Path, Bytes: photo.Bytes, Difference: g.Distance(photo),
				Explain: explain[photo.Path],
			})
			sg.Bytes += photo.Bytes
		}
		if len(sg.Extras) > 0 {
			data.Groups = append(data.Groups, sg)
		}
	}
	show := func(w io.Writer) { writeSimilar(w, e.Home, data) }
	return runListed(cmd, "similar", e, p, paths, picks, pickAll, *f, show, data)
}

// similarList is the data of --json when the list is only shown.
type similarList struct {
	Groups     []similarGroup `json:"groups"`
	Skipped    []plan.Skip    `json:"skipped"` // found, but protected, whitelisted or in use
	Images     int            `json:"images"`  // images looked at
	Unreadable int            `json:"unreadable"`
}

type similarGroup struct {
	Keep   similarKeep    `json:"keep"`
	Extras []similarExtra `json:"extras"`
	Bytes  int64          `json:"bytes"` // allocated bytes of the extras
}

type similarKeep struct {
	Path   string `json:"path"`
	Pixels int64  `json:"pixels"` // 0 when unknown
}

type similarExtra struct {
	Number     int    `json:"number"`
	Path       string `json:"path"`
	Bytes      int64  `json:"bytes"`
	Difference int    `json:"difference"` // of 64 bits
	Explain    string `json:"explain"`
}

func writeSimilar(w io.Writer, home string, data similarList) {
	st := newStyle(w)
	if len(data.Groups) == 0 {
		fmt.Fprintf(w, "No similar photos found among %s.\n", plural(data.Images, "image"))
	}
	var photos int
	var total int64
	for i, g := range data.Groups {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, st.bold(row(0, fmt.Sprintf("Looks alike · %s", plural(len(g.Extras)+1, "photo")), output.Bytes(g.Bytes))))
		keep := "  keep  " + tilde(home, g.Keep.Path)
		if g.Keep.Pixels > 0 {
			fmt.Fprintln(w, row(0, keep, fmt.Sprintf("%.1f MP", float64(g.Keep.Pixels)/1e6)))
		} else {
			fmt.Fprintln(w, keep)
		}
		for _, x := range g.Extras {
			fmt.Fprintln(w, row(0, fmt.Sprintf("%3d  move  %s", x.Number, tilde(home, x.Path)), fmt.Sprintf("%d/64 %s", x.Difference, output.Bytes(x.Bytes))))
		}
		photos += len(g.Extras)
		total += g.Bytes
	}
	if len(data.Groups) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, st.bold(row(0, fmt.Sprintf("Total · %s in %s", plural(photos, "extra photo"), plural(len(data.Groups), "group")), output.Bytes(total))))
		writeWrapped(w, st, 0, "Similar is not identical. Look at each group before you pick. The number after each photo is how different it is, out of 64.")
	}
	if data.Unreadable > 0 {
		fmt.Fprintf(w, "\n%s could not be read and was left out.\n", plural(data.Unreadable, "image"))
	}
}

// What follows is shared by similar and tidy: commands that list items and
// move only the ones the user picks by number, or all of them.

func addListedPickFlags(cmd *cobra.Command, pick *string, pickAll *bool, noun string) {
	cmd.Flags().StringVar(pick, "pick", "", "move these numbers from the list, e.g. 1,3,5 or 2-4")
	cmd.Flags().BoolVar(pickAll, "pick-all", false, "move every "+noun+" in the list")
}

// listedPicks reads --pick and --pick-all. It returns nil when the list is
// only shown. For --pick-all it returns an empty, not nil, list: runListed
// fills it in once it knows how many items there are.
func listedPicks(cmd *cobra.Command, pick string, pickAll bool) ([]int, error) {
	if pickAll && cmd.Flags().Changed("pick") {
		return nil, usageErr(errors.New("use --pick or --pick-all, not both"))
	}
	if pickAll {
		return []int{}, nil
	}
	return foundPicks(cmd, pick)
}

// checkListedFlags checks the flags that cannot work together. Showing the
// list needs no check; writing a plan or moving items does.
func checkListedFlags(f *cleanupFlags, picks []int) error {
	if picks == nil && f.planFile == "" {
		return nil
	}
	return f.check()
}

// runListed finishes a command that lists items. paths are the paths of the
// listed items, in the order of their numbers. Without --pick it shows the
// list, and with --plan FILE also writes the plan with nothing selected; it
// changes nothing else. With --pick or --pick-all it selects those items and
// runs the plan, which shows only them and asks.
func runListed(cmd *cobra.Command, name string, e env.Env, p *plan.Plan, paths []string, picks []int, pickAll bool, f cleanupFlags, show func(io.Writer), data any) error {
	if pickAll && len(paths) > 0 {
		picks = make([]int, len(paths))
		for i := range picks {
			picks[i] = i + 1
		}
	}
	if len(paths) == 0 {
		picks = nil
	}
	if picks != nil {
		return runFound(cmd, name, e, p, paths, picks, f, show, data)
	}

	why, err := protectWhy(e)
	if err != nil {
		return err
	}
	out := humanOut(cmd)
	show(out)
	writeSkips(out, newStyle(out), e.Home, "Not shown", p.Skipped, why, f.verbose)
	if f.planFile != "" {
		return savePlan(cmd, name, p, f)
	}
	if len(paths) > 0 {
		fmt.Fprintln(out, "Nothing was changed. Pick with --pick 1,3 or --pick-all.")
	}
	return printData(cmd, name, data)
}
