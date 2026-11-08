package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/khmuhtadin/sapu/internal/engine"
	"github.com/khmuhtadin/sapu/internal/plan"
	"github.com/khmuhtadin/sapu/internal/tidy"
)

const tidyLong = `Lists empty folders, symlinks whose target is gone and files with nothing in
them. A folder counts as empty when it holds only Finder's .DS_Store files and
other empty folders; only the topmost one is listed.

Without DIR, Sapu looks in ~/Downloads, ~/Desktop and ~/Documents. It never
looks inside ~/Library, app bundles, git repositories or node_modules. Dotfiles
and files such as *.lock and *.pid are not listed as empty files.

Nothing is chosen for you, because an app may expect a folder or a marker file.
Pick numbers with --pick, e.g. --pick 1,3 or --pick 2-4, or take everything with
--pick-all. Sapu asks before it moves them to quarantine; undo puts them back.`

func newTidyCmd() *cobra.Command {
	var f cleanupFlags
	var pick string
	var pickAll bool
	cmd := &cobra.Command{
		Use:   "tidy [DIR...]",
		Short: "Find empty folders, broken links and empty files",
		Long:  tidyLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return finish(cmd, "tidy", runTidy(cmd, args, &f, pick, pickAll))
		},
	}
	f.addPlanFlag(cmd)
	f.addApplyFlags(cmd)
	addListedPickFlags(cmd, &pick, &pickAll, "item")
	return cmd
}

func runTidy(cmd *cobra.Command, args []string, f *cleanupFlags, pick string, pickAll bool) error {
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
		roots = tidy.DefaultRoots(e)
	}
	ctx := cmd.Context()
	found, err := tidy.Find(ctx, e, roots)
	if err != nil {
		return err
	}
	p, err := engine.Plan(ctx, e, plan.KindTidy, tidy.Items(found))
	if err != nil {
		return err
	}

	// Number the entries that are still in the plan (the engine leaves out
	// protected, whitelisted and in-use ones).
	explain := map[string]string{} // path -> explanation
	for _, it := range p.Items {
		explain[it.Path] = it.Explain
	}
	data := tidyList{Items: []tidyItem{}, Skipped: p.Skipped}
	var paths []string
	counts := map[tidy.Kind]int{}
	for _, en := range found {
		why, ok := explain[en.Path]
		if !ok {
			continue
		}
		paths = append(paths, en.Path)
		counts[en.Kind]++
		data.Items = append(data.Items, tidyItem{Number: len(paths), Path: en.Path, Kind: string(en.Kind), Explain: why})
	}
	show := func(w io.Writer) {
		if len(data.Items) == 0 {
			fmt.Fprintln(w, "No empty folders, broken links or empty files found.")
			return
		}
		rows := make([][]string, len(data.Items))
		for i, it := range data.Items {
			rows[i] = []string{fmt.Sprint(it.Number), tidyKindName[tidy.Kind(it.Kind)], tilde(e.Home, it.Path)}
		}
		writeTable(w, []string{"#", "What", "Path"}, rows)
		fmt.Fprintf(w, "\n%s, %s, %s.\n",
			plural(counts[tidy.EmptyFolder], "empty folder"), plural(counts[tidy.BrokenLink], "broken link"), plural(counts[tidy.EmptyFile], "empty file"))
	}
	return runListed(cmd, "tidy", e, p, paths, picks, pickAll, *f, show, data)
}

var tidyKindName = map[tidy.Kind]string{
	tidy.EmptyFolder: "empty folder",
	tidy.BrokenLink:  "broken link",
	tidy.EmptyFile:   "empty file",
}

// tidyList is the data of --json when the list is only shown.
type tidyList struct {
	Items   []tidyItem  `json:"items"`
	Skipped []plan.Skip `json:"skipped"` // found, but protected, whitelisted or in use
}

type tidyItem struct {
	Number  int    `json:"number"`
	Path    string `json:"path"`
	Kind    string `json:"kind"` // empty-folder, broken-link or empty-file
	Explain string `json:"explain"`
}
