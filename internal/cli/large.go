package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/env"
	"github.com/khmuhtadin/sapu/internal/finders"
	"github.com/khmuhtadin/sapu/internal/output"
	"github.com/khmuhtadin/sapu/internal/plan"
)

const largeLong = `Lists files of at least --min-size that were not opened or changed for
--older-than, biggest first. It does not look inside ~/Library, app bundles,
Photos libraries or git repositories. With no DIR it looks in your home folder.

Nothing is chosen for you: files are risky, because only you know whether you
still need them. Pick numbers from the list with --pick, e.g. --pick 1,3,5 or
--pick 2-4; Sapu then asks before it moves them to quarantine.`

func newLargeCmd() *cobra.Command {
	var f cleanupFlags
	var minSize, olderThan, kind, pick string
	cmd := &cobra.Command{
		Use:   "large [DIR...]",
		Short: "Find big files you have not opened in a long time",
		Long:  largeLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "large", runLarge(cmd, args, &f, minSize, olderThan, kind, pick))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	cmd.Flags().StringVar(&minSize, "min-size", "500MB", "smallest file to list, e.g. 200MB, 2GB")
	cmd.Flags().StringVar(&olderThan, "older-than", "90d", "not opened or changed for this long, e.g. 90d, 26w, 1y")
	cmd.Flags().StringVar(&kind, "type", "any", "only "+finders.LargeTypes)
	cmd.Flags().StringVar(&pick, "pick", "", "move these numbers from the list, e.g. 1,3,5 or 2-4")
	return cmd
}

func runLarge(cmd *cobra.Command, args []string, f *cleanupFlags, minSize, olderThan, kind, pick string) error {
	picks, err := foundPicks(cmd, pick)
	if err != nil {
		return err
	}
	if err := checkFoundFlags(f, picks); err != nil {
		return err
	}
	opts := finders.LargeOptions{Type: kind, Now: time.Now()}
	if opts.MinSize, err = finders.ParseSize(minSize); err != nil {
		return usageErr(fmt.Errorf("--min-size: %w", err))
	}
	if opts.OlderThan, err = finders.ParseAge(olderThan); err != nil {
		return usageErr(fmt.Errorf("--older-than: %w", err))
	}
	if !finders.ValidLargeType(kind) {
		return usageErr(fmt.Errorf("--type must be %s, not %q", finders.LargeTypes, kind))
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
		roots = []string{e.Home}
	}
	ctx := cmd.Context()
	found, err := finders.Large(ctx, e, roots, opts)
	if err != nil {
		return err
	}
	items := make([]plan.Item, len(found))
	for i, file := range found {
		items[i] = file.Item
	}
	p, err := engine.Plan(ctx, e, plan.KindLarge, items)
	if err != nil {
		return err
	}
	files := keepFound(p, found, func(f finders.File) string { return f.Item.Path })

	data := foundList[largeJSON]{Items: []largeJSON{}, Skipped: p.Skipped}
	paths := make([]string, len(files))
	for i, file := range files {
		paths[i] = file.Item.Path
		data.Items = append(data.Items, largeJSON{
			Number: i + 1, Path: file.Item.Path, Bytes: file.Item.Bytes,
			LastUsed: file.Used, Type: file.Type, Explain: file.Item.Explain,
		})
	}
	show := func(w io.Writer) {
		if len(files) == 0 {
			fmt.Fprintf(w, "No file of at least %s that you have not used for %s.\n", output.Bytes(opts.MinSize), olderThan)
			return
		}
		rows := make([][]string, len(files))
		var total int64
		for i, file := range files {
			total += file.Item.Bytes
			rows[i] = []string{fmt.Sprint(i + 1), output.Bytes(file.Item.Bytes), foundDay(file.Used), tilde(e.Home, file.Item.Path)}
		}
		writeTable(w, []string{"#", "Size", "Last used", "File"}, rows, 0, 1)
		fmt.Fprintf(w, "\n%s, %s.\n", plural(len(files), "file"), output.Bytes(total))
	}
	return runFound(cmd, "large", e, p, paths, picks, *f, show, data)
}

// largeJSON is one file in the list that `sapu large --json` prints.
type largeJSON struct {
	Number   int       `json:"number"`
	Path     string    `json:"path"`
	Bytes    int64     `json:"bytes"`
	LastUsed time.Time `json:"last_used"`
	Type     string    `json:"type"` // the extension; empty when there is none
	Explain  string    `json:"explain"`
}

// What follows is shared by large and backups: commands that list items and
// move only the ones the user picks by number.

// foundList is the data of --json when the list is only shown.
type foundList[T any] struct {
	Items   []T         `json:"items"`
	Skipped []plan.Skip `json:"skipped"` // found, but protected, whitelisted or in use
}

// foundPicks reads --pick. It returns nil when the flag is not given.
func foundPicks(cmd *cobra.Command, value string) ([]int, error) {
	if !cmd.Flags().Changed("pick") {
		return nil, nil
	}
	nums, err := finders.ParsePick(value)
	if err != nil {
		return nil, usageErr(fmt.Errorf("--pick: %w", err))
	}
	return nums, nil
}

// checkFoundFlags rejects flags that cannot work with or without --pick.
func checkFoundFlags(f *cleanupFlags, picks []int) error {
	if picks == nil {
		if f.planFile != "" {
			return usageErr(errors.New("--plan needs --pick: choose the items first"))
		}
		return nil
	}
	return f.check()
}

// checkFoundDirs rejects a DIR argument that is not a folder.
func checkFoundDirs(dirs []string) error {
	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil {
			return usageErr(err)
		}
		if !info.IsDir() {
			return usageErr(fmt.Errorf("%s is not a folder", dir))
		}
	}
	return nil
}

// keepFound returns the entries of found whose path is still in the plan (the
// engine leaves out protected, whitelisted and in-use items), in found's order.
func keepFound[T any](p *plan.Plan, found []T, pathOf func(T) string) []T {
	inPlan := map[string]bool{}
	for _, it := range p.Items {
		inPlan[it.Path] = true
	}
	var kept []T
	for _, f := range found {
		if path := pathOf(f); inPlan[path] {
			kept = append(kept, f)
			delete(inPlan, path)
		}
	}
	return kept
}

// runFound finishes a command that lists items. paths are the paths of the
// listed items, in the order of their numbers. Without --pick it shows the
// list and changes nothing; data is its --json. With --pick it selects the
// numbers and runs the plan, which shows only the picked items and asks.
func runFound(cmd *cobra.Command, name string, e env.Env, p *plan.Plan, paths []string, picks []int, f cleanupFlags, show func(io.Writer), data any) error {
	if picks == nil {
		why, err := protectWhy(e)
		if err != nil {
			return err
		}
		out := humanOut(cmd)
		show(out)
		writeSkips(out, newStyle(out), e.Home, "Not shown", p.Skipped, why, f.verbose)
		if len(paths) > 0 {
			fmt.Fprintln(out, "Nothing was changed. Pick with --pick 1,3, or mark them in sapu analyze.")
		}
		return printData(cmd, name, data)
	}

	picked := map[string]bool{}
	for _, n := range picks {
		if n > len(paths) {
			return usageErr(fmt.Errorf("--pick %d: the list has %s", n, plural(len(paths), "item")))
		}
		picked[paths[n-1]] = true
	}
	for i := range p.Items {
		p.Items[i].Selected = picked[p.Items[i].Path]
	}
	f.selectedOnly = true
	return runPlan(cmd, name, e, p, f)
}

func foundDay(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("2006-01-02")
}
